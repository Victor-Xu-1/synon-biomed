package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestThirdPartyLicenseInventoryIsEmbeddedAndVerifiable(t *testing.T) {
	app := New(Options{}).Handler()
	request := httptest.NewRequest(http.MethodGet, "/api/synonbiomed/licenses/third-party", nil)
	request.Header.Set("X-Synon-User-Id", "local")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Title   string `json:"title"`
		Source  string `json:"source"`
		Bytes   int    `json:"bytes"`
		SHA256  string `json:"sha256"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Title == "" || payload.Source != thirdPartyLicenseSource {
		t.Fatalf("license identity = %#v", payload)
	}
	if payload.Bytes != len(payload.Content) || payload.Bytes < 60_000 {
		t.Fatalf("license bytes = %d content bytes = %d", payload.Bytes, len(payload.Content))
	}
	if payload.SHA256 != "a20ea383f5ccb559baea01120e886d9337371f7c4b0606156f3ba76d82156b4a" {
		t.Fatalf("license sha256 = %q", payload.SHA256)
	}
	if !strings.Contains(payload.Content, "### Ketcher") ||
		!strings.Contains(payload.Content, "### GNU General Public License v3.0") {
		t.Fatal("embedded license inventory is incomplete")
	}
	if !strings.Contains(payload.Content, "6d495a3849e60c89b192a6b66894ed935fa49cdd") ||
		!strings.Contains(payload.Content, "docs/licenses/biomart-mcp/{LICENSE,NOTICE.md}") ||
		!strings.Contains(payload.Content, "Copyright (c) 2025 John Zinno") {
		t.Fatal("scoped BioMart source attribution is incomplete")
	}
	if !strings.Contains(payload.Content, "0e67a612e4045f007e38fa77adc8f3ebfc5616b6") ||
		!strings.Contains(payload.Content, "docs/licenses/bionemo-agent-toolkit/") {
		t.Fatal("shared scientific Skill attribution is incomplete")
	}
}

func TestThirdPartyLicenseInventoryRejectsMutations(t *testing.T) {
	app := New(Options{}).Handler()
	request := httptest.NewRequest(http.MethodPost, "/api/synonbiomed/licenses/third-party", strings.NewReader(`{}`))
	request.Header.Set("X-Synon-User-Id", "local")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("status = %d allow = %q", response.Code, response.Header().Get("Allow"))
	}
}
