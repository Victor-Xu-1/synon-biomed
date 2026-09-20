//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly && !windows

package runtimecontrol

import "io/fs"

const usageAccounting = "logical-per-entry"

func linkedFileIdentity(_ string, _ fs.FileInfo) (fileIdentity, bool, error) {
	return fileIdentity{}, false, nil
}
