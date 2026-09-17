//go:build !windows

package sqlitebackup

import (
	"errors"
	"os"
	"syscall"
)

func validateRootOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("sqlite backup root must be owned by the current user")
	}
	return nil
}
