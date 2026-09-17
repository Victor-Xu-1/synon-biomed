package webui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHandlerServesSPAAndAssetsWithoutOwningAPIRoutes(t *testing.T) {
	root := t.TempDir()
	writeTestAsset(t, root, "index.html", "<!doctype html><title>Synon Go</title>")
	writeTestAsset(t, root, "assets/app-12345678.js", "console.log('ok')")
	handler, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	for _, requestPath := range []string{"/", "/projects/example"} {
		request := httptest.NewRequest(http.MethodGet, requestPath, nil)
		request.Header.Set("Accept", "text/html")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Synon Go") {
			t.Fatalf("%s: status=%d body=%q", requestPath, response.Code, response.Body.String())
		}
		if got := response.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s cache control = %q", requestPath, got)
		}
		csp := response.Header().Get("Content-Security-Policy")
		if csp == "" || response.Header().Get("X-Frame-Options") != "DENY" {
			t.Fatalf("%s is missing security headers", requestPath)
		}
		if !strings.Contains(csp, "frame-src 'self' http://mcp-app.localhost:* https://mcp-app.localhost:*;") {
			t.Fatalf("%s does not allow only the dedicated MCP App sandbox origin: %q", requestPath, csp)
		}
		if strings.Contains(csp, "frame-src *") || strings.Contains(csp, "frame-src https:") {
			t.Fatalf("%s has an overbroad frame source: %q", requestPath, csp)
		}
	}

	assetRequest := httptest.NewRequest(http.MethodGet, "/assets/app-12345678.js", nil)
	assetResponse := httptest.NewRecorder()
	handler.ServeHTTP(assetResponse, assetRequest)
	if assetResponse.Code != http.StatusOK || assetResponse.Body.String() != "console.log('ok')" {
		t.Fatalf("asset: status=%d body=%q", assetResponse.Code, assetResponse.Body.String())
	}
	if got := assetResponse.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("asset cache control = %q", got)
	}

	for _, requestPath := range []string{"/api/missing", "/ws/missing", "/assets/missing.js"} {
		request := httptest.NewRequest(http.MethodGet, requestPath, nil)
		request.Header.Set("Accept", "text/html")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "Synon Go") {
			t.Fatalf("%s leaked SPA fallback: status=%d body=%q", requestPath, response.Code, response.Body.String())
		}
	}
}

func TestHandlerConfinesRDKitUnsafeEvalToTheDedicatedWorker(t *testing.T) {
	root := t.TempDir()
	writeTestAsset(t, root, "index.html", "<!doctype html><title>Synon Biomed</title>")
	writeTestAsset(t, root, "rdkit/rdkit-worker.js", "self.postMessage('ready')")
	writeTestAsset(t, root, "rdkit/RDKit_minimal.js", "self.initRDKitModule = () => ({})")
	writeTestAsset(t, root, "rdkit/RDKit_minimal.wasm", "wasm")
	handler, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	pageRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	pageResponse := httptest.NewRecorder()
	handler.ServeHTTP(pageResponse, pageRequest)
	const pagePolicy = "default-src 'self'; base-uri 'self'; connect-src 'self' ws: wss:; font-src 'self' data:; form-action 'self'; frame-ancestors 'none'; frame-src 'self' http://mcp-app.localhost:* https://mcp-app.localhost:*; img-src 'self' data: blob:; object-src 'none'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; worker-src 'self' blob:;"
	if got := pageResponse.Header().Get("Content-Security-Policy"); got != pagePolicy || strings.Contains(got, "'unsafe-eval'") {
		t.Fatalf("page CSP = %q, want exact no-eval policy %q", got, pagePolicy)
	}

	workerRequest := httptest.NewRequest(http.MethodGet, "/rdkit/rdkit-worker.js", nil)
	workerResponse := httptest.NewRecorder()
	handler.ServeHTTP(workerResponse, workerRequest)
	const workerPolicy = "default-src 'none'; script-src 'self' 'unsafe-eval' 'wasm-unsafe-eval'; connect-src 'self';"
	if workerResponse.Code != http.StatusOK || workerResponse.Body.String() != "self.postMessage('ready')" {
		t.Fatalf("worker response status=%d body=%q", workerResponse.Code, workerResponse.Body.String())
	}
	if got := workerResponse.Header().Get("Content-Security-Policy"); got != workerPolicy {
		t.Fatalf("worker CSP = %q, want exact isolated policy %q", got, workerPolicy)
	}
	if got := workerResponse.Header().Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
		t.Fatalf("worker CORP = %q", got)
	}
	for _, requestPath := range []string{
		"/rdkit/rdkit-worker.js",
		"/rdkit/RDKit_minimal.js",
		"/rdkit/RDKit_minimal.wasm",
	} {
		request := httptest.NewRequest(http.MethodGet, requestPath, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d", requestPath, response.Code)
		}
		if got := response.Header().Get("Cache-Control"); got != "no-cache, must-revalidate" {
			t.Fatalf("%s cache control = %q", requestPath, got)
		}
	}
}

func TestHandlerRevalidatesThemeBootstrapWithoutRelaxingPageCSP(t *testing.T) {
	root := t.TempDir()
	writeTestAsset(t, root, "index.html", `<script src="/theme-init.js"></script><main>Synon Biomed</main>`)
	writeTestAsset(t, root, themeInitPath, "document.documentElement.dataset.theme = 'dark'")
	handler, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/theme-init.js", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("theme bootstrap status = %d", response.Code)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-cache, must-revalidate" {
		t.Fatalf("theme bootstrap cache control = %q", got)
	}
	if got := response.Header().Get("Content-Security-Policy"); got != pageContentSecurityPolicy || strings.Contains(got, "script-src 'self' 'wasm-unsafe-eval' 'unsafe-inline'") {
		t.Fatalf("theme bootstrap CSP = %q", got)
	}
}

func TestHandlerRejectsUnsafePathsMethodsAndSymlinks(t *testing.T) {
	root := t.TempDir()
	writeTestAsset(t, root, "index.html", "index")
	handler, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	for _, requestPath := range []string{"/../secret", "/.git/config", `/assets\\secret.js`} {
		request := httptest.NewRequest(http.MethodGet, requestPath, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d", requestPath, response.Code)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x"))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST status=%d allow=%q", response.Code, response.Header().Get("Allow"))
	}

	if runtime.GOOS != "windows" {
		outside := filepath.Join(t.TempDir(), "outside.js")
		if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "escape.js")); err != nil {
			t.Fatal(err)
		}
		if _, err := New(root); err == nil || !strings.Contains(err.Error(), "must not contain symlinks") {
			t.Fatalf("New() symlink error = %v", err)
		}
	}
}

func TestNewRequiresRegularIndex(t *testing.T) {
	if _, err := New(t.TempDir()); err == nil || !strings.Contains(err.Error(), "stat web index") {
		t.Fatalf("New() missing index error = %v", err)
	}
}

func writeTestAsset(t *testing.T, root, relative, content string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
