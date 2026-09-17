package compute

import (
	"strings"
	"testing"
)

func TestDetectLocalHostInfoMatchesV11Shape(t *testing.T) {
	info := DetectLocalHostInfo()
	if strings.TrimSpace(info.HostLabel) == "" || strings.ContainsAny(info.HostLabel, "\x00\r\n") || strings.ContainsAny(info.HostDetail, "\x00\r\n") {
		t.Fatalf("host info = %#v", info)
	}
}
