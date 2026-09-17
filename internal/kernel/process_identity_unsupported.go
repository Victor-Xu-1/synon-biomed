//go:build !linux

package kernel

import "errors"

type ProcessIdentity struct {
	PID        int
	StartTicks int64
	PGID       int
}

func (w *Worker) ProcessIdentity() (ProcessIdentity, error) {
	return ProcessIdentity{}, errors.New("kernel worker process identity is unavailable on this platform")
}

func CurrentProcessStartTicks() (int64, error) {
	return 0, errors.New("process start identity is unavailable on this platform")
}

func ProcessIdentityAlive(pid int64, startTicks int64) (bool, error) {
	return false, errors.New("process identity inspection is unavailable on this platform")
}
