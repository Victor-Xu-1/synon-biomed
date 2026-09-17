package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"synon-go/internal/skills"
)

const agentSkillRuntimeMarker = ".synon-skill-bundle.sha256"
const agentSkillRuntimePreparationTimeout = 15 * time.Second

type agentSkillRuntimeFile struct {
	relativePath string
	content      []byte
	digest       string
	executable   bool
}

type agentSkillRuntimeBundle struct {
	digest string
	files  []agentSkillRuntimeFile
}

func (s *Server) materializeAgentSkillRuntime(
	ctx context.Context,
	sessionID string,
	skill skills.Skill,
) (skills.Skill, error) {
	if strings.HasPrefix(skill.Path, "builtin:") {
		return skill, nil
	}
	if !strings.Contains(skill.Body, "${SYNON_SKILL_DIR}") {
		return skill, nil
	}
	bundle, hasRuntimeResources, err := loadAgentSkillRuntimeBundle(skill)
	if err != nil {
		return skills.Skill{}, err
	}
	if !hasRuntimeResources {
		return skills.Skill{}, errors.New("skill references its runtime directory but has no bundled resources")
	}
	preparationCtx := ctx
	if preparationCtx == nil {
		preparationCtx = context.Background()
	}
	preparationCtx, cancelPreparation := context.WithTimeout(preparationCtx, agentSkillRuntimePreparationTimeout)
	defer cancelPreparation()
	identity := s.resolveAgentKernelContext(preparationCtx, sessionID)
	if identity == nil {
		return skills.Skill{}, errors.New("skill runtime resources require an authorized project workspace")
	}
	workspaceRoot, err := s.ensureAgentWorkspaceRoot(identity)
	if err != nil {
		return skills.Skill{}, err
	}
	directory, err := materializeAgentSkillRuntimeBundle(workspaceRoot, skill.Name, bundle)
	if err != nil {
		return skills.Skill{}, err
	}
	skill.Path = filepath.Join(directory, "SKILL.md")
	return skill, nil
}

// prepareAgentSkillRuntimeContexts makes resource-bearing Skills usable in the
// very first provider request. Explicitly selected Skills are already included
// in the runner's system context before the model can invoke the Skill tool, so
// their bundled resources and runtime-directory placeholder must be resolved at
// the same authorized project boundary used by tool execution.
func (s *Server) prepareAgentSkillRuntimeContexts(
	ctx context.Context,
	sessionID string,
	selected []skills.Skill,
) ([]skills.Skill, error) {
	if len(selected) == 0 {
		return nil, nil
	}
	prepared := make([]skills.Skill, len(selected))
	for index, skill := range selected {
		effective, err := s.materializeAgentSkillRuntime(ctx, sessionID, skill)
		if err != nil {
			return nil, fmt.Errorf("materialize selected skill %q runtime: %w", skill.Name, err)
		}
		if strings.Contains(effective.Body, "${SYNON_SKILL_DIR}") {
			effective.Body = renderSkillPrompt(effective, "")
		}
		prepared[index] = effective
	}
	return prepared, nil
}

