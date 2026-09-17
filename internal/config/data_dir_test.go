package config

import (
	"os"
	"path/filepath"
	"testing"

	"synon-go/internal/datadir"
)

func TestLoadAppliesPendingDataDirectoryMigrationBeforeRuntimeOpen(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("SYNON_HOME", "")
	t.Setenv("SYNON_CONFIG", "")
	controlPath := filepath.Join(root, "control", "data-dir.json")
	t.Setenv("SYNON_DATA_DIR_CONTROL", controlPath)
	source := filepath.Join(home, ".synon-go")
	target := filepath.Join(root, "relocated")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "runtime-state.sqlite"), []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := datadir.New(controlPath).Stage(source, target, true); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HomeDir != target || cfg.DataDirSource != "pointer" || cfg.DataDirControlPath != controlPath {
		t.Fatalf("data directory config = home:%q source:%q control:%q", cfg.HomeDir, cfg.DataDirSource, cfg.DataDirControlPath)
	}
	if raw, err := os.ReadFile(filepath.Join(target, "runtime-state.sqlite")); err != nil || string(raw) != "state" {
		t.Fatalf("migrated runtime state = %q, err=%v", raw, err)
	}
}

func TestLoadUsesDataDirectoryPointerAboveExplicitSynonHome(t *testing.T) {
	root := t.TempDir()
	controlPath := filepath.Join(root, "control", "data-dir.json")
	pointerTarget := filepath.Join(root, "pointer")
	explicit := filepath.Join(root, "explicit")
	if err := os.MkdirAll(filepath.Join(root, "default"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := datadir.New(controlPath).Stage(filepath.Join(root, "default"), pointerTarget, false); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYNON_DATA_DIR_CONTROL", controlPath)
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_HOME", explicit)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HomeDir != pointerTarget || cfg.DataDirSource != "pointer" {
		t.Fatalf("pointer data directory config = home:%q source:%q", cfg.HomeDir, cfg.DataDirSource)
	}
}

func TestLoadConfiguresIndependentCondaRoots(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "runtime")
	condaHome := filepath.Join(root, "conda")
	condaEnvs := filepath.Join(root, "shared-envs")
	t.Setenv("SYNON_HOME", home)
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_CONDA_HOME", condaHome)
	t.Setenv("SYNON_CONDA_ENVS_PATH", condaEnvs)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CondaHome != condaHome || cfg.CondaEnvsPath != condaEnvs {
		t.Fatalf("Conda paths = home:%q envs:%q", cfg.CondaHome, cfg.CondaEnvsPath)
	}
}

func TestLoadRejectsRelativeCondaRoots(t *testing.T) {
	t.Setenv("SYNON_HOME", filepath.Join(t.TempDir(), "runtime"))
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_CONDA_HOME", "relative-conda")
	t.Setenv("SYNON_CONDA_ENVS_PATH", "")
	if _, err := Load(); err == nil {
		t.Fatal("relative Conda home was accepted")
	}
}

func TestLoadNormalizesAndValidatesNetworkPolicy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SYNON_HOME", home)
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_NETWORK_ALLOWED_DOMAINS", "Example.COM,*.example.org")
	t.Setenv("SYNON_NETWORK_DENIED_DOMAINS", "blocked.example.net")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Network.AllowedDomains) != 2 || cfg.Network.AllowedDomains[0] != "example.com" ||
		len(cfg.Network.DeniedDomains) != 1 || cfg.Network.DeniedDomains[0] != "blocked.example.net" {
		t.Fatalf("network policy = %#v", cfg.Network)
	}
	t.Setenv("SYNON_NETWORK_ALLOWED_DOMAINS", "api.blocked.example.net")
	t.Setenv("SYNON_NETWORK_DENIED_DOMAINS", "*.blocked.example.net")
	if _, err := Load(); err == nil {
		t.Fatal("conflicting network allow and deny policy was accepted")
	}
}

func TestLoadNormalizesNetworkTLSConfiguration(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config", "synon.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{
  "network": {
    "ca_bundle": "certificates/company.pem",
    "proxy": "http://Proxy.Example:8080",
    "mcp_x509_strict": "relaxed"
  }
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYNON_HOME", filepath.Join(root, "runtime"))
	t.Setenv("SYNON_CONFIG", configPath)
	t.Setenv("SYNON_NETWORK_CA_BUNDLE", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	wantBundle := filepath.Join(filepath.Dir(configPath), "certificates", "company.pem")
	if cfg.Network.CABundle != wantBundle || cfg.Network.Proxy != "http://proxy.example:8080" || cfg.Network.MCPX509Strict != "relaxed" {
		t.Fatalf("network TLS config = %#v", cfg.Network)
	}
}

func TestLoadRejectsInvalidNetworkTLSModeAndRelativeEnvironmentBundle(t *testing.T) {
	t.Setenv("SYNON_HOME", filepath.Join(t.TempDir(), "runtime"))
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_NETWORK_CA_BUNDLE", "relative.pem")
	if _, err := Load(); err == nil {
		t.Fatal("relative environment CA bundle was accepted")
	}

	root := t.TempDir()
	configPath := filepath.Join(root, "synon.json")
	if err := os.WriteFile(configPath, []byte(`{"network":{"mcp_x509_strict":"sometimes"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYNON_CONFIG", configPath)
	t.Setenv("SYNON_NETWORK_CA_BUNDLE", "")
	if _, err := Load(); err == nil {
		t.Fatal("invalid MCP X.509 posture mode was accepted")
	}
}

func TestLoadRejectsUnsafeNetworkProxyConfiguration(t *testing.T) {
	t.Setenv("SYNON_HOME", filepath.Join(t.TempDir(), "runtime"))
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_NETWORK_PROXY", "https://proxy.example/path")
	if _, err := Load(); err == nil {
		t.Fatal("unsupported network proxy was accepted")
	}
}

func TestLoadInheritsStandardHTTPSProxyForDetachedRuntime(t *testing.T) {
	t.Setenv("SYNON_HOME", filepath.Join(t.TempDir(), "runtime"))
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_NETWORK_PROXY", "")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:7890")
	t.Setenv("https_proxy", "")
	t.Setenv("HTTP_PROXY", "http://fallback.example:8080")
	t.Setenv("http_proxy", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Network.Proxy != "http://127.0.0.1:7890" {
		t.Fatalf("inherited network proxy = %q", cfg.Network.Proxy)
	}
}

func TestLoadProductProxyOverridesStandardEnvironment(t *testing.T) {
	t.Setenv("SYNON_HOME", filepath.Join(t.TempDir(), "runtime"))
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_NETWORK_PROXY", "http://product-proxy.example:8080")
	t.Setenv("HTTPS_PROXY", "http://standard-proxy.example:3128")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Network.Proxy != "http://product-proxy.example:8080" {
		t.Fatalf("product network proxy = %q", cfg.Network.Proxy)
	}
}
