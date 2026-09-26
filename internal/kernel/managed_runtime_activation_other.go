//go:build !windows

package kernel

import "os"

func replaceManagedRuntimePointer(temporary, active string) error {
	return os.Rename(temporary, active)
}
