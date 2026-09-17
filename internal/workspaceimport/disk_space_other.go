//go:build !linux

package workspaceimport

import "errors"

func availableBytes(string) (int64, error) {
	return 0, errors.New("target capacity measurement is not supported on this platform")
}
