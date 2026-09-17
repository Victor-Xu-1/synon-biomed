//go:build windows

package secrets

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/windows"
)

func readRegularFile(path string) ([]byte, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_READ|windows.WRITE_DAC,
		windows.FILE_SHARE_READ,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	attributes, err := windows.GetFileAttributes(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return nil, fmt.Errorf("%s is not a regular file", info.Name())
	}
	if err := protectFile(file); err != nil {
		return nil, err
	}
	return io.ReadAll(file)
}

func atomicPublish(temp, target string, replace bool) error {
	from, err := windows.UTF16PtrFromString(temp)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	flags := uint32(windows.MOVEFILE_WRITE_THROUGH)
	if replace {
		flags |= windows.MOVEFILE_REPLACE_EXISTING
	}
	return windows.MoveFileEx(from, to, flags)
}
