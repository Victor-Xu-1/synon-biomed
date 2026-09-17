package compute

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareSSHHostUsesRealRestrictedConfigAndIdentity(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(sshDir, "config")
	if err := os.WriteFile(config, []byte("Host cluster-*\n  User scientist\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(sshDir, "id_fixture")
	if err := os.WriteFile(identity, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareSSHHost(config, home, SSHHostRequest{
		Alias: "cluster-a", InitialContext: "  approved context  ",
		DataRoots: []string{"/data/a", "relative", " /data/b/../c "},
		Overrides: SSHHostOverrides{User: "researcher", Port: 2222, IdentityFile: "~/.ssh/id_fixture"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Alias != "cluster-a" || prepared.InitialContext != "approved context" ||
		len(prepared.DataRoots) != 2 || prepared.DataRoots[1] != "/data/c" ||
		prepared.Overrides["identityFile"] != identity || prepared.Overrides["port"] != 2222 {
		t.Fatalf("prepared=%#v", prepared)
	}
}

func TestPrepareSSHHostRejectsUnknownAliasAndUnsafeIdentity(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(sshDir, "config")
	if err := os.WriteFile(config, []byte("Host known\nHost !cluster-denied cluster-*\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareSSHHost(config, home, SSHHostRequest{Alias: "unknown"}); err == nil {
		t.Fatal("unknown alias unexpectedly accepted")
	}
	outside := filepath.Join(t.TempDir(), "id_outside")
	if _, err := PrepareSSHHost(config, home, SSHHostRequest{Alias: "cluster-denied"}); err == nil {
		t.Fatal("negated SSH alias unexpectedly accepted")
	}
	if err := os.WriteFile(outside, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareSSHHost(config, home, SSHHostRequest{
		Alias: "known", Overrides: SSHHostOverrides{IdentityFile: outside},
	}); err == nil {
		t.Fatal("identity outside approved roots unexpectedly accepted")
	}
}
