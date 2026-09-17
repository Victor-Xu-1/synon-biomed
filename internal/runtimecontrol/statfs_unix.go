//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package runtimecontrol

import "syscall"

func availableBytes(path string) *uint64 {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return nil
	}
	value := uint64(stat.Bavail) * uint64(stat.Bsize)
	return &value
}
