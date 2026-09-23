//go:build windows

package kernel

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// The Windows command is a trusted launcher. The untrusted target is created
// inside an AppContainer only after the launcher belongs to a kill-on-close
// Job. Unsupported authority remains an error, never a direct worker launch.
func newConfinedWorkerCommand(workspaceDir, executable string, arguments, environment []string, mounts []WorkerMount, protected []string) (*exec.Cmd, error) {
	return newConfinedWorkerCommandWithAuxiliary(workspaceDir, executable, arguments, environment, mounts, protected, nil)
}

func newConfinedWorkerCommandWithAuxiliary(workspaceDir, executable string, arguments, environment []string, mounts []WorkerMount, protected []string, auxiliary []*os.File) (*exec.Cmd, error) {
	if err := validateWindowsConfinementRequest(workspaceDir, executable, arguments, environment, mounts, protected, auxiliary); err != nil {
		return nil, err
	}
	roots, files, writable := windowsWorkerAuthority(executable, arguments, environment, mounts)
	frozen := make([]windowsConfinementFrozenPath, 0, len(mounts))
	for _, mount := range mounts {
		if mount.frozen != nil {
			identity, err := windowsFrozenPath(mount.Path, mount.frozen)
			if err != nil {
				return nil, fmt.Errorf("identify Windows frozen kernel mount: %w", err)
			}
			frozen = append(frozen, identity)
		}
	}
	if !platformConfinementEvidence().Available {
		return nil, fmt.Errorf("%w: Windows managed runtime and recovery boundaries are not verified", ErrConfinementUnavailable)
	}
	arguments = windowsIsolatedWorkerArguments(executable, arguments)
	return newWindowsConfinedCommandWithFrozenAuthority(workspaceDir, executable, arguments, environment, roots, files, writable, frozen)
}

func windowsIsolatedWorkerArguments(executable string, arguments []string) []string {
	base := strings.ToLower(filepath.Base(executable))
	if strings.HasPrefix(base, "python") && strings.HasSuffix(base, ".exe") &&
		(len(arguments) == 0 || arguments[0] != "-I") {
		return append([]string{"-I"}, arguments...)
	}
	return arguments
}

func validateWindowsConfinementRequest(workspaceDir, executable string, arguments, environment []string, mounts []WorkerMount, protected []string, auxiliary []*os.File) error {
	if len(auxiliary) != 0 {
		return errors.New("Windows kernel auxiliary handles have no confined transport")
	}
	if err := validateWindowsKernelPath(workspaceDir, true, false); err != nil {
		return fmt.Errorf("kernel workspace: %w", err)
	}
	if err := validateWindowsKernelPath(executable, true, true); err != nil {
		return fmt.Errorf("kernel executable: %w", err)
	}
	if windowsKernelPathsOverlap(workspaceDir, executable) {
		return errors.New("kernel executable cannot be within the writable workspace")
	}
	for _, argument := range arguments {
		if strings.ContainsRune(argument, '\x00') {
			return errors.New("kernel worker argument contains NUL")
		}
	}
	keys := make(map[string]struct{}, len(environment))
	for _, entry := range environment {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" || strings.HasPrefix(key, "=") ||
			strings.ContainsAny(key, "\x00\r\n") || strings.ContainsAny(entry, "\x00\r\n") {
			return errors.New("kernel worker environment is invalid")
		}
		normalized := strings.ToUpper(key)
		if _, duplicate := keys[normalized]; duplicate {
			return errors.New("kernel worker environment has duplicate Windows keys")
		}
		keys[normalized] = struct{}{}
	}
	for _, path := range protected {
		if err := validateWindowsKernelPath(path, false, false); err != nil {
			return fmt.Errorf("kernel protected path: %w", err)
		}
	}
	for _, mount := range mounts {
		if err := validateWindowsKernelPath(mount.Path, true, mount.regular); err != nil {
			return fmt.Errorf("kernel mount: %w", err)
		}
		if mount.frozen != nil {
			current, err := os.Stat(mount.Path)
			frozen, frozenErr := mount.frozen.Stat()
			if err != nil || frozenErr != nil || !os.SameFile(current, frozen) {
				return errors.New("kernel frozen mount no longer identifies its path")
			}
		}
		if mount.Writable && (!mount.trusted || !mount.regular || mount.frozen == nil) {
			return fmt.Errorf("%w: Windows external writable mounts require a frozen trusted regular file", ErrConfinementUnavailable)
		}
		if windowsKernelPathsOverlap(mount.Path, workspaceDir) {
			// NTFS grants on a writable parent do not provide the read-only
			// overlay that Linux mount namespaces enforce for a child.
			return errors.New("kernel workspace mount overlay cannot be enforced on Windows")
		}
		if windowsKernelPathsOverlap(mount.Path, executable) {
			return errors.New("kernel mount overlaps the worker executable")
		}
		for _, path := range protected {
			if windowsKernelPathsOverlap(mount.Path, path) {
				return errors.New("kernel mount overlaps a protected path")
			}
		}
	}
	if len(protected) != 0 {
		return fmt.Errorf("%w: Windows protected paths have no deny authority", ErrConfinementUnavailable)
	}
	return nil
}

