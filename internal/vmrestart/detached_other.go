//go:build !unix && !windows

package vmrestart

import "os/exec"

func configureDetachedProcess(_ *exec.Cmd) {}
