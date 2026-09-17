package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecretsHTTPAPIScopesUsersAndValidatesBaselineLimits(t *testing.T) {
	app := New(Options{FileRoot: t.TempDir()}).Handler()
	created := secretRequestJSON(t, app, http.MethodPost, "/api/go/secrets", "user-a", map[string]any{
		"id": "tenant-secret", "provider": "github", "name": "token",
		"value": "tenant-secret-value", "credentials": map[string]any{"token": "tenant-credential"},
	}, http.StatusOK)
	projection := created["secret"].(map[string]any)
	if projection["masked_preview"] != "configured" {
		t.Fatalf("masked preview = %#v", projection)
	}
	maskedFields := projection["masked_fields"].([]any)
	if len(maskedFields) != 1 || maskedFields[0] != "token" {
		t.Fatalf("masked fields = %#v", projection)
	}
	assertSecretResponseRedacted(t, created)

	otherUser := secretRequestJSON(t, app, http.MethodGet, "/api/go/secrets", "user-b", nil, http.StatusOK)
	if len(otherUser["secrets"].([]any)) != 0 {
		t.Fatalf("cross-tenant list = %#v", otherUser)
	}
	secretRequestJSON(t, app, http.MethodGet, "/api/go/secrets/tenant-secret", "user-b", nil, http.StatusNotFound)
	secretRequestJSON(t, app, http.MethodDelete, "/api/go/secrets/tenant-secret", "user-b", nil, http.StatusNotFound)

	azure := secretRequestJSON(t, app, http.MethodPost, "/api/go/secrets", "user-a", map[string]any{
		"provider": "azure", "credentials": map[string]any{"client_id": "client", "client_secret": "secret"},
	}, http.StatusOK)["secret"].(map[string]any)
	if azure["provider"] != "azure" || azure["credential_type"] != "client_secret" || azure["id"] == "" {
		t.Fatalf("azure secret defaults = %#v", azure)
	}

	for name, body := range map[string]map[string]any{
		"provider":    {"provider": "unsupported"},
		"name":        {"provider": "generic", "name": strings.Repeat("n", 129)},
		"value":       {"provider": "generic", "value": strings.Repeat("v", 65537)},
		"description": {"provider": "generic", "description": strings.Repeat("d", 257)},
		"region":      {"provider": "generic", "region": strings.Repeat("r", 65)},
	} {
		t.Run(name, func(t *testing.T) {
			secretRequestJSON(t, app, http.MethodPost, "/api/go/secrets", "user-a", body, http.StatusBadRequest)
		})
	}
}

func secretRequestJSON(t *testing.T, handler http.Handler, method, path, userID string, body any, wantStatus int) map[string]any {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("X-Synon-User-Id", userID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("%s %s status = %d, body = %s", method, path, response.Code, response.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}
