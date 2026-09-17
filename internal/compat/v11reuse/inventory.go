package v11reuse

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	ErrInvalidRule             = errors.New("invalid reuse rule")
	ErrPathEscape              = errors.New("reuse path escapes source")
	ErrDuplicateClassification = errors.New("duplicate reuse classification")
	ErrUnclassified            = errors.New("unclassified baseline content")
)

type Decision string

const (
	DirectCopy               Decision = "direct_copy"
	ThinAdapter              Decision = "thin_adapter"
	UpstreamIntegration      Decision = "upstream_integration"
	MechanicalPort           Decision = "mechanical_port"
	BlackBoxReimplementation Decision = "black_box_reimplementation"
	FrontendOwned            Decision = "frontend_owned"
	RuntimeStateExcluded     Decision = "runtime_state_excluded"
)

type Rule struct {
	ID                  string
	SourcePath          string
	ExcludePaths        []string
	Category            string
	Language            string
	Decision            Decision
	TargetPath          string
	RuntimeDependencies []string
	Evidence            []string
	Licenses            []string
	Owner               string
	Risks               []string
	MainRuntimeWired    bool
	ReleaseRequired     bool
}

type Entry struct {
	ID                  string
	SourcePath          string
	Category            string
	Language            string
	Decision            Decision
	TargetPath          string
	RuntimeDependencies []string
	Evidence            []string
	Licenses            []string
	Owner               string
	Risks               []string
	MainRuntimeWired    bool
	ReleaseRequired     bool
	FileCount           int
	SymlinkCount        int
	TotalBytes          int64
	SHA256              string
}

type Summary struct {
	Entries         int
	Files           int
	Symlinks        int
	TotalBytes      int64
	ReleaseEntries  int
	WiredEntries    int
	ExcludedEntries int
	ByDecision      map[string]int
	ByCategory      map[string]int
}

type Manifest struct {
	SchemaVersion    int
	BaselineName     string
	SourceCommit     string
	SourceTreeSHA256 string
	Summary          Summary
	Entries          []Entry
}

var ruleIDPattern = regexp.MustCompile("^[a-z0-9][a-z0-9._-]*$")

func Build(source string, rules []Rule) (Manifest, error) {
	root, err := canonicalRoot(source)
	if err != nil {
		return Manifest{}, err
	}
	if len(rules) == 0 {
		return Manifest{}, fmt.Errorf("%w: no rules", ErrInvalidRule)
	}

	objects, err := walkObjects(root)
	if err != nil {
		return Manifest{}, err
	}

	owners := make(map[string]string, len(objects))
	entries := make([]Entry, 0, len(rules))
	seenIDs := make(map[string]struct{}, len(rules))

	for _, originalRule := range rules {
		rule, err := normalizeAndValidateRule(root, originalRule)
		if err != nil {
			return Manifest{}, err
		}
		if _, exists := seenIDs[rule.ID]; exists {
			return Manifest{}, fmt.Errorf("%w: repeated rule id %q", ErrInvalidRule, rule.ID)
		}
		seenIDs[rule.ID] = struct{}{}

		matched := make([]sourceObject, 0)
		duplicatePaths := make([]string, 0)
		for _, object := range objects {
			if !pathOwns(rule.SourcePath, object.Path) || pathExcluded(rule.ExcludePaths, object.Path) {
				continue
			}
			if owner, exists := owners[object.Path]; exists {
				duplicatePaths = append(
					duplicatePaths,
					fmt.Sprintf("%s (%s, %s)", object.Path, owner, rule.ID),
				)
				continue
			}
			owners[object.Path] = rule.ID
			matched = append(matched, object)
		}
		if len(duplicatePaths) > 0 {
			return Manifest{}, fmt.Errorf(
				"%w: %s",
				ErrDuplicateClassification,
				strings.Join(duplicatePaths, ", "),
			)
		}

		if len(matched) == 0 {
			return Manifest{}, fmt.Errorf(
				"%w: rule %q source %q contains no files or symlinks",
				ErrInvalidRule,
				rule.ID,
				rule.SourcePath,
			)
		}
		entries = append(entries, entryFromRule(rule, matched))
	}

	var unclassified []string
	for _, object := range objects {
		if _, exists := owners[object.Path]; !exists {
			unclassified = append(unclassified, object.Path)
		}
	}
	if len(unclassified) > 0 {
		return Manifest{}, fmt.Errorf(
			"%w: %s",
			ErrUnclassified,
			strings.Join(unclassified, ", "),
		)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].ID < entries[j].ID
	})

	manifest := Manifest{
		SchemaVersion: 1,
		BaselineName:  "synonbiomed-v1.1",
		Entries:       entries,
	}
	manifest.Summary = summarize(entries)
	manifest.SourceTreeSHA256 = hashManifestEntries(entries)
	return manifest, nil
}

