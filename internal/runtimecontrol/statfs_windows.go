//go:build windows

package runtimecontrol

import (
	"syscall"
	"unsafe"
)

var getDiskFreeSpaceExW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

func availableBytes(path string) *uint64 {
	pathPointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil
	}
	var available uint64
	result, _, _ := getDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(pathPointer)),
		uintptr(unsafe.Pointer(&available)),
		0,
		0,
	)
	if result == 0 {
		return nil
	}
	return &available
}
