//go:build linux

package kernel

import (
	"errors"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

func newProviderRuntimeSocketPair(name string) (*os.File, *os.File, error) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, errors.New("provider runtime socketpair is unavailable")
	}
	parent := os.NewFile(uintptr(pair[0]), name+"-parent")
	child := os.NewFile(uintptr(pair[1]), name+"-child")
	if parent == nil || child == nil {
		if parent != nil {
			_ = parent.Close()
		}
		if child != nil {
			_ = child.Close()
		}
		return nil, nil, errors.New("provider runtime socketpair is unavailable")
	}
	return parent, child, nil
}

func providerHostNetworkNamespaceInode() (string, error) {
	var stat unix.Stat_t
	if err := unix.Stat("/proc/self/ns/net", &stat); err != nil || stat.Ino == 0 {
		return "", errors.New("provider host network namespace is unavailable")
	}
	return strconv.FormatUint(stat.Ino, 10), nil
}
