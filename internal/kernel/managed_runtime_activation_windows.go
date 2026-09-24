//go:build windows

package kernel

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32MoveFileExW = windows.NewLazySystemDLL("kernel32.dll").NewProc("MoveFileExW")
)

const (
	moveFileReplaceExisting = 0x00000001
	moveFileWriteThrough    = 0x00000008
)

func replaceManagedRuntimePointer(temporary, active string) error {
	source, err := windows.UTF16PtrFromString(temporary)
	if err != nil {
		return err
	}
	destination, err := windows.UTF16PtrFromString(active)
	if err != nil {
		return err
	}
	result, _, callErr := kernel32MoveFileExW.Call(
		uintptr(unsafe.Pointer(source)), uintptr(unsafe.Pointer(destination)),
		moveFileReplaceExisting|moveFileWriteThrough,
	)
	if result == 0 {
		return fmt.Errorf("replace managed runtime activation pointer: %w", callErr)
	}
	return nil
}
