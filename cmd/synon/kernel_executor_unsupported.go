//go:build !linux

package main

import "errors"

func runKernelExecutorCLI(_ []string) error {
	return errors.New("detached kernel executor requires Linux")
}
