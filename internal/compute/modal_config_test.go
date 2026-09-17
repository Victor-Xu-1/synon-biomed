package compute

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReadModalConfigProfilesPreservesOrderAndMasksSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".modal.toml")
	content := strings.Join([]string{
		"[default]",
		"token_id = \"ak-1234567890\"",
		"token_secret = \"as-never-return-this\"",
		"active = true",
		"workspace = \"research\"",
		"",
		"[\"team profile\"] # quoted profile names are valid TOML",
		"token_id = 'ak-short'",
		"token_secret = 'as-also-secret'",
		"active = false",
		"",
		"[unrelated]",
		"color = \"blue\"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	profiles, missing, err := ReadModalConfigProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	if missing {
		t.Fatal("existing config reported missing")
	}
	if len(profiles) != 2 {
		t.Fatalf("profiles=%#v", profiles)
	}
	if profiles[0].Name != "default" || profiles[0].Workspace != "research" ||
		!profiles[0].Active || profiles[0].TokenIDMasked != "ak-1234&7890" {
		t.Fatalf("default=%#v", profiles[0])
	}
	if profiles[1].Name != "team profile" || profiles[1].Active ||
		profiles[1].TokenIDMasked != "ak-&" {
		t.Fatalf("team=%#v", profiles[1])
	}
	serialized := profiles[0].Name + profiles[0].Workspace + profiles[0].TokenIDMasked
	if strings.Contains(serialized, "as-never-return-this") {
		t.Fatal("token secret leaked through profile")
	}
}

func TestReadModalConfigProfilesMissingAndMalformedAreDistinct(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), ".modal.toml")
	profiles, missing, err := ReadModalConfigProfiles(missingPath)
	if err == nil || !missing || len(profiles) != 0 {
		t.Fatalf("missing profiles=%#v missing=%v err=%v", profiles, missing, err)
	}

	malformedPath := filepath.Join(t.TempDir(), "broken.toml")
	if err := os.WriteFile(malformedPath, []byte("[default\ntoken_id = \"secret\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	profiles, missing, err = ReadModalConfigProfiles(malformedPath)
	if err == nil || missing || len(profiles) != 0 {
		t.Fatalf("malformed profiles=%#v missing=%v err=%v", profiles, missing, err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("parse error leaks input: %v", err)
	}
}

func TestReadModalConfigProfilesRejectsOversizedAndDuplicateKeys(t *testing.T) {
	oversized := filepath.Join(t.TempDir(), "oversized.toml")
	if err := os.WriteFile(oversized, []byte(strings.Repeat(" ", MaxModalConfigBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadModalConfigProfiles(oversized); err == nil {
		t.Fatal("oversized Modal config accepted")
	}

	duplicate := filepath.Join(t.TempDir(), "duplicate.toml")
	if err := os.WriteFile(duplicate, []byte(strings.Join([]string{
		"[default]",
		"token_id = \"ak-one\"",
		"token_id = \"ak-two\"",
		"token_secret = \"as-secret\"",
	}, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadModalConfigProfiles(duplicate); err == nil {
		t.Fatal("duplicate relevant key accepted")
	}
}

func TestSelectModalConfigCredentialUsesActiveThenFirst(t *testing.T) {
	profiles := []ModalConfigProfile{
		{Name: "first", TokenID: "ak-first", TokenSecret: "as-first"},
		{Name: "active", TokenID: "ak-active", TokenSecret: "as-active", Active: true},
	}
	credential, ok := SelectModalConfigCredential(profiles)
	if !ok || credential.Name != "active" || credential.TokenSecret != "as-active" {
		t.Fatalf("credential=%#v ok=%v", credential, ok)
	}
	profiles[1].TokenSecret = ""
	credential, ok = SelectModalConfigCredential(profiles)
	if !ok || credential.Name != "first" {
		t.Fatalf("fallback credential=%#v ok=%v", credential, ok)
	}
}

func TestReadModalConfigProfilesRejectsNonTOMLValuesAndEscapes(t *testing.T) {
	for name, content := range map[string]string{
		"uppercase boolean": "[default]\ntoken_id = \"ak-good\"\ntoken_secret = \"as-good\"\nactive = TRUE\n",
		"numeric boolean":   "[default]\ntoken_id = \"ak-good\"\ntoken_secret = \"as-good\"\nactive = 1\n",
		"Go hex escape":     "[default]\ntoken_id = \"\\x61k-bad\"\ntoken_secret = \"as-good\"\n",
		"literal quote":     "[default]\ntoken_id = 'ak'x'\ntoken_secret = 'as-good'\n",
		"broken unknown":    "[default]\ntoken_id = \"ak-good\"\ntoken_secret = \"as-good\"\nextra = {\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".modal.toml")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := ReadModalConfigProfiles(path); err == nil {
				t.Fatalf("invalid TOML accepted: %s", content)
			}
		})
	}
}

func TestReadModalConfigProfilesRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.toml")
	if err := os.WriteFile(target, []byte("[default]\ntoken_id=\"ak-good\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".modal.toml")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, missing, err := ReadModalConfigProfiles(path); err == nil || missing {
		t.Fatalf("symlink profiles missing=%v err=%v", missing, err)
	}
}

func TestMaskModalTokenProducesValidUTF8(t *testing.T) {
	masked := MaskModalToken("aIbWcdefghijkl")
	if !utf8.ValidString(masked) {
		t.Fatalf("invalid UTF-8 mask %q", masked)
	}
}
