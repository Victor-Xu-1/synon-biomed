//go:build linux

package kernel

import (
	"os/exec"
	"testing"
)

func TestWorkerResourceDomainRejectsUnownedFilesystemBeforeLaunch(t *testing.T) {
	command := exec.Command("/bin/true")
	if _, err := startWorkerProcess(command, t.TempDir()); err == nil {
		t.Fatal("regular directory accepted as cgroup")
	}
	if command.Process != nil {
		t.Fatal("invalid resource domain still launched process")
	}
	if _, err := startWorkerProcess(exec.Command("/bin/true"), "relative"); err == nil {
		t.Fatal("relative resource domain accepted")
	}
}
