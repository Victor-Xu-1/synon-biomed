package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
)

type runtimeRestartRequestedError struct {
	Reason string
}

func (e *runtimeRestartRequestedError) Error() string {
	return "runtime restart requested: " + e.Reason
}

func newRuntimeRestartRequester(requests chan<- string) func(string) error {
	var requested atomic.Bool
	return func(reason string) error {
		reason = strings.TrimSpace(reason)
		if reason == "" {
			return errors.New("runtime restart reason is required")
		}
		if !requested.CompareAndSwap(false, true) {
			return errors.New("a runtime restart is already pending")
		}
		select {
		case requests <- reason:
			return nil
		default:
			return errors.New("a runtime restart is already pending")
		}
	}
}

func runMain() error {
	err := run()
	var restart *runtimeRestartRequestedError
	if !errors.As(err, &restart) {
		return err
	}
	if err := launchRuntimeReplacement(); err != nil {
		return fmt.Errorf("launch replacement after %s: %w", restart.Reason, err)
	}
	return nil
}

func launchRuntimeReplacement() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	command := exec.Command(executable, os.Args[1:]...)
	command.Env = os.Environ()
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}
