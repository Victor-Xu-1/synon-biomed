//go:build windows

package kernel

import (
	"errors"
	"os"
	"path/filepath"
)

const windowsManagedRGenerationDirectoryHex = 32

// R embeds its installation prefix. The Windows native runtime contains deep
// compiler headers that exceed legacy Win32 path limits when installed under
// the full 64-hex generation name. The directory key is only a locator: the
// full SHA-256 remains authoritative in the marker and execution contracts.
func managedRGenerationDirectoryName(generation string) string {
	return generation[:windowsManagedRGenerationDirectoryHex]
}

// A truncated locator must never allow one verified generation to replace a
// different one with the same prefix. Invalid or interrupted directories are
// handled by the existing recovery path; a readable marker with a different
// full identity is a collision and must be preserved untouched.
func rejectManagedRGenerationPathCollision(path, generation string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	markerPath := filepath.Join(path, managedRuntimeMarkerName)
	markerInfo, err := os.Lstat(markerPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !markerInfo.Mode().IsRegular() {
		return nil
	}
	var marker managedRuntimeMarker
	if _, err := readBoundedJSON(markerPath, &marker); err != nil {
		return nil
	}
	if marker.Generation != "" && marker.Generation != generation {
		return errors.New("managed R generation directory collides with another full generation")
	}
	return nil
}
