package kernel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestInstallerNetworkRouteIsExplicitAndRejectsAmbientOverrides(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://ambient.example.test:8080")
	t.Setenv("NO_PROXY", "*")
	initial := []string{"PATH=/usr/bin", "HTTPS_PROXY=http://old.example.test", "no_proxy=*", "ALL_PROXY=socks5://old.example.test"}
	direct, err := managedEnvironmentInstallerNetworkEnv(initial, "")
	if err != nil || !reflect.DeepEqual(direct, []string{"PATH=/usr/bin"}) {
		t.Fatalf("direct route=%v error=%v", direct, err)
	}
	proxied, err := managedEnvironmentInstallerNetworkEnv(initial, "http://selected.example.test:8080")
	if err != nil {
		t.Fatal(err)
	}
	if len(proxied) != 5 || strings.Contains(strings.Join(proxied, "\n"), "ambient") || strings.Contains(strings.Join(proxied, "\n"), "old.example") {
		t.Fatalf("competing route escaped: %v", proxied)
	}
	for _, bad := range []string{"http://user:secret@example.test", "http://example.test/route", "http://example.test\nHTTP_PROXY=bad"} {
		if _, err := managedEnvironmentInstallerNetworkEnv(initial, bad); err == nil {
			t.Fatal("invalid route accepted")
		}
	}
}

func TestInstallerChildUsesConfiguredNetworkRoute(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("real Python child required for installer route regression")
	}
	var requests atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host != "installer-source.example.test" || r.URL.Path != "/manifest" {
			t.Errorf("wrong installer destination: %s", r.URL.Redacted())
			http.Error(w, "wrong destination", http.StatusBadRequest)
			return
		}
		requests.Add(1)
		_, _ = w.Write([]byte("verified-route"))
	}))
	defer proxy.Close()
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "*")
	manager := NewManager(Config{UpstreamProxy: proxy.URL, CondaHome: t.TempDir(), ManagedEnvironmentInstallerInactivityTimeout: 5 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	code := "import urllib.request\nassert urllib.request.urlopen('http://installer-source.example.test/manifest',timeout=3).read()==b'verified-route'\nprint('installer-route-ok')"
	if err := manager.runManagedEnvironmentProcess(ctx, python, "-I", "-c", code); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("actual proxy requests=%d", requests.Load())
	}
	// Invalid configuration fails before a subprocess or any network request.
	manager.config.UpstreamProxy = "http://bad.example.test/path"
	if err := manager.runManagedEnvironmentProcess(ctx, python, "-I", "-c", code); err == nil || requests.Load() != 1 {
		t.Fatalf("invalid route was executed: requests=%d error=%v", requests.Load(), err)
	}
}
