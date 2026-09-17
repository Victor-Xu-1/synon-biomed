//go:build !windows

package secrets

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func readRegularFile(path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", info.Name())
	}
	if err := protectFile(file); err != nil {
		return nil, err
	}
	return io.ReadAll(file)
}

func atomicPublish(temp, target string, replace bool) error {
	if !replace {
		if _, err := os.Lstat(target); err == nil {
			return os.ErrExist
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Rename(temp, target); err != nil {
		return err
	}
	dir, err := os.Open(filepathDir(target))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
