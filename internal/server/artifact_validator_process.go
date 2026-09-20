package server

import (
	"context"
	"io"
	"os/exec"
	"time"

	"synon-go/internal/processsupervisor"
)

type artifactValidatorActivityWriter struct {
	destination io.Writer
	watchdog    *processsupervisor.InactivityWatchdog
}

func (writer artifactValidatorActivityWriter) Write(data []byte) (int, error) {
	n, err := writer.destination.Write(data)
	if n > 0 {
		writer.watchdog.MarkActivity()
	}
	return n, err
}

// Validators parse data but never execute artifact code. Reuse the owned
// process boundary and activity watchdog; healthy CPU/IO progress must not be
// mistaken for failure merely because an artifact takes longer to validate.
func runArtifactValidationCommand(ctx context.Context, command *exec.Cmd, inactivity time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	watchdog := processsupervisor.NewInactivityWatchdog(inactivity)
	if command.Stdout != nil {
		command.Stdout = artifactValidatorActivityWriter{command.Stdout, watchdog}
	}
	if command.Stderr != nil {
		command.Stderr = artifactValidatorActivityWriter{command.Stderr, watchdog}
	}
	command.WaitDelay = 5 * time.Second
	process, err := startRunnerProcess(command)
	if err != nil {
		return err
	}
	defer process.close()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	return watchdog.Wait(ctx, command.Process.Pid, done, process.kill)
}
