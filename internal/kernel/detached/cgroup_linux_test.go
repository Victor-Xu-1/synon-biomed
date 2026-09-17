//go:build linux

package detached

import "testing"

func TestParseUnifiedCgroupPathRequiresExactV2Identity(t *testing.T) {
	got, err := parseUnifiedCgroupPath("0::/user.slice/user-1000.slice/app.slice/synon-kernel.service\n")
	if err != nil || got != "/user.slice/user-1000.slice/app.slice/synon-kernel.service" {
		t.Fatalf("cgroup path=%q err=%v", got, err)
	}
	for _, input := range []string{
		"2:cpu:/legacy\n",
		"0::relative\n",
		"0::/user.slice/../escape\n",
		"0::/bad\x00path\n",
	} {
		if _, err := parseUnifiedCgroupPath(input); err == nil {
			t.Fatalf("invalid cgroup identity %q was accepted", input)
		}
	}
}
