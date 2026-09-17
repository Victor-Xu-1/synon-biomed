package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMCPToolCatalogV53PublishedIdentity(t *testing.T) {
	checksum, err := mcpToolCatalogV53Migration.validatedChecksum()
	if err != nil {
		t.Fatal(err)
	}
	const published = "41784eacf2533322ce46dee1668b6bd21b13b1175d613292fefd4b1fbdc1a10b"
	if checksum != published {
		t.Fatalf("published v53 checksum changed: got %s want %s", checksum, published)
	}
}

func TestMCPToolCatalogV53PersistsValidatedCatalogAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	status, err := store.SchemaStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.CurrentVersion != workspaceSchemaVersion || status.TargetVersion != workspaceSchemaVersion {
		t.Fatalf("schema status=%#v", status)
	}
	raw := []byte(`{"version":1,"tools":[{"toolName":"search_articles","inputSchema":{"type":"object"},"readOnlyHint":true}]}`)
	sum := sha256.Sum256(raw)
	catalog := MCPConnectorToolCatalog{
		UserID: "owner-a", Source: "bundled", ConnectorID: "bundled:pubmed",
		ConfigSHA256: strings.Repeat("a", 64), CatalogSHA256: hex.EncodeToString(sum[:]),
		CatalogJSON: string(raw), RefreshedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	if err := store.PutMCPConnectorToolCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	invalid := catalog
	invalid.CatalogSHA256 = strings.Repeat("0", 64)
	if err := store.PutMCPConnectorToolCatalog(invalid); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("tampered catalog error=%v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, found, err := reopened.GetMCPConnectorToolCatalog("owner-a", "bundled", "bundled:pubmed")
	if err != nil || !found {
		t.Fatalf("catalog found=%t err=%v", found, err)
	}
	if got.ConfigSHA256 != catalog.ConfigSHA256 || got.CatalogSHA256 != catalog.CatalogSHA256 || got.CatalogJSON != catalog.CatalogJSON {
		t.Fatalf("persisted catalog=%#v", got)
	}
}
