//go:build !linux && !windows

package agentruntime

import "errors"

func readAllowedMediaFileHandle(string, []string, int64) ([]byte, error) {
	return nil, errors.New("media file sources are unavailable on this platform")
}
