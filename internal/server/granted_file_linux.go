//go:build linux

package server

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
)

func openGrantedRegularFile(path string, grants []hostGrant) (*os.File, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*os.File, error) {
		_ = file.Close()
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fail(errors.New("granted path is not a regular file"))
	}
	finalPath, err := filepath.EvalSymlinks("/proc/self/fd/" + strconv.FormatUint(uint64(file.Fd()), 10))
	if err != nil {
		return fail(errors.New("cannot verify opened file path"))
	}
	for _, grant := range grants {
		if hostPathWithin(grant.Path, finalPath) {
			return file, nil
		}
	}
	return fail(errors.New("opened file is outside granted host directories"))
}
