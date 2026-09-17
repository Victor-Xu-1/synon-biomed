package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

const (
	manifestSchemaVersion = 1
	manifestSource        = "synonbiomed-v1.1/runtime/assets/skills"
	skillsRelativePath    = "skills/synonbiomed"
	manifestRelativePath  = "assets/synonbiomed/skills.manifest.json"
)

type fileRecord struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type skillsManifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	Source        string       `json:"source"`
	Skills        []string     `json:"skills"`
	Files         []fileRecord `json:"files"`
}

var unsafeSkillPatterns = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{name: "world-writable cache permissions", pattern: regexp.MustCompile(`(?m)^\s*chmod\s+777(?:\s|$)`)},
	{name: "unbounded readiness loop", pattern: regexp.MustCompile(`(?s)until\s+curl[^\n]*health/ready[^\n]*;\s*do\s+sleep[^\n]*;\s*done`)},
	{name: "remote shell installer", pattern: regexp.MustCompile(`(?m)^\s*(?:curl|wget)[^\n|]*\|\s*(?:ba)?sh\b`)},
	{name: "remote recursive deletion", pattern: regexp.MustCompile(`(?m)^\s*ssh[^\n]*(?:rm\s+-rf|rm\s+-r)\b`)},
	{name: "retired software runtime path", pattern: regexp.MustCompile(`\bsoftware_runtime\b`)},
	{name: "retired RCSB search shortcut", pattern: regexp.MustCompile(`\bsearch_rcsb_structures\b`)},
	{name: "retired RCSB download shortcut", pattern: regexp.MustCompile(`\bdownload_rcsb_file\b`)},
	{name: "retired binding-mode shortcut", pattern: regexp.MustCompile(`\bbinding_mode_analysis\b`)},
	{name: "retired runtime doctor shortcut", pattern: regexp.MustCompile(`\bAgentRuntimeDoctor\b`)},
}

var (
	inlineHTTPRequestPattern = regexp.MustCompile(`(?m)^\s*[^#\r\n]*requests\.(?:get|post)\([^\)\r\n]*\)`)
	httpTimeoutPattern       = regexp.MustCompile(`\btimeout\s*=`)
	skillEntrypointName      = regexp.MustCompile(`(?m)^name:\s*["']?([A-Za-z0-9_.-]+)["']?\s*$`)
)

var legacySkillToolNames = map[string]struct{}{
	"Bash": {}, "Read": {}, "Write": {}, "Edit": {},
	"WebSearch": {}, "WebFetch": {}, "WebResearch": {},
	"Skill": {}, "SkillSearch": {}, "ToolSearch": {},
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "skills-manifest:", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	flags := flag.NewFlagSet("skills-manifest", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var root string
	var check, write bool
	flags.StringVar(&root, "root", ".", "repository root")
	flags.BoolVar(&check, "check", false, "verify the existing manifest")
	flags.BoolVar(&write, "write", false, "write deterministic manifest JSON")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("positional arguments are not supported")
	}
	if check == write {
		return errors.New("exactly one of --check or --write is required")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	expected, err := buildManifest(filepath.Join(root, filepath.FromSlash(skillsRelativePath)))
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(root, filepath.FromSlash(manifestRelativePath))
	if check {
		return checkManifest(manifestPath, expected)
	}
	return writeManifest(manifestPath, expected)
}

func buildManifest(skillsRoot string) (skillsManifest, error) {
	rootInfo, err := os.Lstat(skillsRoot)
	if err != nil {
		return skillsManifest{}, fmt.Errorf("inspect skills root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return skillsManifest{}, errors.New("skills root must be a real directory")
	}

	manifest := skillsManifest{SchemaVersion: manifestSchemaVersion, Source: manifestSource}
	entries, err := os.ReadDir(skillsRoot)
	if err != nil {
		return skillsManifest{}, fmt.Errorf("read skills root: %w", err)
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return skillsManifest{}, fmt.Errorf("symlink is not allowed: %s", entry.Name())
		}
		if !entry.IsDir() {
			continue
		}
		skillPath := filepath.Join(skillsRoot, entry.Name(), "SKILL.md")
		info, err := os.Lstat(skillPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return skillsManifest{}, fmt.Errorf("inspect %s: %w", filepath.ToSlash(skillPath), err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return skillsManifest{}, fmt.Errorf("skill entrypoint must be a regular file: %s/SKILL.md", entry.Name())
		}
		content, err := os.ReadFile(skillPath)
		if err != nil {
			return skillsManifest{}, fmt.Errorf("read %s/SKILL.md: %w", entry.Name(), err)
		}
		match := skillEntrypointName.FindSubmatch(content)
		if len(match) != 2 || string(match[1]) != entry.Name() {
			declared := "(missing)"
			if len(match) == 2 {
				declared = string(match[1])
			}
			return skillsManifest{}, fmt.Errorf(
				"skill directory and frontmatter name must match exactly: directory=%q name=%q",
				entry.Name(), declared,
			)
		}
		manifest.Skills = append(manifest.Skills, entry.Name())
	}
	sort.Slice(manifest.Skills, func(left, right int) bool {
		return manifestPathLess(manifest.Skills[left], manifest.Skills[right])
	})

	err = filepath.WalkDir(skillsRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == skillsRoot {
			return nil
		}
		relative, err := filepath.Rel(skillsRoot, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink is not allowed: %s", relative)
		}
		if entry.IsDir() {
			if entry.Name() == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(strings.ToLower(entry.Name()), ".pyc") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect %s: %w", relative, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular file is not allowed: %s", relative)
		}
		if filepath.Base(relative) == "SKILL.md" {
			content, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read %s for safety validation: %w", relative, err)
			}
			if err := validateSkillContent(relative, content); err != nil {
				return err
			}
		}
		record, err := hashFile(path, relative)
		if err != nil {
			return err
		}
		manifest.Files = append(manifest.Files, record)
		return nil
	})
	if err != nil {
		return skillsManifest{}, fmt.Errorf("enumerate skills tree: %w", err)
	}
	sort.Slice(manifest.Files, func(left, right int) bool {
		return manifestPathLess(manifest.Files[left].Path, manifest.Files[right].Path)
	})
	if err := validateManifest(manifest, true); err != nil {
		return skillsManifest{}, fmt.Errorf("generated manifest is invalid: %w", err)
	}
	return manifest, nil
}