func Marshal(manifest Manifest) ([]byte, error) {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal v1.1 reuse manifest: %w", err)
	}
	return append(data, '\n'), nil
}

func canonicalRoot(source string) (string, error) {
	if strings.TrimSpace(source) == "" {
		return "", fmt.Errorf("%w: source is empty", ErrPathEscape)
	}
	absolute, err := filepath.Abs(source)
	if err != nil {
		return "", fmt.Errorf("resolve source path: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("stat source path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("source path is not a directory: %s", absolute)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve source symlinks: %w", err)
	}
	return filepath.Clean(resolved), nil
}

func normalizeAndValidateRule(root string, rule Rule) (Rule, error) {
	rule.ID = strings.TrimSpace(rule.ID)
	rule.Category = strings.TrimSpace(rule.Category)
	rule.Language = strings.TrimSpace(rule.Language)
	rule.TargetPath = normalizeOptionalPath(rule.TargetPath)
	rule.Owner = strings.TrimSpace(rule.Owner)

	if !ruleIDPattern.MatchString(rule.ID) {
		return Rule{}, fmt.Errorf("%w: invalid id %q", ErrInvalidRule, rule.ID)
	}
	if !validDecision(rule.Decision) {
		return Rule{}, fmt.Errorf("%w: rule %q has decision %q", ErrInvalidRule, rule.ID, rule.Decision)
	}
	if rule.Category == "" || rule.Language == "" || rule.Owner == "" {
		return Rule{}, fmt.Errorf("%w: rule %q is missing category, language, or owner", ErrInvalidRule, rule.ID)
	}
	if len(nonEmptyStrings(rule.Licenses)) == 0 {
		return Rule{}, fmt.Errorf("%w: rule %q has no license disposition", ErrInvalidRule, rule.ID)
	}
	if len(nonEmptyStrings(rule.Evidence)) == 0 {
		return Rule{}, fmt.Errorf("%w: rule %q has no evidence", ErrInvalidRule, rule.ID)
	}
	if rule.ReleaseRequired && rule.TargetPath == "" {
		return Rule{}, fmt.Errorf("%w: release rule %q has no target", ErrInvalidRule, rule.ID)
	}

	sourcePath, err := normalizeRelativePath(rule.SourcePath)
	if err != nil {
		return Rule{}, fmt.Errorf("%w: rule %q source %q: %v", ErrPathEscape, rule.ID, rule.SourcePath, err)
	}
	sourceAbsolute := filepath.Join(root, filepath.FromSlash(sourcePath))
	if !withinRoot(root, sourceAbsolute) {
		return Rule{}, fmt.Errorf("%w: rule %q source %q", ErrPathEscape, rule.ID, sourcePath)
	}
	if _, err := os.Lstat(sourceAbsolute); err != nil {
		return Rule{}, fmt.Errorf("%w: rule %q source %q: %v", ErrInvalidRule, rule.ID, sourcePath, err)
	}
	rule.SourcePath = sourcePath

	excludes := make([]string, 0, len(rule.ExcludePaths))
	for _, excluded := range rule.ExcludePaths {
		clean, err := normalizeRelativePath(excluded)
		if err != nil {
			return Rule{}, fmt.Errorf("%w: rule %q exclusion %q: %v", ErrPathEscape, rule.ID, excluded, err)
		}
		if !pathOwns(sourcePath, clean) {
			return Rule{}, fmt.Errorf(
				"%w: rule %q exclusion %q is outside source %q",
				ErrInvalidRule,
				rule.ID,
				clean,
				sourcePath,
			)
		}
		excludes = append(excludes, clean)
	}
	sort.Strings(excludes)
	rule.ExcludePaths = compactStrings(excludes)
	rule.RuntimeDependencies = sortedStrings(rule.RuntimeDependencies)
	rule.Evidence = sortedStrings(rule.Evidence)
	rule.Licenses = sortedStrings(rule.Licenses)
	rule.Risks = sortedStrings(rule.Risks)
	return rule, nil
}

func normalizeRelativePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || path == "." || filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return "", errors.New("path must be a non-root relative path")
	}
	if strings.ContainsRune(path, '\\') {
		return "", errors.New("path must use slash separators")
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("parent traversal is not allowed")
	}
	return clean, nil
}

func normalizeOptionalPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
}

func validDecision(decision Decision) bool {
	switch decision {
	case DirectCopy, ThinAdapter, UpstreamIntegration, MechanicalPort,
		BlackBoxReimplementation, FrontendOwned, RuntimeStateExcluded:
		return true
	default:
		return false
	}
}

func pathOwns(parent string, child string) bool {
	return child == parent || strings.HasPrefix(child, parent+"/")
}

func pathExcluded(excludes []string, path string) bool {
	for _, excluded := range excludes {
		if pathOwns(excluded, path) {
			return true
		}
	}
	return false
}

func withinRoot(root string, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func entryFromRule(rule Rule, objects []sourceObject) Entry {
	entry := Entry{
		ID:                  rule.ID,
		SourcePath:          rule.SourcePath,
		Category:            rule.Category,
		Language:            rule.Language,
		Decision:            rule.Decision,
		TargetPath:          rule.TargetPath,
		RuntimeDependencies: append([]string{}, rule.RuntimeDependencies...),
		Evidence:            append([]string{}, rule.Evidence...),
		Licenses:            append([]string{}, rule.Licenses...),
		Owner:               rule.Owner,
		Risks:               append([]string{}, rule.Risks...),
		MainRuntimeWired:    rule.MainRuntimeWired,
		ReleaseRequired:     rule.ReleaseRequired,
		SHA256:              hashSourceObjects(objects),
	}
	for _, object := range objects {
		switch object.Kind {
		case objectFile:
			entry.FileCount++
			entry.TotalBytes += object.Size
		case objectSymlink:
			entry.SymlinkCount++
		}
	}
	return entry
}

func summarize(entries []Entry) Summary {
	summary := Summary{
		Entries:    len(entries),
		ByDecision: make(map[string]int),
		ByCategory: make(map[string]int),
	}
	for _, entry := range entries {
		summary.Files += entry.FileCount
		summary.Symlinks += entry.SymlinkCount
		summary.TotalBytes += entry.TotalBytes
		if entry.ReleaseRequired {
			summary.ReleaseEntries++
		}
		if entry.MainRuntimeWired {
			summary.WiredEntries++
		}
		if entry.Decision == RuntimeStateExcluded {
			summary.ExcludedEntries++
		}
		summary.ByDecision[string(entry.Decision)]++
		summary.ByCategory[entry.Category]++
	}
	return summary
}

func hashManifestEntries(entries []Entry) string {
	hashValue := sha256.New()
	for _, entry := range entries {
		writeHashField(hashValue, entry.ID)
		writeHashField(hashValue, entry.SourcePath)
		writeHashField(hashValue, entry.SHA256)
	}
	return hex.EncodeToString(hashValue.Sum(nil))
}

func nonEmptyStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, strings.TrimSpace(value))
		}
	}
	return result
}

func sortedStrings(values []string) []string {
	result := nonEmptyStrings(values)
	sort.Strings(result)
	return compactStrings(result)
}

func compactStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}
