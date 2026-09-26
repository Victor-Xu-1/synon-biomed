package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func validateManagedLockedRequirements(path, expectedSHA256 string) (string, string, error) {
	path = strings.TrimSpace(path)
	expectedSHA256 = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(expectedSHA256), "sha256:"))
	if path == "" && expectedSHA256 == "" {
		return "", "", nil
	}
	if path == "" || len(expectedSHA256) != sha256.Size*2 {
		return "", "", errors.New("locked requirements path and SHA-256 must be supplied together")
	}
	if _, err := hex.DecodeString(expectedSHA256); err != nil {
		return "", "", errors.New("locked requirements SHA-256 is invalid")
	}
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return "", "", errors.New("locked requirements path must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return "", "", errors.New("locked requirements path must be canonical")
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maxManagedLockedRequirementsBytes {
		return "", "", errors.New("locked requirements file is unavailable or exceeds the size limit")
	}
	file, err := os.Open(resolved)
	if err != nil {
		return "", "", errors.New("locked requirements file is unavailable")
	}
	defer file.Close()
	digest := sha256.New()
	written, err := io.Copy(digest, io.LimitReader(file, maxManagedLockedRequirementsBytes+1))
	if err != nil || written != info.Size() || hex.EncodeToString(digest.Sum(nil)) != expectedSHA256 {
		return "", "", errors.New("locked requirements file failed checksum validation")
	}
	return resolved, expectedSHA256, nil
}