func validateSkillContent(relative string, content []byte) error {
	if err := validateSkillFrontmatterToolNames(relative, content); err != nil {
		return err
	}
	for _, unsafe := range unsafeSkillPatterns {
		if unsafe.pattern.Match(content) {
			return fmt.Errorf("unsafe skill content in %s: %s", relative, unsafe.name)
		}
	}
	for _, request := range inlineHTTPRequestPattern.FindAll(content, -1) {
		if !httpTimeoutPattern.Match(request) {
			return fmt.Errorf("unsafe skill content in %s: unbounded HTTP request", relative)
		}
	}
	return nil
}

func validateSkillFrontmatterToolNames(relative string, content []byte) error {
	lines := strings.Split(string(content), "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return nil
	}
	inToolList := false
	check := func(raw string) error {
		for _, token := range strings.FieldsFunc(raw, func(value rune) bool {
			return value == ',' || value == '[' || value == ']' || value == '\'' || value == '"' || unicode.IsSpace(value)
		}) {
			if _, legacy := legacySkillToolNames[token]; legacy {
				return fmt.Errorf("legacy tool identity %q in %s frontmatter", token, relative)
			}
		}
		return nil
	}
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			return nil
		}
		if strings.HasPrefix(trimmed, "allowed-tools:") || strings.HasPrefix(trimmed, "tools:") {
			inToolList = true
			if err := check(strings.TrimSpace(strings.SplitN(trimmed, ":", 2)[1])); err != nil {
				return err
			}
			continue
		}
		if !inToolList {
			continue
		}
		if strings.HasPrefix(trimmed, "-") {
			if err := check(strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))); err != nil {
				return err
			}
			continue
		}
		if trimmed != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			inToolList = false
		}
	}
	return nil
}

func hashFile(path, relative string) (fileRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		return fileRecord{}, fmt.Errorf("open %s: %w", relative, err)
	}
	defer file.Close()
	hash := sha256.New()
	count, err := io.Copy(hash, file)
	if err != nil {
		return fileRecord{}, fmt.Errorf("hash %s: %w", relative, err)
	}
	return fileRecord{Path: relative, SHA256: hex.EncodeToString(hash.Sum(nil)), Bytes: count}, nil
}

func manifestPathLess(left, right string) bool {
	leftHead, leftTail := manifestSortParts(left)
	rightHead, rightTail := manifestSortParts(right)
	if leftHead != rightHead {
		return leftHead < rightHead
	}
	if leftTail != rightTail {
		return leftTail < rightTail
	}
	if left == right {
		return false
	}
	if strings.EqualFold(left, right) {
		return left < right
	}
	return strings.ToLower(left) < strings.ToLower(right)
}

