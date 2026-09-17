package mcpdirectory

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestOptionalConnectorsStayOutOfRuntimeUntilInstalled(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store, root, nil)
	items, err := service.ListOptionalConnectors(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Status != "not-installed" || items[1].Status != "not-installed" {
		t.Fatalf("optional catalog=%#v", items)
	}
	connectors, err := service.ListUnifiedConnectors(context.Background(), "local")
	if err != nil {
		t.Fatal(err)
	}
	for _, connector := range connectors {
		if connector.ID == "bundled:renkin-local" || connector.ID == "bundled:rna-design-local" {
			t.Fatalf("uninstalled optional connector leaked into runtime: %#v", connector)
		}
	}
}

func TestOptionalConnectorStateRequiresManagedPathAndExecutable(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	installRoot := optionalConnectorInstallRoot(root, "bundled:renkin-local")
	if err := os.MkdirAll(filepath.Join(installRoot, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installRoot, "bin", executableName("renkin-mcp")), []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeOptionalInstallState(root, optionalInstallState{
		ID: "bundled:renkin-local", Status: "installed", InstallRoot: installRoot,
	}); err != nil {
		t.Fatal(err)
	}
	service := New(store, root, nil)
	items, err := service.ListOptionalConnectors(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var renkin OptionalConnector
	for _, item := range items {
		if item.ID == "bundled:renkin-local" {
			renkin = item
		}
	}
	if !renkin.Installed || renkin.Status != "installed" || renkin.InstallPath == "" {
		t.Fatalf("installed optional projection=%#v", renkin)
	}
	connectors, err := service.ListUnifiedConnectors(context.Background(), "local")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, connector := range connectors {
		if connector.ID == "bundled:renkin-local" {
			found = true
		}
	}
	if !found {
		t.Fatalf("installed RENKIN missing from runtime: %#v", connectors)
	}
	if err := writeOptionalInstallState(root, optionalInstallState{
		ID: "bundled:renkin-local", Status: "installed", InstallRoot: filepath.Join(root, "outside"),
	}); err != nil {
		t.Fatal(err)
	}
	items, err = service.ListOptionalConnectors(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ID == "bundled:renkin-local" && item.Installed {
			t.Fatalf("path escape was accepted: %#v", item)
		}
	}
}
