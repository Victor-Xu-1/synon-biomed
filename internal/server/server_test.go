package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"synon-go/internal/buildinfo"
	"synon-go/internal/capabilities"
)

func TestHTTPServerHealthAndCapabilities(t *testing.T) {
	srv := New(Options{
		Capabilities:     capabilities.CompactSynon(),
		SkillDirectories: []string{v11SkillsDir(t)},
	})
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	healthResp, err := http.Get(httpServer.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health error = %v", err)
	}
	defer healthResp.Body.Close()
	if healthResp.StatusCode != http.StatusOK {
		t.Fatalf("/health status = %d", healthResp.StatusCode)
	}
	var health map[string]any
	if err := json.NewDecoder(healthResp.Body).Decode(&health); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if health["status"] != "healthy" || health["name"] != "Synon Biomed" || health["version"] != buildinfo.Release().Version || health["agents_registered"] != float64(14) {
		t.Fatalf("health = %#v", health)
	}

	capResp, err := http.Get(httpServer.URL + "/api/plugins/synon/capabilities")
	if err != nil {
		t.Fatalf("GET capabilities error = %v", err)
	}
	defer capResp.Body.Close()
	if capResp.StatusCode != http.StatusOK {
		t.Fatalf("capabilities status = %d", capResp.StatusCode)
	}
	var report capabilities.Report
	if err := json.NewDecoder(capResp.Body).Decode(&report); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if !report.ContainsAction("search_web") {
		t.Fatalf("capabilities missing search_web: %#v", report)
	}
	if !report.ContainsAction("receive_feishu_event") {
		t.Fatalf("capabilities missing receive_feishu_event: %#v", report)
	}
	if !report.ContainsAction("receive_wechat_event") {
		t.Fatalf("capabilities missing receive_wechat_event: %#v", report)
	}
}
