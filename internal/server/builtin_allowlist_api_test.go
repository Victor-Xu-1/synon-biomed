package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBuiltinAllowlistHTTPAPIIsDurableAndPreservesLockedGroups(t *testing.T) {
	root := t.TempDir()
	app := New(Options{FileRoot: root}).Handler()
	initial := httptest.NewRecorder()
	app.ServeHTTP(initial, newLoopbackTestRequest(http.MethodGet, "/api/preferences/builtin-allowlist", nil))
	if initial.Code != http.StatusOK || !bytes.Contains(initial.Body.Bytes(), []byte("Package management")) || !bytes.Contains(initial.Body.Bytes(), []byte("activeKernelCount")) {
		t.Fatalf("initial allowlist = %d: %s", initial.Code, initial.Body.String())
	}
	var initialPayload map[string]any
	if err := json.Unmarshal(initial.Body.Bytes(), &initialPayload); err != nil {
		t.Fatal(err)
	}
	if disabled, ok := initialPayload["disabled"].([]any); !ok || len(disabled) != 0 {
		t.Fatalf("initial disabled selection must be []: %#v", initialPayload["disabled"])
	}
	if groups, ok := initialPayload["disabledGroups"].([]any); !ok || len(groups) != 0 {
		t.Fatalf("initial disabled groups must be []: %#v", initialPayload["disabledGroups"])
	}
	serveWorkspaceJSON(t, app, http.MethodPut, "/api/preferences/builtin-allowlist/disabled", map[string]any{
		"disabled": []string{"pypi.org", "*.NCBI.NLM.NIH.GOV", "*.ncbi.nlm.nih.gov"},
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPut, "/api/preferences/builtin-allowlist/disabled-groups", map[string]any{
		"disabledGroups": []string{"pkg", "nih", "nih"},
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/preferences/builtin-allowlist/onboarding-seen", map[string]any{}, http.StatusOK)

	restarted := New(Options{FileRoot: root}).Handler()
	response := httptest.NewRecorder()
	restarted.ServeHTTP(response, newLoopbackTestRequest(http.MethodGet, "/api/go/preferences/builtin-allowlist", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("durable allowlist = %d: %s", response.Code, response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["hasSeenOnboarding"] != true {
		t.Fatalf("onboarding state = %#v", payload)
	}
	domains, _ := payload["effectiveDomains"].([]any)
	hasPackage, hasNIH := false, false
	for _, domain := range domains {
		if domain == "pypi.org" {
			hasPackage = true
		}
		if domain == "*.ncbi.nlm.nih.gov" {
			hasNIH = true
		}
	}
	if !hasPackage || hasNIH {
		t.Fatalf("effective domain rules = %#v", domains)
	}
}