func manifestSortParts(value string) (string, string) {
	head, tail, found := strings.Cut(filepath.ToSlash(value), "/")
	if !found {
		return manifestSortKey(head), ""
	}
	return manifestSortKey(head), strings.ToLower(tail)
}

func manifestSortKey(value string) string {
	var result strings.Builder
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsNumber(character) {
			result.WriteRune(unicode.ToLower(character))
		}
	}
	return result.String()
}

func readManifest(path string) (skillsManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return skillsManifest{}, fmt.Errorf("read manifest: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var manifest skillsManifest
	if err := decoder.Decode(&manifest); err != nil {
		return skillsManifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return skillsManifest{}, errors.New("decode manifest: trailing JSON value")
		}
		return skillsManifest{}, fmt.Errorf("decode manifest trailer: %w", err)
	}
	if err := validateManifest(manifest, false); err != nil {
		return skillsManifest{}, err
	}
	return manifest, nil
}

func validateManifest(manifest skillsManifest, requireSorted bool) error {
	if manifest.SchemaVersion != manifestSchemaVersion {
		return fmt.Errorf("schemaVersion must be %d", manifestSchemaVersion)
	}
	if manifest.Source != manifestSource {
		return fmt.Errorf("source must be %q", manifestSource)
	}
	seenSkills := make(map[string]struct{}, len(manifest.Skills))
	for index, skill := range manifest.Skills {
		if skill == "" || skill != strings.TrimSpace(skill) || strings.ContainsAny(skill, `/\\`) {
			return fmt.Errorf("invalid skill name at index %d", index)
		}
		if _, found := seenSkills[skill]; found {
			return fmt.Errorf("duplicate skill %q", skill)
		}
		seenSkills[skill] = struct{}{}
		if requireSorted && index > 0 && !manifestPathLess(manifest.Skills[index-1], skill) {
			return fmt.Errorf("skills must be strictly sorted: %q before %q", manifest.Skills[index-1], skill)
		}
	}
	seenFiles := make(map[string]struct{}, len(manifest.Files))
	entrypointSkills := make(map[string]struct{}, len(manifest.Skills))
	for index, file := range manifest.Files {
		if file.Path == "" || file.Path != filepath.ToSlash(file.Path) || strings.HasPrefix(file.Path, "/") ||
			file.Path != strings.TrimPrefix(filepath.ToSlash(filepath.Clean(file.Path)), "./") || strings.HasPrefix(file.Path, "../") {
			return fmt.Errorf("invalid file path at index %d", index)
		}
		if _, found := seenFiles[file.Path]; found {
			return fmt.Errorf("duplicate file path %q", file.Path)
		}
		seenFiles[file.Path] = struct{}{}
		if requireSorted && index > 0 && !manifestPathLess(manifest.Files[index-1].Path, file.Path) {
			return fmt.Errorf(
				"files must be strictly sorted by slash path: %q before %q",
				manifest.Files[index-1].Path, file.Path,
			)
		}
		digest, err := hex.DecodeString(file.SHA256)
		if err != nil || len(digest) != sha256.Size || file.SHA256 != strings.ToLower(file.SHA256) {
			return fmt.Errorf("invalid sha256 for %q", file.Path)
		}
		if file.Bytes < 0 {
			return fmt.Errorf("negative byte count for %q", file.Path)
		}
		parts := strings.Split(file.Path, "/")
		if len(parts) == 2 && parts[1] == "SKILL.md" {
			entrypointSkills[parts[0]] = struct{}{}
		}
	}
	if !reflect.DeepEqual(seenSkills, entrypointSkills) {
		return errors.New("skills must exactly match first-level directories containing SKILL.md")
	}
	return nil
}

func checkManifest(path string, expected skillsManifest) error {
	actual, err := readManifest(path)
	if err != nil {
		return err
	}
	canonicalizeManifest(&actual)
	if !reflect.DeepEqual(actual, expected) {
		return errors.New("manifest does not match the complete regular skills tree; run with --write after review")
	}
	return nil
}

func writeManifest(path string, manifest skillsManifest) error {
	if err := validateManifest(manifest, true); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}

func canonicalizeManifest(manifest *skillsManifest) {
	if manifest == nil {
		return
	}
	sort.Slice(manifest.Skills, func(left, right int) bool {
		return manifestPathLess(manifest.Skills[left], manifest.Skills[right])
	})
	sort.Slice(manifest.Files, func(left, right int) bool {
		return manifestPathLess(manifest.Files[left].Path, manifest.Files[right].Path)
	})
}
