//go:build !linux

package kernel

import (
	"errors"
	"os/exec"
)

func configureWorkerResourceDomain(_ *exec.Cmd, directories []string) (func(), error) {
	if len(directories) == 0 || len(directories) == 1 && directories[0] == "" {
		return func() {}, nil
	}
	return nil, errors.New("worker cgroup resource domain requires Linux")
}
