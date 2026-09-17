package releasepkg

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"synon-go/internal/buildinfo"
)

const ManifestFile = "RELEASE_MANIFEST.json"

var (
	releaseIdentity = buildinfo.Release()
	releaseName     = releaseIdentity.MachineSlug
	releaseVersion  = releaseIdentity.Version
)

type Options struct {
	GeneratedAt        time.Time
	GOOS               string
	GOARCH             string
	RequireSupplyChain bool
}

type RuntimeRequirements struct {
	Bun    bool `json:"bun"`
	Node   bool `json:"node"`
	Go     bool `json:"go"`
	Python bool `json:"python"`
}

type Coverage struct {
	Scope                         string `json:"scope"`
	Baseline                      string `json:"baseline"`
	Authority                     string `json:"authority"`
	CompatibilityBaselineEligible bool   `json:"compatibilityBaselineEligible"`
	ServiceContracts              int    `json:"serviceContracts"`
	ServiceImplemented            int    `json:"serviceImplemented"`
	ServiceNotApplicable          int    `json:"serviceNotApplicable"`
	HTTPRoutes                    int    `json:"httpRoutes"`
	HTTPRoutesImplemented         int    `json:"httpRoutesImplemented"`
	RealtimeEvents                int    `json:"realtimeEvents"`
	RealtimeEventsImplemented     int    `json:"realtimeEventsImplemented"`
	RealtimeQueries               int    `json:"realtimeQueries"`
	RealtimeQueriesImplemented    int    `json:"realtimeQueriesImplemented"`
	InScope                       int    `json:"inScope"`
	Implemented                   int    `json:"implemented"`
	Missing                       int    `json:"missing"`
}

type FileEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Mode   string `json:"mode"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	SchemaVersion         int                 `json:"schemaVersion"`
	Name                  string              `json:"name"`
	Version               string              `json:"version"`
	GOOS                  string              `json:"goos,omitempty"`
	GOARCH                string              `json:"goarch,omitempty"`
	GeneratedAt           time.Time           `json:"generatedAt"`
	Coverage              *Coverage           `json:"coverage,omitempty"`
	CoreRuntimeRequires   RuntimeRequirements `json:"coreRuntimeRequires"`
	OptionalAssetRuntimes []string            `json:"optionalAssetRuntimes"`
	Integrity             string              `json:"integrity"`
	FileCount             int                 `json:"fileCount"`
	TotalBytes            int64               `json:"totalBytes"`
	Files                 []FileEntry         `json:"files"`
}

type VerificationReport struct {
	Valid         bool   `json:"valid"`
	Version       string `json:"version"`
	VerifiedFiles int    `json:"verifiedFiles"`
	VerifiedBytes int64  `json:"verifiedBytes"`
}

func Generate(root string, options Options) (Manifest, error) {
	root, err := validatedRoot(root)
	if err != nil {
		return Manifest{}, err
	}
	if options.RequireSupplyChain {
		if _, err := VerifySupplyChainForRelease(root); err != nil {
			return Manifest{}, fmt.Errorf("verify release supply chain: %w", err)
		}
	}
	files, totalBytes, err := inventory(root)
	if err != nil {
		return Manifest{}, err
	}
	generatedAt := options.GeneratedAt.UTC()
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}
	manifest := Manifest{
		SchemaVersion:       4,
		Name:                releaseName,
		Version:             releaseVersion,
		GOOS:                strings.TrimSpace(options.GOOS),
		GOARCH:              strings.TrimSpace(options.GOARCH),
		GeneratedAt:         generatedAt,
		CoreRuntimeRequires: RuntimeRequirements{},
		OptionalAssetRuntimes: []string{
			"python: optional kernel and MCP sidecars only",
		},
		Integrity:  "sha256",
		FileCount:  len(files),
		TotalBytes: totalBytes,
		Files:      files,
	}
	if err := writeManifest(root, manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func Verify(root string) (VerificationReport, error) {
	root, err := validatedRoot(root)
	if err != nil {
		return VerificationReport{}, err
	}
	manifest, err := readManifest(root)
	if err != nil {
		return VerificationReport{}, err
	}
	if err := validateManifestContract(manifest); err != nil {
		return VerificationReport{}, err
	}
	actual, totalBytes, err := inventory(root)
	if err != nil {
		return VerificationReport{}, err
	}
	expected := make(map[string]FileEntry, len(manifest.Files))
	for _, file := range manifest.Files {
		if !validRelativePath(file.Path) {
			return VerificationReport{}, fmt.Errorf("release manifest contains invalid path %q", file.Path)
		}
		if _, exists := expected[file.Path]; exists {
			return VerificationReport{}, fmt.Errorf("release manifest contains duplicate path %q", file.Path)
		}
		expected[file.Path] = file
	}
	for _, file := range actual {
		want, exists := expected[file.Path]
		if !exists {
			return VerificationReport{}, fmt.Errorf("unexpected file in release package: %s", file.Path)
		}
		if file.Size != want.Size {
			return VerificationReport{}, fmt.Errorf("size mismatch for %s: got %d want %d", file.Path, file.Size, want.Size)
		}
		if manifest.GOOS != "windows" && file.Mode != want.Mode {
			return VerificationReport{}, fmt.Errorf("mode mismatch for %s: got %s want %s", file.Path, file.Mode, want.Mode)
		}
		if file.SHA256 != want.SHA256 {
			return VerificationReport{}, fmt.Errorf("checksum mismatch for %s", file.Path)
		}
		delete(expected, file.Path)
	}
	if len(expected) > 0 {
		missing := make([]string, 0, len(expected))
		for path := range expected {
			missing = append(missing, path)
		}
		sort.Strings(missing)
		return VerificationReport{}, fmt.Errorf("release package is missing file: %s", missing[0])
	}
	if len(actual) != manifest.FileCount || totalBytes != manifest.TotalBytes {
		return VerificationReport{}, fmt.Errorf("release inventory totals do not match manifest")
	}
	return VerificationReport{
		Valid:         true,
		Version:       manifest.Version,
		VerifiedFiles: len(actual),
		VerifiedBytes: totalBytes,
	}, nil
}

func validatedRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", errors.New("release package root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve release package root: %w", err)
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return "", fmt.Errorf("inspect release package root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("release package root cannot be a symbolic link")
	}
	if !info.IsDir() {
		return "", errors.New("release package root must be a directory")
	}
	return filepath.Clean(abs), nil
}

func inventory(root string) ([]FileEntry, int64, error) {
	files := []FileEntry{}
	var totalBytes int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == ManifestFile {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("release package contains symbolic link: %s", relative)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("release package contains non-regular file: %s", relative)
		}
		digest, err := hashFile(path)
		if err != nil {
			return err
		}
		files = append(files, FileEntry{
			Path:   relative,
			Size:   info.Size(),
			Mode:   fmt.Sprintf("%04o", info.Mode().Perm()),
			SHA256: digest,
		})
		totalBytes += info.Size()
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, totalBytes, nil
}

func hashFile(path string) (string, error) {
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

func writeManifest(root string, manifest Manifest) error {
	content, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode release manifest: %w", err)
	}
	content = append(content, '\n')
	temporary, err := os.CreateTemp(root, ".release-manifest-*.tmp")
	if err != nil {
		return fmt.Errorf("create release manifest: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, filepath.Join(root, ManifestFile)); err != nil {
		return fmt.Errorf("commit release manifest: %w", err)
	}
	return nil
}

func readManifest(root string) (Manifest, error) {
	file, err := os.Open(filepath.Join(root, ManifestFile))
	if err != nil {
		return Manifest{}, fmt.Errorf("open release manifest: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 16*1024*1024))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode release manifest: %w", err)
	}
	return manifest, nil
}

func validateManifestContract(manifest Manifest) error {
	if (manifest.SchemaVersion != 3 && manifest.SchemaVersion != 4) || manifest.Name != releaseName || manifest.Version != releaseVersion {
		return errors.New("release manifest identity is not supported")
	}
	if manifest.Integrity != "sha256" {
		return errors.New("release manifest integrity algorithm must be sha256")
	}
	if manifest.SchemaVersion == 3 {
		if err := validateLegacyCoverage(manifest.Coverage); err != nil {
			return err
		}
	} else if manifest.Coverage != nil {
		return errors.New("current release manifest must not contain historical coverage")
	}
	if manifest.CoreRuntimeRequires.Bun || manifest.CoreRuntimeRequires.Node || manifest.CoreRuntimeRequires.Go || manifest.CoreRuntimeRequires.Python {
		return errors.New("release manifest declares a forbidden core runtime dependency")
	}
	if manifest.FileCount != len(manifest.Files) || manifest.FileCount < 1 || manifest.TotalBytes < 1 {
		return errors.New("release manifest inventory totals are invalid")
	}
	return nil
}

func validRelativePath(path string) bool {
	if path == "" || path == "." || strings.Contains(path, "\\") || filepath.IsAbs(path) {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	return clean == path && clean != ".." && !strings.HasPrefix(clean, "../") && clean != ManifestFile
}
