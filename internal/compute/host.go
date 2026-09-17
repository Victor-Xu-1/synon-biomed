package compute

import (
	"os"
	"os/user"
	"strings"
)

type LocalHostInfo struct {
	HostLabel  string `json:"hostLabel"`
	HostDetail string `json:"hostDetail"`
}

func DetectLocalHostInfo() LocalHostInfo {
	label := "This machine"
	if hostname, err := os.Hostname(); err == nil && strings.TrimSpace(hostname) != "" {
		label = strings.TrimSpace(hostname)
	}
	detail := ""
	if current, err := user.Current(); err == nil {
		detail = strings.TrimSpace(current.Username)
	}
	return LocalHostInfo{HostLabel: label, HostDetail: detail}
}
