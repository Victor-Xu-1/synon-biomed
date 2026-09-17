package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pluginhost "synon-go/internal/plugins/host"
)

func TestPluginsAPIListsOnlyCompactSynonPlugin(t *testing.T) {
	srv := New(Options{})
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	resp, err := http.Get(httpServer.URL + "/api/plugins")
	if err != nil {
		t.Fatalf("GET /api/plugins error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("plugins status = %d", resp.StatusCode)
	}
	var body struct {
		Plugins []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Version string `json:"version"`
			Mount   string `json:"mount"`
			Modules []struct {
				ID       string `json:"id"`
				Endpoint string `json:"endpoint"`
			} `json:"modules"`
		} `json:"plugins"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode plugins: %v", err)
	}
	if len(body.Plugins) != 1 {
		t.Fatalf("plugins = %#v", body.Plugins)
	}
	plugin := body.Plugins[0]
	if plugin.ID != "synon" || plugin.Version != "1.1.5" || plugin.Mount != "/api/plugins/synon" {
		t.Fatalf("plugin = %#v", plugin)
	}
	if !hasPluginModule(plugin.Modules, "capabilities", "/api/plugins/synon/capabilities") ||
		!hasPluginModule(plugin.Modules, "link-download", "/api/plugins/synon/link/download") {
		t.Fatalf("plugin modules = %#v", plugin.Modules)
	}
	raw, _ := json.Marshal(body)
	lower := strings.ToLower(string(raw))
	for _, removed := range []string{"admet", "markush", "knowledge", "daily", "molecular", "pcc"} {
		if strings.Contains(lower, removed) {
			t.Fatalf("removed capability leaked in plugin listing: %s", raw)
		}
	}
}

func TestPluginsAPIProvidesSynonManifestAndRejectsUnknownPlugin(t *testing.T) {
	srv := New(Options{})
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	resp, err := http.Get(httpServer.URL + "/api/plugins/synon/manifest")
	if err != nil {
		t.Fatalf("GET manifest error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manifest status = %d", resp.StatusCode)
	}
	var manifest map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest["id"] != "synon" || manifest["pluginFormat"] != "synon_agent_native" {
		t.Fatalf("manifest = %#v", manifest)
	}
	serverValue, ok := manifest["server"].(map[string]any)
	if !ok || serverValue["apiMount"] != "/api/plugins/synon" {
		t.Fatalf("manifest server = %#v", manifest["server"])
	}

	unknownResp, err := http.Get(httpServer.URL + "/api/plugins/admet/manifest")
	if err != nil {
		t.Fatalf("GET unknown manifest error = %v", err)
	}
	defer unknownResp.Body.Close()
	if unknownResp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown manifest status = %d", unknownResp.StatusCode)
	}
}

func TestPluginsAPIListsExternalPluginManifests(t *testing.T) {
	root := t.TempDir()
	manifestDir := filepath.Join(root, "terminal-tools", ".synon-plugin")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
  "id": "terminal-tools",
  "name": "terminal-tools",
  "version": "0.2.0",
  "description": "Small local terminal helper plugin",
  "author": {"name": "Plugin Author"},
  "server": {
    "api": {"mount": "/api/plugins/terminal-tools", "entry": "./server/api.go"},
    "context": {"entry": "./server/context.go"}
  }
}`
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	plugins, err := pluginhost.NewDefaultWithExternalDirectories([]string{filepath.Join(root, "terminal-tools")})
	if err != nil {
		t.Fatalf("NewDefaultWithExternalDirectories() error = %v", err)
	}
	srv := New(Options{Plugins: plugins})
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	resp, err := http.Get(httpServer.URL + "/api/plugins")
	if err != nil {
		t.Fatalf("GET /api/plugins error = %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		Plugins []struct {
			ID           string `json:"id"`
			PluginFormat string `json:"pluginFormat"`
			Mount        string `json:"mount"`
			Runtime      any    `json:"runtime"`
			Server       struct {
				APIMount string `json:"apiMount"`
			} `json:"server"`
		} `json:"plugins"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode plugins: %v", err)
	}
	if len(body.Plugins) != 2 {
		t.Fatalf("plugins = %#v", body.Plugins)
	}
	if body.Plugins[0].ID != "synon" || body.Plugins[1].ID != "terminal-tools" {
		t.Fatalf("plugins order = %#v", body.Plugins)
	}
	external := body.Plugins[1]
	if external.PluginFormat != "synon_agent_external" || external.Mount != "/api/plugins/terminal-tools" {
		t.Fatalf("external plugin = %#v", external)
	}
	if external.Server.APIMount != "/api/plugins/terminal-tools" {
		t.Fatalf("external server = %#v", external.Server)
	}

	manifestResp, err := http.Get(httpServer.URL + "/api/plugins/terminal-tools/manifest")
	if err != nil {
		t.Fatalf("GET external manifest error = %v", err)
	}
	defer manifestResp.Body.Close()
	if manifestResp.StatusCode != http.StatusOK {
		t.Fatalf("external manifest status = %d", manifestResp.StatusCode)
	}
	var externalManifest map[string]any
	if err := json.NewDecoder(manifestResp.Body).Decode(&externalManifest); err != nil {
		t.Fatalf("decode external manifest: %v", err)
	}
	if externalManifest["id"] != "terminal-tools" || externalManifest["pluginFormat"] != "synon_agent_external" {
		t.Fatalf("external manifest = %#v", externalManifest)
	}
}

func hasPluginModule(modules []struct {
	ID       string `json:"id"`
	Endpoint string `json:"endpoint"`
}, id string, endpoint string) bool {
	for _, module := range modules {
		if module.ID == id && module.Endpoint == endpoint {
			return true
		}
	}
	return false
}
