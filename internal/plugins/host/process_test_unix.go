//go:build unix

package host

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

func pluginTestProcessExited(pid int) bool {
	if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
		return true
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return os.IsNotExist(err)
	}
	fields := strings.Fields(string(raw))
	return len(fields) > 2 && fields[2] == "Z"
}
