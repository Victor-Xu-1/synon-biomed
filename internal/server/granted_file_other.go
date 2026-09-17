//go:build !linux && !windows

package server

import (
	"errors"
	"os"
)

func openGrantedRegularFile(string, []hostGrant) (*os.File, error) {
	return nil, errors.New("secure opened-file path verification is unavailable on this platform")
}
