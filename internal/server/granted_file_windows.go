//go:build windows

package server

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/windows"
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
	buffer := make([]uint16, 32768)
	length, err := windows.GetFinalPathNameByHandle(windows.Handle(file.Fd()), &buffer[0], uint32(len(buffer)), 0)
	if err != nil || length == 0 || length >= uint32(len(buffer)) {
		return fail(errors.New("cannot verify opened file path"))
	}
	finalPath := string(utf16.Decode(buffer[:length]))
	if strings.HasPrefix(finalPath, `\\?\UNC\`) {
		finalPath = `\\` + strings.TrimPrefix(finalPath, `\\?\UNC\`)
	} else {
		finalPath = strings.TrimPrefix(finalPath, `\\?\`)
	}
	finalPath = filepath.Clean(finalPath)
	for _, grant := range grants {
		if hostPathWithin(grant.Path, finalPath) {
			return file, nil
		}
	}
	return fail(errors.New("opened file is outside granted host directories"))
}
