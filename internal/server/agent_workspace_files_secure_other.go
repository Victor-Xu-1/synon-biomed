//go:build !linux && !windows

package server

import (
	"errors"
	"os"
)

type agentWorkspaceEditAuthority struct{}
type agentWorkspaceStagingFile struct{ file *os.File }

func openAgentWorkspaceRegularFile(string, string) (*os.File, error) {
	return nil, errors.New("secure workspace file reads are unavailable on this platform")
}

func openAgentWorkspaceEditAuthority(string, string, bool) (*agentWorkspaceEditAuthority, error) {
	return nil, errors.New("secure workspace file edits are unavailable on this platform")
}

func (*agentWorkspaceEditAuthority) openCurrent() (*os.File, os.FileMode, bool, error) {
	return nil, 0, false, errors.New("secure workspace file edits are unavailable on this platform")
}

func (*agentWorkspaceEditAuthority) createStaging(os.FileMode) (*agentWorkspaceStagingFile, error) {
	return nil, errors.New("secure workspace file edits are unavailable on this platform")
}

func (*agentWorkspaceEditAuthority) commit(*agentWorkspaceStagingFile) error {
	return errors.New("secure workspace file edits are unavailable on this platform")
}

func (*agentWorkspaceEditAuthority) actualPath() (string, error) {
	return "", errors.New("secure workspace file edits are unavailable on this platform")
}

func (*agentWorkspaceEditAuthority) close() error                       { return nil }
func (*agentWorkspaceStagingFile) discard(*agentWorkspaceEditAuthority) {}

func secureEnsureAgentWorkspaceDirectory(string, os.FileMode) (string, error) {
	return "", errors.New("secure workspace directory creation is unavailable on this platform")
}
