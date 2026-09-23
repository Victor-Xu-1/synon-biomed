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

// Windows process supervision is not a filesystem or network sandbox. Until
// the worker launcher can create an AppContainer process with security
// capabilities before its first instruction, this entry point must not return
// an executable command. Keep the request checks here so a future launcher
// cannot silently accept path or handle authority that its policy cannot
// represent.
func newConfinedWorkerCommand(workspaceDir, executable string, arguments, environment []string, mounts []WorkerMount, protected []string) (*exec.Cmd, error) {
	return newConfinedWorkerCommandWithAuxiliary(workspaceDir, executable, arguments, environment, mounts, protected, nil)
}

func newConfinedWorkerCommandWithAuxiliary(workspaceDir, executable string, arguments, environment []string, mounts []WorkerMount, protected []string, auxiliary []*os.File) (*exec.Cmd, error) {
	if err := validateWindowsConfinementRequest(workspaceDir, executable, arguments, environment, mounts, protected, auxiliary); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("%w: native Windows worker identity, filesystem, and network boundaries are not installed", ErrConfinementUnavailable)
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
		if mount.frozen != nil || (mount.trusted && mount.Writable) {
			return errors.New("kernel mount authority is invalid on Windows")
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
	return nil
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
	// Do not infer confinement from a successful Job Object assignment. That
	// proves process-tree ownership, not restricted identity or denied I/O.
	return platformConfinementEvidence()
}
