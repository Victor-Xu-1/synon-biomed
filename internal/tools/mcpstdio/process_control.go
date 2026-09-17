package mcpstdio

import (
	"os/exec"

	"synon-go/internal/subprocess"
)

type mcpProcessControl struct {
	control *subprocess.Control
}

func prepareMCPProcess(command *exec.Cmd) (*mcpProcessControl, error) {
	control, err := subprocess.Prepare(command)
	if err != nil {
		return nil, err
	}
	return &mcpProcessControl{control: control}, nil
}

func (control *mcpProcessControl) attach(command *exec.Cmd) error {
	return control.control.Attach(command)
}

func (control *mcpProcessControl) kill(command *exec.Cmd) error {
	return control.control.Kill(command)
}

func (control *mcpProcessControl) close() error {
	return control.control.Close()
}
