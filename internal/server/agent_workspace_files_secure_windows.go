//go:build windows

package server

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/google/uuid"
	"golang.org/x/sys/windows"
)

type agentWorkspaceEditAuthority struct {
	parent windows.Handle
	name   string
}

type agentWorkspaceStagingFile struct {
	file *os.File
	name string
}

type agentWorkspaceFileRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

func openAgentWorkspaceRegularFile(root, relative string) (*os.File, error) {
	parent, name, err := openAgentWorkspaceWindowsParent(root, relative, false, false)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(parent)
	handle, err := openAgentWorkspaceWindowsObject(parent, name, windows.FILE_GENERIC_READ, windows.FILE_OPEN, windows.FILE_NON_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if err != nil {
		return nil, err
	}
	if err := validateAgentWorkspaceWindowsRegular(handle, true); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	return os.NewFile(uintptr(handle), filepath.Join(root, relative)), nil
}

func openAgentWorkspaceEditAuthority(root, relative string, createParents bool) (*agentWorkspaceEditAuthority, error) {
	parent, name, err := openAgentWorkspaceWindowsParent(root, relative, createParents, true)
	if err != nil {
		return nil, err
	}
	return &agentWorkspaceEditAuthority{parent: parent, name: name}, nil
}

func openAgentWorkspaceWindowsParent(root, relative string, createParents, writable bool) (windows.Handle, string, error) {
	parts, err := secureAgentWorkspacePathParts(relative)
	if err != nil {
		return windows.InvalidHandle, "", err
	}
	access := uint32(windows.FILE_GENERIC_READ | windows.SYNCHRONIZE)
	if writable || createParents {
		access |= windows.FILE_GENERIC_WRITE | windows.DELETE
	}
	current, err := openAgentWorkspaceWindowsRoot(root, access)
	if err != nil {
		return windows.InvalidHandle, "", err
	}
	fail := func(err error) (windows.Handle, string, error) {
		_ = windows.CloseHandle(current)
		return windows.InvalidHandle, "", err
	}
	for _, part := range parts[:len(parts)-1] {
		disposition := uint32(windows.FILE_OPEN)
		if createParents {
			disposition = windows.FILE_OPEN_IF
		}
		next, openErr := openAgentWorkspaceWindowsObject(current, part, access, disposition, windows.FILE_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT)
		if openErr != nil {
			return fail(openErr)
		}
		if err := validateAgentWorkspaceWindowsDirectory(next); err != nil {
			_ = windows.CloseHandle(next)
			return fail(err)
		}
		_ = windows.CloseHandle(current)
		current = next
	}
	return current, parts[len(parts)-1], nil
}

func openAgentWorkspaceWindowsRoot(root string, access uint32) (windows.Handle, error) {
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
		Length:     uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		ObjectName: name,
		Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	allocation := int64(0)
	err = windows.NtCreateFile(
		&handle, access, attributes, &status, &allocation, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0,
	)
	if err != nil {
		return windows.InvalidHandle, err
	}
	if err := validateAgentWorkspaceWindowsDirectory(handle); err != nil {
		_ = windows.CloseHandle(handle)
		return windows.InvalidHandle, err
	}
	return handle, nil
}

func openAgentWorkspaceWindowsObject(root windows.Handle, name string, access, disposition, options uint32) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return windows.InvalidHandle, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: root,
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	allocation := int64(0)
	err = windows.NtCreateFile(
		&handle, access, attributes, &status, &allocation, windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		disposition, options, 0, 0,
	)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return handle, nil
}

func validateAgentWorkspaceWindowsDirectory(handle windows.Handle) error {
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return err
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("workspace path component is not a real directory")
	}
	return nil
}

func validateAgentWorkspaceWindowsRegular(handle windows.Handle, requireSingleLink bool) error {
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return err
	}
	if information.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return errors.New("workspace target is not a regular file")
	}
	if requireSingleLink && information.NumberOfLinks != 1 {
		return errors.New("workspace target must have exactly one link")
	}
	return nil
}

func (authority *agentWorkspaceEditAuthority) openCurrent() (*os.File, os.FileMode, bool, error) {
	handle, err := openAgentWorkspaceWindowsObject(
		authority.parent, authority.name,
		windows.FILE_GENERIC_READ|windows.DELETE,
		windows.FILE_OPEN,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT,
	)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) ||
		errors.Is(err, windows.STATUS_OBJECT_NAME_NOT_FOUND) || errors.Is(err, windows.STATUS_OBJECT_PATH_NOT_FOUND) {
		return nil, 0o600, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	if err := validateAgentWorkspaceWindowsRegular(handle, true); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, 0, false, err
	}
	file := os.NewFile(uintptr(handle), authority.name)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, 0, false, err
	}
	return file, info.Mode().Perm(), true, nil
}

func (authority *agentWorkspaceEditAuthority) createStaging(mode os.FileMode) (*agentWorkspaceStagingFile, error) {
	if mode == 0 {
		mode = 0o600
	}
	for attempt := 0; attempt < 16; attempt++ {
		name := ".synon-edit-" + uuid.NewString()
		handle, err := openAgentWorkspaceWindowsObject(
			authority.parent, name,
			windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.DELETE,
			windows.FILE_CREATE,
			windows.FILE_NON_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		)
		if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) ||
			errors.Is(err, windows.STATUS_OBJECT_NAME_COLLISION) {
			continue
		}
		if err != nil {
			return nil, err
		}
		file := os.NewFile(uintptr(handle), name)
		if err := file.Chmod(mode.Perm()); err != nil {
			_ = file.Close()
			return nil, err
		}
		return &agentWorkspaceStagingFile{file: file, name: name}, nil
	}
	return nil, errors.New("could not allocate a workspace staging file")
}

