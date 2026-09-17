//go:build linux

package workspaceimport

import (
	"fmt"
	"math"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func availableBytes(path string) (int64, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return 0, err
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(absolute, &stat); err != nil {
		return 0, fmt.Errorf("measure target capacity: %w", err)
	}
	blocks, blockSize := uint64(stat.Bavail), uint64(stat.Bsize)
	if blockSize == 0 || blocks > uint64(math.MaxInt64)/blockSize {
		return 0, fmt.Errorf("target capacity exceeds supported range")
	}
	available := blocks * blockSize
	return int64(available), nil
}
