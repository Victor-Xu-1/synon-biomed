package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestSecretsHTTPAPINeverReturnsPlaintext(t *testing.T) {
	root := t.TempDir()
	app := New(Options{FileRoot: root}).Handler()
	createBody := map[string]any{
		"id": "secret-1", "provider": "github", "name": "release token",
		"value": "api-super-secret", "credentials": map[string]any{"token": "credential-super-secret"},
		"description": "release automation", "credential_type": "token",
	}
	created := postProjectControlJSON(t, app, http.MethodPost, "/api/go/secrets", createBody, http.StatusOK)
	if created["secret"].(map[string]any)["valueConfigured"] != true ||
		created["secret"].(map[string]any)["credentialsConfigured"] != true {
		t.Fatalf("created secret projection = %#v", created)
	}
	assertSecretResponseRedacted(t, created)

	listed := getProjectControlJSON(t, app, "/api/go/secrets", http.StatusOK)
	if len(listed["secrets"].([]any)) != 1 {
		t.Fatalf("listed secrets = %#v", listed)
	}
	assertSecretResponseRedacted(t, listed)

	updated := postProjectControlJSON(t, app, http.MethodPatch, "/api/go/secrets/secret-1", map[string]any{
		"description": "updated description",
	}, http.StatusOK)
	if updated["secret"].(map[string]any)["description"] != "updated description" {
		t.Fatalf("updated secret = %#v", updated)
	}
	assertSecretResponseRedacted(t, updated)

	restarted := New(Options{FileRoot: root}).Handler()
	listed = getProjectControlJSON(t, restarted, "/api/go/secrets", http.StatusOK)
	if len(listed["secrets"].([]any)) != 1 {
		t.Fatalf("restarted secrets = %#v", listed)
	}
	assertSecretResponseRedacted(t, listed)

	raw, err := os.ReadFile(filepath.Join(root, "secrets", "vault.enc"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("api-super-secret")) || bytes.Contains(raw, []byte("credential-super-secret")) {
		t.Fatal("encrypted vault contains API plaintext")
	}

	postProjectControlJSON(t, restarted, http.MethodDelete, "/api/go/secrets/secret-1", map[string]any{}, http.StatusOK)
	listed = getProjectControlJSON(t, restarted, "/api/go/secrets", http.StatusOK)
	if len(listed["secrets"].([]any)) != 0 {
		t.Fatalf("secrets after delete = %#v", listed)
	}
}

func assertSecretResponseRedacted(t *testing.T, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(encoded)
	for _, forbidden := range []string{"api-super-secret", "credential-super-secret", `"value":`, `"credentials":`} {
		if bytes.Contains([]byte(raw), []byte(forbidden)) {
			t.Fatalf("secret response leaked %q: %s", forbidden, raw)
		}
	}
}