// The manager supplies the executable, worker script and verified runtime
// mounts. Never infer authority from arbitrary absolute arguments: those may
// contain user-supplied paths that the worker must not gain access to.
func windowsWorkerAuthority(executable string, arguments, environment []string, mounts []WorkerMount) (roots, files, writable []string) {
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(key, "CONDA_PREFIX") && windowsKernelPathContains(value, executable) {
			roots = appendUniqueWindowsAuthorityPath(roots, value)
		}
	}
	base := strings.ToLower(filepath.Base(executable))
	if base == "python.exe" || base == "pythonw.exe" || base == "rscript.exe" {
		// A system Python without CONDA_PREFIX still needs its standard
		// library. Managed prefixes take precedence over this fallback.
		if len(roots) == 0 {
			root := filepath.Dir(executable)
			if strings.EqualFold(filepath.Base(root), "Scripts") || strings.EqualFold(filepath.Base(root), "bin") {
				root = filepath.Dir(root)
			}
			roots = appendUniqueWindowsAuthorityPath(roots, root)
		}
		for _, argument := range arguments {
			name := strings.ToLower(filepath.Base(argument))
			if name == "kernel_worker.py" || name == "kernel_worker.r" {
				roots = appendUniqueWindowsAuthorityPath(roots, filepath.Dir(argument))
				break
			}
		}
	}
	for _, mount := range mounts {
		if mount.Writable {
			writable = appendUniqueWindowsAuthorityPath(writable, mount.Path)
		} else if mount.regular {
			files = appendUniqueWindowsAuthorityPath(files, mount.Path)
		} else {
			roots = appendUniqueWindowsAuthorityPath(roots, mount.Path)
		}
	}
	return roots, files, writable
}

func appendUniqueWindowsAuthorityPath(paths []string, path string) []string {
	for _, existing := range paths {
		if strings.EqualFold(existing, path) {
			return paths
		}
	}
	return append(paths, path)
}

func windowsKernelPathContains(parent, child string) bool {
	parent = strings.ToLower(filepath.Clean(parent))
	child = strings.ToLower(filepath.Clean(child))
	return strings.HasPrefix(child, parent+string(os.PathSeparator))
}

// validateWindowsKernelPath deliberately accepts only ordinary, already
// existing, drive-rooted filesystem objects for executable authority.
// Component checks catch junctions and symlinks, including reparse points
// that os.Lstat does not classify as ModeSymlink.
func validateWindowsKernelPath(path string, mustExist, regular bool) error {
	volume := filepath.VolumeName(path)
	if len(volume) != 2 || volume[1] != ':' || !filepath.IsAbs(path) ||
		filepath.Clean(path) != path || len(path) <= len(volume)+1 {
		return errors.New("path must be an absolute ordinary drive path")
	}
	tail := path[len(volume):]
	if !strings.HasPrefix(tail, `\`) {
		return errors.New("path must be drive-rooted")
	}
	components := strings.Split(tail[1:], `\`)
	current := volume + `\`
	for index, component := range components {
		if component == "" || component == "." || component == ".." ||
			strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") ||
			strings.ContainsAny(component, "<>:\"|?*") {
			return errors.New("path contains an unsafe component")
		}
		current = filepath.Join(current, component)
		if !mustExist {
			continue
		}
		name, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return errors.New("path cannot be encoded")
		}
		attributes, err := windows.GetFileAttributes(name)
		if err != nil {
			return errors.New("path component is unavailable")
		}
		if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return errors.New("path contains a reparse point")
		}
		directory := attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
		if index < len(components)-1 && !directory {
			return errors.New("path parent is not a directory")
		}
		if index == len(components)-1 && directory == regular {
			return errors.New("path has the wrong file type")
		}
	}
	return nil
}

func windowsKernelPathsOverlap(left, right string) bool {
	left = strings.ToLower(filepath.Clean(left))
	right = strings.ToLower(filepath.Clean(right))
	return left == right ||
		strings.HasPrefix(left, right+string(os.PathSeparator)) ||
		strings.HasPrefix(right, left+string(os.PathSeparator))
}

func platformConfinementEvidence() ConfinementEvidence {
	return ConfinementEvidence{
		Available: false, Mode: "unavailable", Reason: "Synon kernel confinement is unavailable on this platform",
	}
}

func probePlatformConfinement() ConfinementEvidence {
	if err := recoverWindowsConfinementLeases(); err != nil {
		return ConfinementEvidence{
			Available: false, Mode: "unavailable", Reason: "Windows kernel confinement recovery failed",
		}
	}
	// Do not infer confinement from a successful Job Object assignment or a
	// system helper probe. Managed runtime, mount and broker contracts remain
	// unavailable until verified end to end.
	return platformConfinementEvidence()
}
