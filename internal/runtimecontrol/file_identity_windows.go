//go:build windows

package runtimecontrol

import (
	"errors"
	"io/fs"

	"golang.org/x/sys/windows"
)

const usageAccounting = "logical-unique-within-category"

func linkedFileIdentity(path string, _ fs.FileInfo) (fileIdentity, bool, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fileIdentity{}, false, err
	}
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return fileIdentity{}, false, err
	}
	defer windows.CloseHandle(handle)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return fileIdentity{}, false, err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		return fileIdentity{}, false, errors.New("storage entry changed during measurement")
	}
	return fileIdentity{uint64(info.VolumeSerialNumber), uint64(info.FileIndexHigh), uint64(info.FileIndexLow)}, info.NumberOfLinks > 1, nil
}