func loadAgentSkillRuntimeBundle(skill skills.Skill) (agentSkillRuntimeBundle, bool, error) {
	files, err := workspaceSkillFiles(skill)
	if err != nil {
		return agentSkillRuntimeBundle{}, false, err
	}
	if len(files) == 0 {
		return agentSkillRuntimeBundle{}, false, errors.New("skill runtime bundle is empty")
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	root := filepath.Dir(skill.Path)
	bundle := agentSkillRuntimeBundle{files: make([]agentSkillRuntimeFile, 0, len(files))}
	hasRuntimeResources := false
	bundleHash := sha256.New()
	for _, file := range files {
		relative := filepath.ToSlash(strings.TrimSpace(file.Path))
		if relative == "" || relative == "." || relative == agentSkillRuntimeMarker {
			return agentSkillRuntimeBundle{}, false, errors.New("skill runtime bundle contains an invalid path")
		}
		if relative != "SKILL.md" {
			hasRuntimeResources = true
		}
		source, err := openGrantedRegularFile(file.absolute, []hostGrant{{Path: root, Mode: "read"}})
		if err != nil {
			return agentSkillRuntimeBundle{}, false, errors.New("skill runtime resource is unavailable")
		}
		info, statErr := source.Stat()
		if statErr != nil || !info.Mode().IsRegular() || info.Size() != file.Size {
			_ = source.Close()
			return agentSkillRuntimeBundle{}, false, errors.New("skill runtime resource changed while it was inspected")
		}
		content, readErr := io.ReadAll(io.LimitReader(source, file.Size+1))
		_ = source.Close()
		if readErr != nil || int64(len(content)) != file.Size {
			return agentSkillRuntimeBundle{}, false, errors.New("skill runtime resource changed while it was read")
		}
		content, executable := normalizeAgentSkillRuntimeResource(relative, content)
		contentHash := sha256.Sum256(content)
		digest := hex.EncodeToString(contentHash[:])
		_, _ = bundleHash.Write([]byte(relative))
		_, _ = bundleHash.Write([]byte{0})
		_, _ = bundleHash.Write(contentHash[:])
		_, _ = bundleHash.Write([]byte{0})
		if executable {
			_, _ = bundleHash.Write([]byte{1})
		} else {
			_, _ = bundleHash.Write([]byte{0})
		}
		bundle.files = append(bundle.files, agentSkillRuntimeFile{
			relativePath: relative,
			content:      content,
			digest:       digest,
			executable:   executable,
		})
	}
	bundle.digest = hex.EncodeToString(bundleHash.Sum(nil))
	return bundle, hasRuntimeResources, nil
}

func materializeAgentSkillRuntimeBundle(
	workspaceRoot string,
	skillName string,
	bundle agentSkillRuntimeBundle,
) (string, error) {
	if !agentSkillRuntimeValidSHA256(bundle.digest) || len(bundle.files) == 0 {
		return "", errors.New("skill runtime bundle digest is invalid")
	}
	cacheRoot, err := secureEnsureAgentWorkspaceDirectory(
		filepath.Join(workspaceRoot, ".synon", "runtime", "skills", agentSkillRuntimeDirectoryName(skillName)),
		0o700,
	)
	if err != nil {
		return "", errors.New("skill runtime cache is unavailable")
	}
	target := filepath.Join(cacheRoot, bundle.digest)
	if !hostPathWithin(cacheRoot, target) || filepath.Clean(target) == filepath.Clean(cacheRoot) {
		return "", errors.New("skill runtime target escaped its cache")
	}
	if valid, verifyErr := verifyAgentSkillRuntimeBundle(target, bundle); verifyErr != nil {
		return "", verifyErr
	} else if valid {
		return target, nil
	}
	if _, statErr := os.Lstat(target); statErr == nil {
		if removeErr := os.RemoveAll(target); removeErr != nil {
			return "", errors.New("invalid skill runtime cache could not be replaced")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", errors.New("skill runtime cache could not be inspected")
	}

	staging := filepath.Join(cacheRoot, ".stage-"+uuid.NewString())
	if !hostPathWithin(cacheRoot, staging) || filepath.Clean(staging) == filepath.Clean(cacheRoot) {
		return "", errors.New("skill runtime staging path escaped its cache")
	}
	if err := os.Mkdir(staging, 0o700); err != nil {
		return "", errors.New("skill runtime staging directory could not be created")
	}
	defer func() { _ = os.RemoveAll(staging) }()
	for _, file := range bundle.files {
		destination := filepath.Join(staging, filepath.FromSlash(file.relativePath))
		if !hostPathWithin(staging, destination) || filepath.Clean(destination) == filepath.Clean(staging) {
			return "", errors.New("skill runtime resource escaped its staging directory")
		}
		parent, err := secureEnsureAgentWorkspaceDirectory(filepath.Dir(destination), 0o700)
		if err != nil || filepath.Clean(parent) != filepath.Clean(filepath.Dir(destination)) {
			return "", errors.New("skill runtime resource directory could not be created")
		}
		mode := os.FileMode(0o444)
		if file.executable {
			mode = 0o555
		}
		writer, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return "", errors.New("skill runtime resource could not be created")
		}
		written, writeErr := writer.Write(file.content)
		closeErr := writer.Close()
		if writeErr != nil || closeErr != nil || written != len(file.content) {
			return "", errors.New("skill runtime resource could not be written")
		}
	}
	if err := os.WriteFile(filepath.Join(staging, agentSkillRuntimeMarker), []byte(bundle.digest+"\n"), 0o444); err != nil {
		return "", errors.New("skill runtime marker could not be written")
	}
	if valid, verifyErr := verifyAgentSkillRuntimeBundle(staging, bundle); verifyErr != nil || !valid {
		if verifyErr != nil {
			return "", verifyErr
		}
		return "", errors.New("skill runtime staging verification failed")
	}
	if err := os.Rename(staging, target); err != nil {
		if valid, verifyErr := verifyAgentSkillRuntimeBundle(target, bundle); verifyErr != nil {
			return "", verifyErr
		} else if valid {
			return target, nil
		}
		return "", fmt.Errorf("activate skill runtime bundle: %w", err)
	}
	return target, nil
}

func verifyAgentSkillRuntimeBundle(target string, bundle agentSkillRuntimeBundle) (bool, error) {
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, nil
	}
	marker, err := os.ReadFile(filepath.Join(target, agentSkillRuntimeMarker))
	if err != nil || string(marker) != bundle.digest+"\n" {
		return false, nil
	}
	expected := map[string]agentSkillRuntimeFile{}
	for _, file := range bundle.files {
		expected[file.relativePath] = file
	}
	seen := map[string]bool{}
	err = filepath.WalkDir(target, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("skill runtime cache contains a symbolic link")
		}
		if entry.IsDir() {
			return nil
		}
		relative, relativeErr := filepath.Rel(target, path)
		if relativeErr != nil {
			return relativeErr
		}
		relative = filepath.ToSlash(relative)
		if relative == agentSkillRuntimeMarker {
			return nil
		}
		file, ok := expected[relative]
		if !ok || seen[relative] {
			return errors.New("skill runtime cache contains an unexpected file")
		}
		fileInfo, statErr := entry.Info()
		if statErr != nil || !fileInfo.Mode().IsRegular() || fileInfo.Size() != int64(len(file.content)) ||
			(file.executable && fileInfo.Mode().Perm()&0o111 == 0) ||
			(!file.executable && fileInfo.Mode().Perm()&0o111 != 0) {
			return errors.New("skill runtime cache contains an invalid file")
		}
		digest, digestErr := agentSkillRuntimeFileSHA256(path)
		if digestErr != nil || digest != file.digest {
			return errors.New("skill runtime cache failed checksum verification")
		}
		seen[relative] = true
		return nil
	})
	if err != nil {
		return false, nil
	}
	return len(seen) == len(expected), nil
}

