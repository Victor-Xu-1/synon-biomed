package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestHostAccessHTTPAPIUsesDurableRealPathGrants(t *testing.T) {
	root := t.TempDir()
	granted := filepath.Join(t.TempDir(), "granted")
	if err := os.MkdirAll(filepath.Join(granted, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(granted, "note.txt"), []byte("secret content"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := New(Options{FileRoot: root, Workspace: store, HostDirectoryPicker: func(_ context.Context) (string, error) {
		return granted, nil
	}})
	app := server.Handler()
	for _, key := range []string{hostGrantsSettingKey, hostGrantsSettingKey + ".0123456789abcdef"} {
		if _, err := server.executeSettingsTool("settings_set", map[string]any{"key": key, "value": []any{}}); err == nil || !strings.Contains(err.Error(), "host access API") {
			t.Fatalf("settings_set host grant bypass key=%q err=%v", key, err)
		}
	}

	created := postProjectControlJSON(t, app, http.MethodPost, "/api/go/preferences/host-grants", map[string]any{
		"path": granted, "mode": "read",
	}, http.StatusOK)
	grant := created["grant"].(map[string]any)
	if grant["path"] != granted || grant["mode"] != "read" {
		t.Fatalf("created grant = %#v", grant)
	}
	picked := postProjectControlJSON(t, app, http.MethodPost, "/api/go/preferences/host-grants/picker", map[string]any{
		"mode": "read",
	}, http.StatusOK)
	if picked["grant"].(map[string]any)["path"] != granted {
		t.Fatalf("picked grant = %#v", picked)
	}
	foreign := runtimeCompatJSON(t, app, http.MethodGet, "/api/go/preferences/host-grants", "user-2", nil, http.StatusOK)
	if len(foreign["grants"].([]any)) != 0 {
		t.Fatalf("foreign grants = %#v", foreign)
	}

	listed := getProjectControlJSON(t, app, "/api/go/preferences/host-grants", http.StatusOK)
	if len(listed["grants"].([]any)) != 1 {
		t.Fatalf("grants = %#v", listed)
	}
	updated := postProjectControlJSON(t, app, http.MethodPatch, "/api/go/preferences/host-grants", map[string]any{
		"path": granted, "mode": "read_write",
	}, http.StatusOK)
	if updated["grant"].(map[string]any)["mode"] != "read_write" {
		t.Fatalf("updated grant = %#v", updated)
	}

	browse := getProjectControlJSON(t, app, "/api/go/preferences/host-browse?path="+granted, http.StatusOK)
	entries := browse["entries"].([]any)
	if len(entries) != 2 || entries[0].(map[string]any)["name"] != "child" ||
		entries[1].(map[string]any)["name"] != "note.txt" {
		t.Fatalf("browse entries = %#v", entries)
	}
	if _, leaked := entries[1].(map[string]any)["content"]; leaked {
		t.Fatalf("host browse leaked file content: %#v", entries[1])
	}
	getProjectControlJSON(t, app, "/api/go/preferences/host-browse?path="+filepath.Dir(root), http.StatusForbidden)
	home := getProjectControlJSON(t, app, "/api/go/preferences/host-home", http.StatusOK)
	if home["path"] == "" {
		t.Fatalf("host home = %#v", home)
	}

	if err := os.RemoveAll(granted); err != nil {
		t.Fatal(err)
	}
	postProjectControlJSON(t, app, http.MethodDelete, "/api/go/preferences/host-grants", map[string]any{
		"path": granted,
	}, http.StatusOK)
	listed = getProjectControlJSON(t, app, "/api/go/preferences/host-grants", http.StatusOK)
	if len(listed["grants"].([]any)) != 0 {
		t.Fatalf("grants after revoke = %#v", listed)
	}
	realtime := runtimeCompatJSON(t, app, http.MethodGet, "/api/events?type=host_access_granted&limit=100", "local", nil, http.StatusOK)
	if events := realtime["events"].([]any); len(events) != 4 {
		t.Fatalf("host grant realtime events=%#v", events)
	}

	restarted := New(Options{FileRoot: root}).Handler()
	listed = getProjectControlJSON(t, restarted, "/api/go/preferences/host-grants", http.StatusOK)
	if len(listed["grants"].([]any)) != 0 {
		t.Fatalf("restarted grants = %#v", listed)
	}
}

func TestWindowsPathToWSLUsesInstalledConverter(t *testing.T) {
	if !isWSLRuntime() {
		t.Skip("requires WSL")
	}
	converted, err := windowsPathToWSL(context.Background(), `C:\Windows`)
	if err != nil {
		t.Fatal(err)
	}
	if converted != "/mnt/c/Windows" {
		t.Fatalf("converted path = %q", converted)
	}
	if info, err := os.Stat(converted); err != nil || !info.IsDir() {
		t.Fatalf("converted directory is not real: info=%v err=%v", info, err)
	}
}

func TestHostGrantProtectedDataAndRollbackAuthority(t *testing.T) {
	root := t.TempDir()
	server := New(Options{FileRoot: root})
	protectedChild := filepath.Join(root, "sessions")
	if err := os.MkdirAll(protectedChild, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, protectedChild, filepath.Dir(root)} {
		if _, err := server.upsertHostGrant("owner", path, "read_write"); err == nil || !strings.Contains(err.Error(), "protected application data") {
			t.Fatalf("protected host grant path=%q err=%v", path, err)
		}
	}
	external := t.TempDir()
	alias := filepath.Join(t.TempDir(), "external-alias")
	if err := os.Symlink(external, alias); err != nil {
		t.Fatal(err)
	}
	aliasedGrant, err := server.upsertHostGrant("alias-owner", alias, "read")
	if err != nil || aliasedGrant.Path != external || aliasedGrant.ID != external {
		t.Fatalf("canonical aliased grant=%#v err=%v", aliasedGrant, err)
	}
	if _, err := server.upsertHostGrant("alias-owner", "relative/path", "read"); err == nil {
		t.Fatal("relative host grant was accepted")
	}
	if _, err := server.upsertHostGrant("owner", external, "read"); err != nil {
		t.Fatal(err)
	}
	_, receipt, err := server.upsertHostGrantWithReceipt("owner", external, "read_write")
	if err != nil || !receipt.Changed || receipt.Before == nil {
		t.Fatalf("temporary grant receipt=%#v err=%v", receipt, err)
	}
	if err := server.rollbackHostGrantMutation(receipt); err != nil {
		t.Fatal(err)
	}
	grants, err := server.loadHostGrants("owner")
	if err != nil || len(grants) != 1 || grants[0].Mode != "read" {
		t.Fatalf("restored grants=%#v err=%v", grants, err)
	}
	_, receipt, err = server.upsertHostGrantWithReceipt("owner", external, "read_write")
	if err != nil {
		t.Fatal(err)
	}
	if removed, err := server.revokeHostGrant("owner", external); err != nil || !removed {
		t.Fatalf("concurrent revoke removed=%t err=%v", removed, err)
	}
	if err := server.rollbackHostGrantMutation(receipt); err == nil || !strings.Contains(err.Error(), "conflicts with current authority") {
		t.Fatalf("stale rollback err=%v", err)
	}
	grants, err = server.loadHostGrants("owner")
	if err != nil || len(grants) != 0 {
		t.Fatalf("stale rollback restored authority grants=%#v err=%v", grants, err)
	}
}
