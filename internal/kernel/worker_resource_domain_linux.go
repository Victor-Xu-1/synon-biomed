//go:build linux

package kernel

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func configureWorkerResourceDomain(command *exec.Cmd, directories []string) (func(), error) {
	if len(directories) == 0 || len(directories) == 1 && directories[0] == "" {
		return func() {}, nil
	}
	if len(directories) != 1 || !filepath.IsAbs(directories[0]) {
		return nil, errors.New("invalid worker resource domain")
	}
	file, err := os.Open(directories[0])
	if err != nil {
		return nil, err
	}
	var stat syscall.Statfs_t
	info, statErr := file.Stat()
	if err = syscall.Fstatfs(int(file.Fd()), &stat); err != nil || stat.Type != 0x63677270 || statErr != nil || !info.IsDir() {
		file.Close()
		return nil, errors.New("worker resource domain is not a cgroup v2 directory")
	}
	command.SysProcAttr.UseCgroupFD = true
	command.SysProcAttr.CgroupFD = int(file.Fd())
	return func() { _ = file.Close() }, nil
}
