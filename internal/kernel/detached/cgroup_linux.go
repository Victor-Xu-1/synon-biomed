//go:build linux

package detached

import (
	"errors"
	"os"
	"path"
	"strings"
)

func currentUnifiedCgroupPath() (string, error) {
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil || len(raw) == 0 || len(raw) > 64*1024 {
		return "", errors.New("detached executor cgroup identity is unavailable")
	}
	return parseUnifiedCgroupPath(string(raw))
}

func parseUnifiedCgroupPath(raw string) (string, error) {
	for _, line := range strings.Split(raw, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), ":", 3)
		if len(parts) != 3 || parts[0] != "0" || parts[1] != "" {
			continue
		}
		value := strings.TrimSpace(parts[2])
		clean := path.Clean(value)
		if value == "" || !strings.HasPrefix(value, "/") || clean != value ||
			len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") {
			return "", errors.New("detached executor cgroup identity is invalid")
		}
		return value, nil
	}
	return "", errors.New("unified cgroup v2 identity is unavailable")
}
