package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"io"
	"os"
	"path/filepath"

	"strings"

	"github.com/google/uuid"
)

func canonicalManagedRegistrationPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !filepath.IsAbs(value) || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("path is invalid")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(value))
	if err != nil || !filepath.IsAbs(resolved) {
		return "", errors.New("path is unavailable")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("path is not a directory")
	}
	return resolved, nil
}

func hostPathContains(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative))
}

func managedEnvironmentGeneration(name, language string, packages []string, specDigest string) string {
	return managedEnvironmentGenerationAtValidation(name, language, packages, specDigest, managedEnvironmentValidationRevision)
}

func managedEnvironmentGenerationAtValidation(name, language string, packages []string, specDigest string, revision int) string {
	hash := sha256.New()
	contract := "synon-managed-environment-v3-final-prefix\x00" + name + "\x00" + language + "\x00"
	if revision > 0 {
		contract = "synon-managed-environment-v4-verified-installation\x00" + name + "\x00" + language + "\x00"
	}
	if specDigest != "" {
		contract += specDigest + "\x00"
	}
	_, _ = io.WriteString(hash, contract)
	for _, item := range packages {
		_, _ = io.WriteString(hash, item+"\x00")
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func managedEnvironmentLegacyGeneration(name, language string, packages []string, specDigest string) string {
	hash := sha256.New()
	contract := "synon-managed-environment-v1\x00" + name + "\x00" + language + "\x00"
	if specDigest != "" {
		contract = "synon-managed-environment-v2\x00" + name + "\x00" + language + "\x00" + specDigest + "\x00"
	}
	_, _ = io.WriteString(hash, contract)
	for _, item := range packages {
		_, _ = io.WriteString(hash, item+"\x00")
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func managedEnvironmentSpecDigest(pipPhases [][]string, importNames []string) string {
	if len(pipPhases) == 0 && len(importNames) == 0 {
		return ""
	}
	raw, err := json.Marshal(struct {
		PipPhases   [][]string `json:"pip_phases,omitempty"`
		ImportNames []string   `json:"import_names,omitempty"`
	}{pipPhases, importNames})
	if err != nil {
		panic("managed environment specification contains an unsupported value")
	}
	digest := sha256.Sum256(append([]byte("synon-managed-environment-spec-v1\x00"), raw...))
	return hex.EncodeToString(digest[:])
}

func managedRegisteredEnvironmentGeneration(name, language, sourcePath, runtimePath string, packages []string) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, "synon-managed-registered-environment-v1\x00"+name+"\x00"+language+"\x00"+sourcePath+"\x00"+runtimePath+"\x00")
	for _, item := range packages {
		_, _ = io.WriteString(hash, item+"\x00")
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func managedEnvironmentOperationKey(kind string, input any) string {
	raw, err := json.Marshal(input)
	if err != nil {
		panic("managed environment operation key contains an unsupported value")
	}
	digest := sha256.Sum256(append([]byte("synon-managed-environment-operation-v1\x00"+kind+"\x00"), raw...))
	return hex.EncodeToString(digest[:])
}

func managedEnvironmentRequestKey(operationID, kind string, input any) string {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return managedEnvironmentOperationKey(kind, input)
	}
	if len([]byte(operationID)) > 512 || strings.ContainsAny(operationID, "\x00\r\n") {
		return managedEnvironmentOperationKey(kind, input)
	}
	return managedEnvironmentOperationKey(kind, struct {
		OperationID string
		Request     any
	}{operationID, input})
}

type recoveredManagedEnvironment struct {
	path        string
	environment ManagedEnvironment
}

type managedEnvironmentHealthCheck struct {
	done chan struct{}
	err  error
}

func (m *Manager) validateManagedEnvironmentGeneration(
	ctx context.Context,
	generation, language, prefix string,
	packages, imports []string,
) error {
	if m == nil || !validSHA256(generation) {
		return errors.New("managed environment generation health identity is invalid")
	}
	key := managedEnvironmentOperationKey("health", struct {
		Generation, Language, Prefix string
		Packages, Imports            []string
	}{generation, language, prefix, packages, imports})
	m.managedEnvironmentHealthMu.Lock()
	if existing := m.managedEnvironmentHealth[key]; existing != nil {
		done := existing.done
		m.managedEnvironmentHealthMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return existing.err
		}
	}
	check := &managedEnvironmentHealthCheck{done: make(chan struct{})}
	m.managedEnvironmentHealth[key] = check
	m.managedEnvironmentHealthMu.Unlock()

	check.err = validateManagedEnvironmentPublication(ctx, language, prefix, packages, imports)
	m.managedEnvironmentHealthMu.Lock()
	close(check.done)
	if check.err != nil {
		delete(m.managedEnvironmentHealth, key)
	}
	m.managedEnvironmentHealthMu.Unlock()
	return check.err
}

// findManagedEnvironmentOperationGeneration closes the process-restart gap
// between a verified generation being published and the owning durable tool
// batch checkpointing its terminal result. The operation key is derived from
// the stable tool-call identity plus the exact request, so recovery activates
// the already verified generation instead of rerunning package hooks.
func (m *Manager) findManagedEnvironmentOperationGeneration(
	ctx context.Context,
	generationRoot, operationKey string,
) (recoveredManagedEnvironment, bool, error) {
	if !validSHA256(operationKey) {
		return recoveredManagedEnvironment{}, false, errors.New("managed environment operation identity is invalid")
	}
	entries, err := os.ReadDir(generationRoot)
	if err != nil {
		return recoveredManagedEnvironment{}, false, errors.New("managed environment generations cannot be inspected")
	}
	if len(entries) > maxManagedEnvironmentGenerations {
		return recoveredManagedEnvironment{}, false, errors.New("managed environment generation history requires maintenance")
	}
	var recovered recoveredManagedEnvironment
	for _, entry := range entries {
		if !entry.IsDir() || !validSHA256(entry.Name()) {
			continue
		}
		path := filepath.Join(generationRoot, entry.Name())
		marker, readErr := readManagedEnvironmentMarker(path)
		if readErr != nil || marker.SchemaVersion != managedEnvironmentMarkerVersion || marker.OperationKey != operationKey {
			continue
		}
		if needsRebuild, err := managedEnvironmentNeedsRebuild(path, marker); err != nil || needsRebuild {
			continue
		}
		candidate := recoveredManagedEnvironment{path: path, environment: ManagedEnvironment{
			Name: marker.Name, Language: marker.Language, Kind: marker.Kind,
			Generation: marker.Generation, SpecDigest: marker.SpecDigest,
			Packages: append([]string(nil), marker.Packages...), Status: "ready",
		}}
		if err := m.validateRecoveredManagedEnvironment(ctx, candidate); err != nil {
			// Preserve the unhealthy immutable generation for diagnosis, but do
			// not let it block a repaired generation with the same durable
			// operation identity. If no healthy candidate remains, the caller
			// reruns the installer/registration transaction.
			continue
		}
		if recovered.path != "" && recovered.path != candidate.path {
			return recoveredManagedEnvironment{}, false, errors.New("managed environment operation conflicts with durable generations")
		}
		recovered = candidate
	}
	return recovered, recovered.path != "", nil
}

func managedEnvironmentMarkerEquivalent(left, right managedEnvironmentMarker) bool {
	return left.ValidationRevision == right.ValidationRevision && left.SchemaVersion == right.SchemaVersion && left.Name == right.Name && left.Language == right.Language &&
		left.Generation == right.Generation && left.Kind == right.Kind && left.SourcePath == right.SourcePath &&
		left.RuntimePath == right.RuntimePath && left.OperationKey == right.OperationKey && left.SpecDigest == right.SpecDigest &&
		strings.Join(left.ImportNames, "\x00") == strings.Join(right.ImportNames, "\x00") &&
		strings.Join(left.Packages, "\x00") == strings.Join(right.Packages, "\x00")
}

// Generation identity is content-addressed and intentionally survives
// deactivation. A later create request with a new durable operation identity
// may therefore converge on the same immutable generation. Reuse is safe only
// when every content and validation field matches; operation metadata is not
// part of the generation payload.
func managedEnvironmentGenerationEquivalent(left, right managedEnvironmentMarker) bool {
	return left.ValidationRevision == right.ValidationRevision && left.SchemaVersion == right.SchemaVersion && left.Name == right.Name && left.Language == right.Language &&
		left.Generation == right.Generation && left.Kind == right.Kind && left.SourcePath == right.SourcePath &&
		left.RuntimePath == right.RuntimePath && left.SpecDigest == right.SpecDigest &&
		strings.Join(left.ImportNames, "\x00") == strings.Join(right.ImportNames, "\x00") &&
		strings.Join(left.Packages, "\x00") == strings.Join(right.Packages, "\x00")
}

func activateManagedEnvironment(root, name, generationPath string) error {
	active := filepath.Join(root, name)
	if info, err := os.Lstat(active); err == nil && info.Mode()&os.ModeSymlink == 0 {
		return errors.New("managed environment active path is not an atomic pointer")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("managed environment active pointer cannot be inspected")
	}
	temporary := active + ".tmp-" + uuid.NewString()
	defer os.Remove(temporary)
	if err := os.Symlink(generationPath, temporary); err != nil {
		return errors.New("managed environment activation pointer cannot be created")
	}
	if err := os.Rename(temporary, active); err != nil {
		return errors.New("managed environment activation failed")
	}
	return syncDirectory(root)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
