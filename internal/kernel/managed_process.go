package kernel

import "os/exec"

// ManagedProcess exposes the existing process-tree owner to command adapters.
// Adapters must not install their own parent-only cancellation implementation.
type ManagedProcess struct{ process *workerProcess }

func StartManagedProcess(command *exec.Cmd) (*ManagedProcess, error) {
	process, err := startWorkerProcess(command)
	if err != nil {
		return nil, err
	}
	return &ManagedProcess{process: process}, nil
}

func (p *ManagedProcess) Terminate() error { return p.process.kill() }
func (p *ManagedProcess) Close()           { p.process.close() }
