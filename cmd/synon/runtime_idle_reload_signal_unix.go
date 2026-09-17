//go:build unix

package main

import (
	"os"
	"syscall"
)

func runtimeIdleReloadSignal() os.Signal {
	return syscall.SIGUSR1
}

func isRuntimeIdleReloadSignal(signal os.Signal) bool {
	return signal == syscall.SIGUSR1
}