func normalizeAgentSkillRuntimeResource(relativePath string, content []byte) ([]byte, bool) {
	relativePath = filepath.ToSlash(strings.TrimSpace(relativePath))
	isScript := strings.HasPrefix(relativePath, "scripts/") && len(content) >= 2 && content[0] == '#' && content[1] == '!'
	if !isScript {
		return content, false
	}
	// Skill bundles are shared across Windows and Linux. A shebang with CRLF
	// asks Linux for an interpreter whose name ends in a carriage return, while
	// read-only materialization prevents a task from repairing it safely. Make
	// the portable script bytes and executable intent part of the content hash.
	return []byte(strings.ReplaceAll(string(content), "\r\n", "\n")), true
}

func agentSkillRuntimeValidSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func agentSkillRuntimeFileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func agentSkillRuntimeDirectoryName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var slug strings.Builder
	for _, character := range name {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' || character == '_' {
			slug.WriteRune(character)
		} else if slug.Len() > 0 && !strings.HasSuffix(slug.String(), "-") {
			slug.WriteByte('-')
		}
		if slug.Len() >= 48 {
			break
		}
	}
	value := strings.Trim(slug.String(), "-_")
	if value == "" {
		value = "skill"
	}
	digest := sha256.Sum256([]byte(name))
	return value + "-" + hex.EncodeToString(digest[:6])
}
