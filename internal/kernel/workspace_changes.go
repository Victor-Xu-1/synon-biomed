package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxKernelTrackedFiles = 10000
const maxKernelTrackedFileBytes int64 = 16 << 20
const maxKernelWorkspaceScanDuration = time.Second

type FileWrite struct {
	Path          string `json:"path"`
	SHA256        string `json:"sha256,omitempty"`
	Oversize      bool   `json:"oversize,omitempty"`
	Dropped       bool   `json:"dropped,omitempty"` // legacy decode compatibility; new scans use dropped_roots.
	PolicyCode    string `json:"policy_code,omitempty"`
	PolicyAction  string `json:"policy_action,omitempty"`
	PolicyMessage string `json:"policy_message,omitempty"`
}

type fileFingerprint struct {
	size     int64
	mtime    int64
	sha256   string
	oversize bool
}

func (fingerprint fileFingerprint) contentEquivalent(other fileFingerprint) bool {
	if fingerprint.oversize || other.oversize || fingerprint.sha256 == "" || other.sha256 == "" {
		// Without complete content hashes, retain the conservative legacy
		// comparison so an unobserved large or incomplete write is never hidden.
		return fingerprint == other
	}
	return fingerprint.size == other.size && strings.EqualFold(fingerprint.sha256, other.sha256)
}

type workspaceRootSnapshot struct {
	root     string
	absolute bool
	files    map[string]fileFingerprint
	dropped  bool
}

type workspaceFilesSnapshot struct {
	roots map[string]workspaceRootSnapshot
}

// WorkspaceChangeTracker exposes the same bounded before/after artifact scan
// to provider-owned execution backends. The snapshots remain private so a
// backend cannot fabricate file evidence or relax the established limits.
type WorkspaceChangeTracker struct {
	workspaceDir string
	workingDir   string
	before       workspaceFilesSnapshot
}

func NewWorkspaceChangeTracker(workspaceDir, workingDir string) *WorkspaceChangeTracker {
	return &WorkspaceChangeTracker{
		workspaceDir: workspaceDir, workingDir: workingDir,
		before: snapshotWorkspaceFiles(workspaceDir, workingDir),
	}
}

func (t *WorkspaceChangeTracker) Finish() ([]FileWrite, []string) {
	if t == nil {
		return nil, nil
	}
	after := snapshotWorkspaceFiles(t.workspaceDir, t.workingDir)
	return changedWorkspaceFiles(t.before, after)
}

var errStopKernelWorkspaceScan = fs.SkipAll

func snapshotWorkspaceFiles(workspaceDir, workingDir string) workspaceFilesSnapshot {
	return snapshotWorkspaceFilesWithLimits(workspaceDir, workingDir, maxKernelTrackedFiles, maxKernelWorkspaceScanDuration)
}

func snapshotWorkspaceFilesWithLimits(workspaceDir, workingDir string, maxFiles int, maxDuration time.Duration) workspaceFilesSnapshot {
	result := workspaceFilesSnapshot{roots: map[string]workspaceRootSnapshot{}}
	workspaceDir = filepath.Clean(workspaceDir)
	roots := []string{workspaceDir}
	workingDir = filepath.Clean(strings.TrimSpace(workingDir))
	if filepath.IsAbs(workingDir) && !pathWithin(workspaceDir, workingDir) {
		roots = append(roots, workingDir)
	}
	for _, root := range roots {
		result.roots[root] = snapshotWorkspaceRoot(root, root != workspaceDir, maxFiles, maxDuration)
	}
	return result
}

func snapshotWorkspaceRoot(root string, absolute bool, maxFiles int, maxDuration time.Duration) workspaceRootSnapshot {
	result := workspaceRootSnapshot{root: root, absolute: absolute, files: map[string]fileFingerprint{}}
	deadline := time.Now().Add(maxDuration)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			result.dropped = true
			return nil
		}
		if entry.IsDir() {
			if path != root && excludedKernelWorkspaceDirectory(entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if len(result.files) >= maxFiles || time.Now().After(deadline) {
			result.dropped = true
			return errStopKernelWorkspaceScan
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			if err != nil {
				result.dropped = true
			}
			return nil
		}
		if time.Now().After(deadline) {
			result.dropped = true
			return errStopKernelWorkspaceScan
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." || relative == ".." {
			if err != nil {
				result.dropped = true
			}
			return nil
		}
		fp := fileFingerprint{size: info.Size(), mtime: info.ModTime().UnixNano(), oversize: info.Size() > maxKernelTrackedFileBytes}
		if !fp.oversize {
			file, err := os.Open(path)
			if err != nil {
				result.dropped = true
				return nil
			}
			hash := sha256.New()
			_, copyErr := io.Copy(hash, file)
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil {
				result.dropped = true
				return nil
			}
			if time.Now().After(deadline) {
				result.dropped = true
				return errStopKernelWorkspaceScan
			}
			fp.sha256 = hex.EncodeToString(hash.Sum(nil))
		}
		result.files[filepath.ToSlash(relative)] = fp
		return nil
	})
	if err != nil && !errorsIsSkipAll(err) {
		result.dropped = true
	}
	if time.Now().After(deadline) {
		result.dropped = true
	}
	return result
}

func excludedKernelWorkspaceDirectory(name string) bool {
	switch name {
	case ".git", ".r-libs", "node_modules", "site-packages", "conda-meta":
		return true
	default:
		return false
	}
}

func changedWorkspaceFiles(before, after workspaceFilesSnapshot) ([]FileWrite, []string) {
	var files []FileWrite
	var droppedRoots []string
	for root, currentRoot := range after.roots {
		previousRoot, existed := before.roots[root]
		if currentRoot.dropped || !existed || previousRoot.dropped {
			path := "."
			if currentRoot.absolute {
				path = root
			}
			droppedRoots = append(droppedRoots, path)
			continue
		}
		for path, current := range currentRoot.files {
			previous, existed := previousRoot.files[path]
			if existed && previous.contentEquivalent(current) {
				continue
			}
			reportedPath := path
			if currentRoot.absolute {
				reportedPath = filepath.Join(root, filepath.FromSlash(path))
			}
			files = append(files, FileWrite{Path: reportedPath, SHA256: current.sha256, Oversize: current.oversize})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	sort.Strings(droppedRoots)
	return files, droppedRoots
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func errorsIsSkipAll(err error) bool {
	return err == fs.SkipAll
}
