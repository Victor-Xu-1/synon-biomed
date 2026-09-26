package server

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func (s *Server) resolveWebFSPath(
	userID string,
	requested string,
	workspaceValue string,
	write bool,
	mustExist bool,
	allowLocalRead bool,
) (webFSAccess, error) {
	roots, err := s.webFSRoots(userID, allowLocalRead && !write)
	if err != nil {
		return webFSAccess{}, err
	}
	if strings.TrimSpace(workspaceValue) != "" {
		workspaceRoot, err := canonicalWebFSDirectory(workspaceValue)
		if err != nil {
			return webFSAccess{}, err
		}
		authorized := false
		for _, root := range roots {
			if write && !root.Writable {
				continue
			}
			if webFSPathWithin(root.Path, workspaceRoot) {
				authorized = true
				break
			}
		}
		if !authorized {
			return webFSAccess{}, errWebFSForbidden
		}
		target, err := resolveWebFSTarget(workspaceRoot, requested, mustExist)
		return webFSAccess{Root: workspaceRoot, Target: target}, err
	}
	requested = strings.TrimSpace(requested)
	if requested == "" || !filepath.IsAbs(requested) {
		return webFSAccess{}, fmt.Errorf("%w: an absolute path or workspace is required", errWebFSInvalid)
	}
	absolute, err := filepath.Abs(requested)
	if err != nil {
		return webFSAccess{}, fmt.Errorf("%w: %v", errWebFSInvalid, err)
	}
	for _, root := range roots {
		if write && !root.Writable {
			continue
		}
		if !webFSPathWithin(root.Path, absolute) {
			continue
		}
		target, err := resolveWebFSTarget(root.Path, absolute, mustExist)
		return webFSAccess{Root: root.Path, Target: target}, err
	}
	return webFSAccess{}, errWebFSForbidden
}

// openWebFSRegularFile consumes a resolved, owner-authorized root. The actual
// open remains anchored even if an intermediate directory changes to a symlink
// after path validation. The returned descriptor owns the file snapshot.
func openWebFSRegularFile(access webFSAccess) (*os.File, error) {
	relative, inside := relativePathWithin(access.Root, access.Target)
	if !inside {
		return nil, errWebFSForbidden
	}
	root, err := os.OpenRoot(access.Root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return openRegularWorkspaceRootFile(root, filepath.FromSlash(relative))
}

func (s *Server) webFSRoots(userID string, includeLocalRead bool) ([]webFSRoot, error) {
	roots := make([]webFSRoot, 0, 8)
	if s.workspaceStore != nil {
		for offset := 0; offset < 10_000; offset += 1000 {
			projects, err := s.workspaceStore.ListProjectsForUser(userID, 1000, offset)
			if err != nil {
				return nil, err
			}
			for _, project := range projects {
				if strings.TrimSpace(project.Path) == "" {
					continue
				}
				if path, err := canonicalWebFSDirectory(project.Path); err == nil {
					roots = append(roots, webFSRoot{Path: path, Writable: true})
				}
			}
			if len(projects) < 1000 {
				break
			}
		}
	}
	if s.settingsStore != nil {
		grants, err := s.loadHostGrants(userID)
		if err != nil {
			return nil, err
		}
		for _, grant := range grants {
			path, err := canonicalWebFSDirectory(grant.Path)
			if err != nil {
				continue
			}
			roots = append(roots, webFSRoot{Path: path, Writable: grant.Mode == "read_write"})
		}
	}
	tempRoot, err := s.webFSTempRoot(userID)
	if err != nil {
		return nil, err
	}
	roots = append(roots, webFSRoot{Path: tempRoot, Writable: true})
	if includeLocalRead {
		localRoot := string(filepath.Separator)
		if volume := filepath.VolumeName(filepath.Clean(s.fileRoot)); volume != "" {
			localRoot = volume + string(filepath.Separator)
		}
		roots = append(roots, webFSRoot{Path: filepath.Clean(localRoot)})
	}

	merged := make(map[string]webFSRoot, len(roots))
	for _, root := range roots {
		if existing, ok := merged[root.Path]; ok {
			existing.Writable = existing.Writable || root.Writable
			merged[root.Path] = existing
			continue
		}
		merged[root.Path] = root
	}
	roots = roots[:0]
	for _, root := range merged {
		roots = append(roots, root)
	}
	sort.Slice(roots, func(i, j int) bool { return len(roots[i].Path) > len(roots[j].Path) })
	return roots, nil
}

func (s *Server) webFSTempRoot(userID string) (string, error) {
	base := strings.TrimSpace(s.fileRoot)
	if base == "" {
		base = filepath.Join(os.TempDir(), "synon-go")
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(userID)))
	root := filepath.Join(base, "web-fs-temp", fmt.Sprintf("%x", sum[:8]))
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return "", err
	}
	return canonicalWebFSDirectory(root)
}

func canonicalWebFSDirectory(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%w: directory path is required", errWebFSInvalid)
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errWebFSInvalid, err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("%w: path must be a real directory", errWebFSUnsupported)
	}
	evaluated, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(evaluated), nil
}

func resolveWebFSTarget(root, requested string, mustExist bool) (string, error) {
	root = filepath.Clean(root)
	requested = strings.TrimSpace(requested)
	if requested == "" {
		requested = "."
	}
	var target string
	var err error
	if filepath.IsAbs(requested) {
		target, err = filepath.Abs(requested)
	} else {
		target, err = filepath.Abs(filepath.Join(root, requested))
	}
	if err != nil {
		return "", fmt.Errorf("%w: %v", errWebFSInvalid, err)
	}
	if !webFSPathWithin(root, target) {
		return "", errWebFSForbidden
	}
	directory, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer directory.Close()
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return "", errWebFSForbidden
	}
	info, statErr := directory.Lstat(relative)
	if statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errWebFSUnsupported
		}
		evaluated, err := filepath.EvalSymlinks(target)
		if err != nil {
			return "", err
		}
		if !webFSPathWithin(root, evaluated) {
			return "", errWebFSForbidden
		}
		return filepath.Clean(evaluated), nil
	}
	if !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	if mustExist {
		return "", statErr
	}
	ancestor := filepath.Dir(target)
	for {
		relative, err := filepath.Rel(root, ancestor)
		if err != nil || !webFSPathWithin(root, ancestor) {
			return "", errWebFSForbidden
		}
		info, err := directory.Lstat(relative)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return "", errWebFSUnsupported
			}
			evaluated, err := filepath.EvalSymlinks(ancestor)
			if err != nil {
				return "", err
			}
			if !webFSPathWithin(root, evaluated) {
				return "", errWebFSForbidden
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", errWebFSForbidden
		}
		ancestor = parent
	}
	return filepath.Clean(target), nil
}

func webFSPathWithin(root, target string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(relative))
}
