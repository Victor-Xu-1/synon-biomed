package kernel

import (
	"errors"
	"path/filepath"
)

// Only the executor configures this before creating its physical worker. The
// directory is an OS-owned resource boundary, not a task/model input.
func (m *Manager) ConfigureWorkerResourceDomain(directory string) error {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return errors.New("invalid worker resource domain")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.workers) != 0 {
		return errors.New("cannot change an active worker resource domain")
	}
	m.config.WorkerResourceDirectory = directory
	return nil
}
