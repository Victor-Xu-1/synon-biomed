package common

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOutboundMediaLocalPathRequiresAuthorizedRootAndRegularFile(t *testing.T) {
	t.Setenv("SYNON_OUTBOUND_MEDIA_ROOTS", "")
	tempFile := filepath.Join(t.TempDir(), "result.png")
	if err := os.WriteFile(tempFile, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !IsSafeOutboundLocalPath(tempFile) {
		t.Fatalf("temporary result should be authorized: %s", tempFile)
	}
	if IsSafeOutboundLocalPath(filepath.Dir(tempFile)) {
		t.Fatal("directory was accepted as outbound media")
	}

	root := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("SYNON_OUTBOUND_MEDIA_ROOTS", root)
	} else {
		t.Setenv("SYNON_OUTBOUND_MEDIA_ROOTS", root+string(filepath.ListSeparator)+"relative-root")
	}
	authorized := filepath.Join(root, "report.pdf")
	if err := os.WriteFile(authorized, []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !IsSafeOutboundLocalPath(authorized) {
		t.Fatalf("explicit authorized result should be accepted: %s", authorized)
	}
}

func TestOutboundMediaLocalPathRejectsSensitiveAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SYNON_OUTBOUND_MEDIA_ROOTS", root)
	secret := filepath.Join(root, ".env.production")
	if err := os.WriteFile(secret, []byte("TOKEN=secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if IsSafeOutboundLocalPath(secret) {
		t.Fatal("sensitive environment file was accepted")
	}
	key := filepath.Join(root, "client.pem")
	if err := os.WriteFile(key, []byte("private key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if IsSafeOutboundLocalPath(key) {
		t.Fatal("private key file was accepted")
	}
	if runtime.GOOS == "windows" {
		return
	}
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked.png")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if IsSafeOutboundLocalPath(link) {
		t.Fatal("symlink escaping the authorized root was accepted")
	}
}