func (authority *agentWorkspaceEditAuthority) commit(staging *agentWorkspaceStagingFile) error {
	if staging == nil || staging.file == nil || staging.name == "" {
		return errors.New("workspace staging file is unavailable")
	}
	if err := staging.file.Sync(); err != nil {
		return err
	}
	name, err := windows.UTF16FromString(authority.name)
	if err != nil {
		return err
	}
	nameLength := (len(name) - 1) * 2
	var shape agentWorkspaceFileRenameInformation
	buffer := make([]byte, int(unsafe.Offsetof(shape.FileName))+nameLength)
	information := (*agentWorkspaceFileRenameInformation)(unsafe.Pointer(&buffer[0]))
	information.ReplaceIfExists = windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS
	information.RootDirectory = authority.parent
	information.FileNameLength = uint32(nameLength)
	copy((*[windows.MAX_LONG_PATH]uint16)(unsafe.Pointer(&information.FileName[0]))[:nameLength/2:nameLength/2], name[:len(name)-1])
	var status windows.IO_STATUS_BLOCK
	if err := windows.NtSetInformationFile(
		windows.Handle(staging.file.Fd()), &status, &buffer[0], uint32(len(buffer)), windows.FileRenameInformation,
	); err != nil {
		return err
	}
	staging.name = ""
	err = staging.file.Close()
	staging.file = nil
	return err
}

func (authority *agentWorkspaceEditAuthority) actualPath() (string, error) {
	buffer := make([]uint16, 32768)
	length, err := windows.GetFinalPathNameByHandle(authority.parent, &buffer[0], uint32(len(buffer)), 0)
	if err != nil || length == 0 || length >= uint32(len(buffer)) {
		return "", errors.New("workspace parent path could not be resolved")
	}
	parent := windows.UTF16ToString(buffer[:length])
	if strings.HasPrefix(parent, `\\?\UNC\`) {
		parent = `\\` + strings.TrimPrefix(parent, `\\?\UNC\`)
	} else {
		parent = strings.TrimPrefix(parent, `\\?\`)
	}
	return filepath.Join(parent, authority.name), nil
}

func (authority *agentWorkspaceEditAuthority) close() error {
	if authority == nil || authority.parent == windows.InvalidHandle || authority.parent == 0 {
		return nil
	}
	err := windows.CloseHandle(authority.parent)
	authority.parent = windows.InvalidHandle
	return err
}

func (staging *agentWorkspaceStagingFile) discard(authority *agentWorkspaceEditAuthority) {
	if staging == nil || staging.file == nil {
		return
	}
	deleteFile := byte(1)
	var status windows.IO_STATUS_BLOCK
	_ = windows.NtSetInformationFile(
		windows.Handle(staging.file.Fd()), &status, &deleteFile, 1, windows.FileDispositionInformation,
	)
	_ = staging.file.Close()
	staging.file = nil
	staging.name = ""
}

func secureEnsureAgentWorkspaceDirectory(path string, mode os.FileMode) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil || !filepath.IsAbs(absolute) {
		return "", errors.New("workspace directory path is invalid")
	}
	volume := filepath.VolumeName(absolute)
	if volume == "" {
		return "", errors.New("workspace directory volume is unavailable")
	}
	root := volume + string(os.PathSeparator)
	relative, err := filepath.Rel(root, absolute)
	if err != nil {
		return "", err
	}
	if relative == "." {
		return canonicalHostDirectory(root)
	}
	parts, err := secureAgentWorkspacePathParts(relative)
	if err != nil {
		return "", err
	}
	access := uint32(windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.DELETE | windows.SYNCHRONIZE)
	current, err := openAgentWorkspaceWindowsRoot(root, access)
	if err != nil {
		return "", err
	}
	defer func() { _ = windows.CloseHandle(current) }()
	for _, part := range parts {
		next, openErr := openAgentWorkspaceWindowsObject(current, part, access, windows.FILE_OPEN_IF, windows.FILE_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT)
		if openErr != nil {
			return "", openErr
		}
		if err := validateAgentWorkspaceWindowsDirectory(next); err != nil {
			_ = windows.CloseHandle(next)
			return "", err
		}
		_ = windows.CloseHandle(current)
		current = next
	}
	buffer := make([]uint16, 32768)
	length, err := windows.GetFinalPathNameByHandle(current, &buffer[0], uint32(len(buffer)), 0)
	if err != nil || length == 0 || length >= uint32(len(buffer)) {
		return "", errors.New("workspace directory path could not be resolved")
	}
	resolved := windows.UTF16ToString(buffer[:length])
	if strings.HasPrefix(resolved, `\\?\UNC\`) {
		resolved = `\\` + strings.TrimPrefix(resolved, `\\?\UNC\`)
	} else {
		resolved = strings.TrimPrefix(resolved, `\\?\`)
	}
	_ = mode
	return filepath.Clean(resolved), nil
}
