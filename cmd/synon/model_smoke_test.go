package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestModelSmokePlanIsOfflineAndRedacted(t *testing.T) {
	configPath := writeModelSmokeConfig(t, `{
  "runner": {"enabled": true, "provider": "go_builtin"}
}`)
	var output bytes.Buffer
	if err := runModelSmokeCLI(t.Context(), []string{"--config", configPath, "--plan", "--require-all", "--json"}, &output); err != nil {
		t.Fatalf("runModelSmokeCLI() error = %v", err)
	}
	var report modelSmokeReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "planned" || report.Mode != "plan" || !report.SecretsRedacted || len(report.Targets) != 2 {
		t.Fatalf("report = %#v", report)
	}
	if report.Targets[0].Name != "runner" || report.Targets[0].Status != "planned" || report.Targets[0].RequiresNetwork {
		t.Fatalf("runner target = %#v", report.Targets[0])
	}
	if report.Targets[1].Name != "compact" || report.Targets[1].Status != "skipped_missing_config" {
		t.Fatalf("compact target = %#v", report.Targets[1])
	}
}

func TestModelSmokeRunUsesRealOpenAIProtocolWithoutLeakingSecrets(t *testing.T) {
	const secret = "model-smoke-secret"
	const responseText = "private response body"
	var requests atomic.Int64
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if requestNumber == 1 {
			http.Error(w, "private transient provider error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"` + responseText + `"}}]}`))
	}))
	defer model.Close()
	configPath := writeModelSmokeConfig(t, `{
  "runner": {
    "enabled": true,
    "provider": "openai_chat",
    "chat_endpoint": "`+model.URL+`/v1/chat/completions",
    "chat_api_key": "`+secret+`",
    "chat_model": "smoke-model"
  },
  "compact_summarizer": {
    "endpoint": "`+model.URL+`/v1/chat/completions",
    "api_key": "`+secret+`",
    "model": "compact-model"
  }
}`)
	var output bytes.Buffer
	if err := runModelSmokeCLI(t.Context(), []string{"--config", configPath, "--run", "--require-all", "--max-attempts", "2", "--json"}, &output); err != nil {
		t.Fatalf("runModelSmokeCLI() error = %v; output=%s", err, output.String())
	}
	if requests.Load() != 3 {
		t.Fatalf("requests = %d", requests.Load())
	}
	if strings.Contains(output.String(), secret) || strings.Contains(output.String(), model.URL) || strings.Contains(output.String(), responseText) {
		t.Fatalf("model smoke output leaked private data: %s", output.String())
	}
	var report modelSmokeReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "passed" || len(report.Targets) != 2 {
		t.Fatalf("report = %#v", report)
	}
	for _, target := range report.Targets {
		if target.Status != "pass" || !target.LiveChecked || !target.ResponseNonEmpty || !target.RequiresNetwork || !target.CredentialConfigured {
			t.Fatalf("target = %#v", target)
		}
	}
}

func TestModelSmokeRunRejectsInvalidEndpointAndRequiredMissingTarget(t *testing.T) {
	invalidConfig := writeModelSmokeConfig(t, `{
  "compact_summarizer": {"endpoint": "file:///tmp/model", "model": "compact-model"}
}`)
	var invalidOutput bytes.Buffer
	err := runModelSmokeCLI(t.Context(), []string{"--config", invalidConfig, "--target", "compact", "--run", "--require-all"}, &invalidOutput)
	if err == nil || !strings.Contains(err.Error(), "did not pass") {
		t.Fatalf("invalid endpoint error = %v", err)
	}
	if strings.Contains(invalidOutput.String(), "file:///tmp/model") || !strings.Contains(invalidOutput.String(), "invalid_endpoint") {
		t.Fatalf("invalid endpoint output = %s", invalidOutput.String())
	}

	missingConfig := writeModelSmokeConfig(t, `{}`)
	var missingOutput bytes.Buffer
	err = runModelSmokeCLI(t.Context(), []string{"--config", missingConfig, "--run", "--require-all"}, &missingOutput)
	if err == nil || !strings.Contains(err.Error(), "did not pass") {
		t.Fatalf("missing target error = %v", err)
	}
	if !strings.Contains(missingOutput.String(), "skipped_missing_config") {
		t.Fatalf("missing target output = %s", missingOutput.String())
	}
}

func TestModelSmokeFlagsRejectConflictingMode(t *testing.T) {
	if err := runModelSmokeCLI(t.Context(), []string{"--plan", "--run"}, &bytes.Buffer{}); err == nil {
		t.Fatal("runModelSmokeCLI() accepted --plan with --run")
	}
}

func writeModelSmokeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "synon.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
