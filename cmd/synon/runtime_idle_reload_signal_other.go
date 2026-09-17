//go:build !unix

package main

import "os"

func runtimeIdleReloadSignal() os.Signal {
	return nil
}

func isRuntimeIdleReloadSignal(os.Signal) bool {
	return false
}
