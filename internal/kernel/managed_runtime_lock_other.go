//go:build !linux && !darwin && !windows

package kernel

import "context"

func lockKernelFile(context.Context, string) (func(), error) {
	return nil, ErrConfinementUnavailable
}
