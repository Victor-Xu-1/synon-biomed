//go:build windows

package sqlitebackup

import "os"

func validateRootOwner(os.FileInfo) error {
	return nil
}
