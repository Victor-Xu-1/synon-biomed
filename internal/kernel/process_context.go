package kernel

import (
	"context"
	"os/exec"
	"sync"
	"time"
)

// workerProcessWaitDelay gives a cancelled process tree a bounded window to
// close inherited pipes before exec.Cmd.Wait returns. The operation context,
// not this cleanup window, owns the scientific command's lifetime.
const workerProcessWaitDelay = 2 * time.Second

// newWorkerProcessCommand makes context cancellation use the same process-tree
// termination authority as explicit worker shutdown. exec.CommandContext's
// default Cancel only kills the direct child, which can leave a micromamba,
// shell, or helper descendant holding stdout/stderr open and make Wait block.
func newWorkerProcessCommand(ctx context.Context, executable string, arguments ...string) *exec.Cmd {
	if ctx == nil {
		ctx = context.Background()
	}
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Cancel = func() error {
		return killWorkerProcess(command)
	}
	command.WaitDelay = workerProcessWaitDelay
	return command
}

// Bind before Start launches exec.Cmd's context watcher. The watcher must not
// read a half-published process identity, and Cancel must never be rewritten
// after Start. Non-context commands must retain a nil Cancel to remain valid.
func bindWorkerProcessCancellation(command *exec.Cmd, cancel func() error) func() {
	ready := make(chan struct{})
	var once sync.Once
	if command.Cancel != nil {
		command.Cancel = func() error { <-ready; return cancel() }
	}
	return func() { once.Do(func() { close(ready) }) }
}
