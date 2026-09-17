//go:build !windows

package secrets

import "os"

func protectPath(path string, directory bool) error {
	mode := os.FileMode(0o600)
	if directory {
		mode = 0o700
	}
	return os.Chmod(path, mode)
}

func protectFile(file *os.File) error {
	return file.Chmod(0o600)
}
