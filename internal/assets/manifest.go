// Package assets verifies optional, non-Go runtime assets before use.
package assets

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Manifest struct {
	SchemaVersion int      `json:"schemaVersion"`
	Source        string   `json:"source,omitempty"`
	Version       string   `json:"version,omitempty"`
	SourceURL     string   `json:"sourceUrl,omitempty"`
	License       string   `json:"license,omitempty"`
	LicenseFile   string   `json:"licenseFile,omitempty"`
	Entrypoint    string   `json:"entrypoint,omitempty"`
	Runtime       Runtime  `json:"runtime,omitempty"`
	Excluded      []string `json:"excluded,omitempty"`
	Skills        []string `json:"skills,omitempty"`
	Agents        []string `json:"agents,omitempty"`
	Files         []File   `json:"files"`
}

// Runtime declares the interpreter boundary for an optional asset pack.
// Go remains the only main process; these packs are launched only on demand.
type Runtime struct {
	Kind           string `json:"kind,omitempty"`
	MinimumVersion string `json:"minimumVersion,omitempty"`
	Optional       bool   `json:"optional,omitempty"`
}

type File struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type Report struct {
	Checked    int   `json:"checked"`
	TotalBytes int64 `json:"totalBytes"`
}

func Load(path string) (Manifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("open asset manifest: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode asset manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return Manifest{}, errors.New("asset manifest must contain one JSON value")
		}
		return Manifest{}, fmt.Errorf("read asset manifest: %w", err)
	}
	return manifest, nil
}

// Verify confirms that every file in manifest is an exact, contained asset.
func Verify(root string, manifest Manifest) (Report, error) {
	if manifest.SchemaVersion != 1 {
		return Report{}, fmt.Errorf("unsupported asset manifest schema version %d", manifest.SchemaVersion)
	}
	if strings.TrimSpace(root) == "" {
		return Report{}, errors.New("asset root is required")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return Report{}, fmt.Errorf("resolve asset root: %w", err)
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return Report{}, fmt.Errorf("resolve asset root symlinks: %w", err)
	}
	report := Report{}
	if manifest.LicenseFile != "" {
		licensePath := filepath.ToSlash(filepath.Clean(filepath.FromSlash(manifest.LicenseFile)))
		if licensePath == "." || filepath.IsAbs(filepath.FromSlash(licensePath)) || strings.HasPrefix(licensePath, "../") {
			return Report{}, errors.New("asset license file path must be relative and contained")
		}
		declared := false
		for _, entry := range manifest.Files {
			if filepath.ToSlash(filepath.Clean(filepath.FromSlash(entry.Path))) == licensePath {
				declared = true
				break
			}
		}
		if !declared {
			return Report{}, fmt.Errorf("asset license file %q is not checksum-declared", manifest.LicenseFile)
		}
	}
	for _, entry := range manifest.Files {
		if err := verifyFile(rootAbs, rootResolved, entry); err != nil {
			return Report{}, err
		}
		report.Checked++
		report.TotalBytes += entry.Bytes
	}
	return report, nil
}

func verifyFile(rootAbs, rootResolved string, entry File) error {
	if !validRelativeAssetPath(entry.Path) {
		return fmt.Errorf("invalid asset path %q", entry.Path)
	}
	if entry.Bytes < 0 {
		return fmt.Errorf("invalid negative asset size for %q", entry.Path)
	}
	if len(entry.SHA256) != sha256.Size*2 {
		return fmt.Errorf("invalid sha256 for %q", entry.Path)
	}
	if _, err := hex.DecodeString(entry.SHA256); err != nil {
		return fmt.Errorf("invalid sha256 for %q: %w", entry.Path, err)
	}
	path := filepath.Join(rootAbs, filepath.FromSlash(entry.Path))
	if !isWithin(rootAbs, path) {
		return fmt.Errorf("asset path escapes root: %q", entry.Path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve asset %q: %w", entry.Path, err)
	}
	if !isWithin(rootResolved, resolved) {
		return fmt.Errorf("asset symlink escapes root: %q", entry.Path)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("stat asset %q: %w", entry.Path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("asset %q is not a regular file", entry.Path)
	}
	if info.Size() != entry.Bytes {
		return fmt.Errorf("asset size mismatch for %q: got %d, want %d", entry.Path, info.Size(), entry.Bytes)
	}
	contents, err := os.ReadFile(resolved)
	if err != nil {
		return fmt.Errorf("read asset %q: %w", entry.Path, err)
	}
	digest := sha256.Sum256(contents)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), entry.SHA256) {
		return fmt.Errorf("asset sha256 mismatch for %q", entry.Path)
	}
	return nil
}

func validRelativeAssetPath(value string) bool {
	if value == "" || filepath.IsAbs(value) {
		return false
	}
	clean := filepath.Clean(filepath.FromSlash(value))
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func isWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
