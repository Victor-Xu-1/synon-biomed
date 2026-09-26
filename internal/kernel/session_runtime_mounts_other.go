//go:build !windows

package kernel

func platformSessionRuntimeMounts(string, string, string, ...string) ([]WorkerMount, error) {
	return nil, nil
}
