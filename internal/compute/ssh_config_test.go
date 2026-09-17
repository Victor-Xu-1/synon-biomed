package compute

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverSSHConfigAliasesParsesRestrictedIncludes(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config")
	if err := os.Mkdir(filepath.Join(root, "conf.d"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("Host alpha *.wild\nInclude conf.d/*\nHost beta alpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "conf.d", "hosts"), []byte("Host gamma ?single\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := DiscoverSSHConfigAliases(config)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ConfigFound || result.ConfigPath != config || result.WildcardCount != 2 || len(result.Aliases) != 3 ||
		result.Aliases[0] != "alpha" || result.Aliases[1] != "beta" || result.Aliases[2] != "gamma" {
		t.Fatalf("result = %#v", result)
	}
}

func TestDiscoverSSHConfigAliasesRejectsIncludeEscapeAndSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-outside")
	t.Cleanup(func() { _ = os.Remove(outside) })
	if err := os.WriteFile(outside, []byte("Host leaked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "config")
	if err := os.WriteFile(config, []byte("Include ../"+filepath.Base(outside)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverSSHConfigAliases(config); err == nil {
		t.Fatal("include escape unexpectedly succeeded")
	}
	link := filepath.Join(root, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := DiscoverSSHConfigAliases(link); err == nil {
		t.Fatal("symlink config unexpectedly succeeded")
	}
}
