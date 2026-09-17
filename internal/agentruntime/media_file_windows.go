//go:build windows

package agentruntime

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func readAllowedMediaFileHandle(path string, roots []string, maxBytes int64) ([]byte, error) {
	for _, rawRoot := range roots {
		root := strings.TrimSpace(rawRoot)
		if root == "" || !filepath.IsAbs(root) {
			continue
		}
		resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(resolvedRoot, path)
		if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		file, err := openMediaWindowsAtRoot(resolvedRoot, relative)
		if err != nil {
			return nil, errors.New("media file could not be opened safely")
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || info.Size() <= 0 || info.Size() > maxBytes {
			return nil, fmt.Errorf("media file size must be between 1 and %d bytes", maxBytes)
		}
		data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
		if err != nil {
			return nil, errors.New("media file could not be read")
		}
		if int64(len(data)) > maxBytes {
			return nil, fmt.Errorf("media part exceeds limit of %d bytes", maxBytes)
		}
		return data, nil
	}
	return nil, errors.New("media file is outside allowed roots")
}

func openMediaWindowsAtRoot(root, relative string) (*os.File, error) {
	parts := strings.Split(filepath.Clean(relative), string(filepath.Separator))
	if len(parts) == 0 {
		return nil, errors.New("media file path is invalid")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.IndexByte(part, 0) >= 0 || strings.Contains(part, ":") {
			return nil, errors.New("media file path is invalid")
		}
	}
	current, err := openMediaWindowsRoot(root)
	if err != nil {
		return nil, err
	}
	for _, part := range parts[:len(parts)-1] {
		next, openErr := openMediaWindowsObject(current, part, windows.FILE_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT)
		_ = windows.CloseHandle(current)
		if openErr != nil {
			return nil, openErr
		}
		if err := validateMediaWindowsDirectory(next); err != nil {
			_ = windows.CloseHandle(next)
			return nil, err
		}
		current = next
	}
	handle, err := openMediaWindowsObject(current, parts[len(parts)-1], windows.FILE_NON_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT)
	_ = windows.CloseHandle(current)
	if err != nil {
		return nil, err
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil ||
		information.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 ||
		information.NumberOfLinks != 1 {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("media target must be one regular file")
	}
	return os.NewFile(uintptr(handle), filepath.Base(relative)), nil
}

func openMediaWindowsRoot(root string) (windows.Handle, error) {
	root = filepath.Clean(root)
	ntPath := `\??\` + root
	if strings.HasPrefix(root, `\\`) {
		ntPath = `\??\UNC\` + strings.TrimPrefix(root, `\\`)
	}
	name, err := windows.NewNTUnicodeString(ntPath)
	if err != nil {
		return windows.InvalidHandle, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length: uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})), ObjectName: name,
		Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	allocation := int64(0)
	err = windows.NtCreateFile(
		&handle, windows.FILE_GENERIC_READ|windows.SYNCHRONIZE, attributes, &status, &allocation, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0,
	)
	if err != nil {
		return windows.InvalidHandle, err
	}
	if err := validateMediaWindowsDirectory(handle); err != nil {
		_ = windows.CloseHandle(handle)
		return windows.InvalidHandle, err
	}
	return handle, nil
}

func openMediaWindowsObject(root windows.Handle, name string, options uint32) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return windows.InvalidHandle, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length: uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})), RootDirectory: root, ObjectName: objectName,
		Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	allocation := int64(0)
	err = windows.NtCreateFile(
		&handle, windows.FILE_GENERIC_READ|windows.SYNCHRONIZE, attributes, &status, &allocation, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN, options, 0, 0,
	)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return handle, nil
}

func validateMediaWindowsDirectory(handle windows.Handle) error {
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return err
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 ||
		information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("media path component is not a real directory")
	}
	return nil
}
