package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"synon-go/internal/webui"
)

func TestServerRoutesAPIBeforeWorkbenchSPA(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<title>workbench</title>"), 0o600); err != nil {
		t.Fatal(err)
	}
	webHandler, err := webui.New(root)
	if err != nil {
		t.Fatal(err)
	}
	app := newV11TestServer(t, Options{WebUI: webHandler})

	health := httptest.NewRequest(http.MethodGet, "/health", nil)
	healthResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(healthResponse, health)
	if strings.Contains(healthResponse.Body.String(), "workbench") {
		t.Fatalf("health was handled by SPA: %s", healthResponse.Body.String())
	}
	apiHealth := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	apiHealthResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(apiHealthResponse, apiHealth)
	if apiHealthResponse.Code != http.StatusOK ||
		!strings.HasPrefix(apiHealthResponse.Header().Get("Content-Type"), "application/json") ||
		strings.Contains(apiHealthResponse.Body.String(), "workbench") {
		t.Fatalf("API health status=%d content-type=%q body=%s", apiHealthResponse.Code,
			apiHealthResponse.Header().Get("Content-Type"), apiHealthResponse.Body.String())
	}

	spa := httptest.NewRequest(http.MethodGet, "/projects/example", nil)
	spa.Header.Set("Accept", "text/html")
	spaResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(spaResponse, spa)
	if spaResponse.Code != http.StatusOK || !strings.Contains(spaResponse.Body.String(), "workbench") {
		t.Fatalf("SPA status=%d body=%s", spaResponse.Code, spaResponse.Body.String())
	}

	missingAPI := httptest.NewRequest(http.MethodGet, "/api/not-implemented", nil)
	missingAPI.Header.Set("Accept", "text/html")
	missingAPIResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(missingAPIResponse, missingAPI)
	if missingAPIResponse.Code != http.StatusUnauthorized || strings.Contains(missingAPIResponse.Body.String(), "workbench") {
		t.Fatalf("missing API status=%d body=%s", missingAPIResponse.Code, missingAPIResponse.Body.String())
	}
}
