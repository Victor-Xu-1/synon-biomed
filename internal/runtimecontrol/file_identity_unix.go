//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package runtimecontrol

import (
	"errors"
	"io/fs"
	"syscall"
)

const usageAccounting = "logical-unique-within-category"

func linkedFileIdentity(_ string, info fs.FileInfo) (fileIdentity, bool, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileIdentity{}, false, errors.New("storage file identity is unavailable")
	}
	return fileIdentity{volume: uint64(stat.Dev), low: uint64(stat.Ino)}, stat.Nlink > 1, nil
}
