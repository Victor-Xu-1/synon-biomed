//go:build windows

package kernel

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// Windows keeps a handle to the exact already-verified directory. A reparse
// point must not redirect a shared scientific library outside its owner root.
func freezeInternalKernelDirectory(path string) (*os.File, error) {
	if err := validateWindowsKernelPath(path, true, false); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.IsDir() {
		_ = file.Close()
		return nil, errors.New("kernel shared library is not an ordinary directory")
	}
	return file, nil
}

func secureROperationLog(prefix string) (string, *os.File, error) {
	if err := validateWindowsKernelPath(prefix, true, false); err != nil {
		return "", nil, errors.New("R operation log environment is unavailable")
	}
	path := filepath.Join(prefix, ".operon_metadata.r.ndjson")
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return "", nil, fmt.Errorf("open R operation log: %w", err)
	}
	var identity windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &identity); err != nil ||
		identity.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 ||
		identity.NumberOfLinks != 1 {
		_ = windows.CloseHandle(handle)
		return "", nil, errors.New("R operation log is not a private ordinary file")
	}
	if err := validateWindowsKernelPath(path, true, true); err != nil {
		_ = windows.CloseHandle(handle)
		return "", nil, err
	}
	security, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return "", nil, err
	}
	owner, _, err := security.Owner()
	user, userErr := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || userErr != nil || owner == nil || !owner.Equals(user.User.Sid) {
		_ = windows.CloseHandle(handle)
		return "", nil, errors.New("R operation log is not owned by the kernel host")
	}
	return path, os.NewFile(uintptr(handle), path), nil
}

func readBoundedKernelMetadataFile(prefix, name string, limit int) ([]byte, bool, error) {
	if limit <= 0 || name == "" || filepath.Base(name) != name ||
		strings.ContainsAny(name, "<>:\"/\\|?*\x00\r\n") {
		return nil, false, errors.New("kernel metadata name or bound is invalid")
	}
	if err := validateWindowsKernelPath(prefix, true, false); err != nil {
		return nil, false, errors.New("kernel metadata directory is unavailable")
	}
	path := filepath.Join(prefix, name)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, err
	}
	if err := validateWindowsKernelPath(path, true, true); err != nil {
		return nil, false, err
	}
	namePointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, false, err
	}
	handle, err := windows.CreateFile(namePointer, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, false, err
	}
	file := os.NewFile(uintptr(handle), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > int64(limit) {
		return nil, false, errors.New("kernel metadata is not a bounded ordinary file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil || len(raw) > limit {
		return nil, false, errors.New("kernel metadata exceeds the size limit")
	}
	return raw, true, nil
}

func replaceKernelDirectory(staging, target string, targetExists bool) error {
	if targetExists {
		// Windows has no equivalent to the atomic directory exchange used by
		// the Linux publisher. Never expose a half-replaced shared library.
		return ErrConfinementUnavailable
	}
	return os.Rename(staging, target)
}

func CopyProviderOperationFile(string, string, io.Writer, int64) (int64, error) {
	// Provider operation sockets still have no Windows auxiliary-handle
	// transport, so this API must not suggest provider execution is ready.
	return 0, ErrConfinementUnavailable
}
