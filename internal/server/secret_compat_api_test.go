package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCompatibilitySecretsListCreateUpdateEncryptAndSurviveRestart(t *testing.T) {
	root := t.TempDir()
	app := New(Options{FileRoot: root}).Handler()
	if listed := compatibilitySecretArrayRequest(t, app, http.MethodGet, "/api/secrets", "", nil, http.StatusOK); len(listed) != 0 {
		t.Fatalf("initial secrets = %#v", listed)
	}

	generic := compatibilitySecretObjectRequest(t, app, http.MethodPost, "/api/secrets", "", map[string]any{
		"provider": "generic", "name": "oracle_key", "value": "oracle-secret", "description": "initial",
		"ignored_by_v11": true,
	}, http.StatusCreated)
	assertCompatibilitySecretProjection(t, generic, "ORACLE_KEY", "generic")
	if generic["masked_preview"] != "orac\u00b7\u00b7\u00b7\u00b7cret" || generic["description"] != "initial" ||
		generic["credential_type"] != nil || generic["buckets"] != nil || generic["region"] != nil {
		t.Fatalf("generic projection = %#v", generic)
	}
	assertCompatibilitySecretRedacted(t, generic, "oracle-secret")
	if other := compatibilitySecretArrayRequest(t, app, http.MethodGet, "/api/secrets", "other", nil, http.StatusOK); len(other) != 0 {
		t.Fatalf("cross-user secrets = %#v", other)
	}

	serviceAccount := map[string]any{
		"type": "service_account", "project_id": "project-oracle", "private_key": "gcp-private-key",
		"client_email": "service-account-name@example.test",
	}
	gcp := compatibilitySecretObjectRequest(t, app, http.MethodPost, "/api/secrets", "local", map[string]any{
		"provider": "gcp", "credentials": map[string]any{"service_account_json": serviceAccount},
	}, http.StatusCreated)
	assertCompatibilitySecretProjection(t, gcp, "Google Cloud", "gcp")
	if gcp["masked_preview"] != "project-oracle \u2014 service-account-name" {
		t.Fatalf("GCP preview = %#v", gcp)
	}
	gcpFields := gcp["masked_fields"].(map[string]any)
	if gcpFields["service_account_json"] != gcp["masked_preview"] {
		t.Fatalf("GCP masked fields = %#v", gcpFields)
	}
	assertCompatibilitySecretRedacted(t, gcp, "gcp-private-key")

	awsCredentials := map[string]any{
		"access_key_id": "ABCDEFGHIJKLMNOP", "secret_access_key": "aws-original-secret",
		"region": "us-west-2", "endpoint": "https://objects.example.test",
	}
	aws := compatibilitySecretObjectRequest(t, app, http.MethodPost, "/api/secrets", "local", map[string]any{
		"provider": "aws", "credentials": awsCredentials, "credential_type": "hmac_key",
		"buckets": []string{"evidence"}, "region": "us-west-2",
	}, http.StatusCreated)
	assertCompatibilitySecretProjection(t, aws, "AWS", "aws")
	awsID := aws["id"].(string)
	updatedAWS := compatibilitySecretObjectRequest(t, app, http.MethodPatch, "/api/secrets/"+awsID, "local", map[string]any{
		"credentials": map[string]any{"secret_access_key": "aws-updated-secret"},
	}, http.StatusOK)
	if updatedAWS["name"] != "AWS" || updatedAWS["credential_type"] != "hmac_key" ||
		updatedAWS["masked_preview"] != "ABCD\u00b7\u00b7\u00b7\u00b7MNOP" {
		t.Fatalf("updated AWS = %#v", updatedAWS)
	}
	awsFields := updatedAWS["masked_fields"].(map[string]any)
	if awsFields["endpoint"] != "https://objects.example.test" || awsFields["access_key_id"] != "ABCD\u00b7\u00b7\u00b7\u00b7MNOP" {
		t.Fatalf("merged AWS fields = %#v", awsFields)
	}

	genericID := generic["id"].(string)
	time.Sleep(2 * time.Millisecond)
	cleared := compatibilitySecretObjectRequest(t, app, http.MethodPatch, "/api/secrets/"+genericID, "local", map[string]any{
		"name": "updated_key", "value": "updated-secret", "description": nil,
	}, http.StatusOK)
	if cleared["name"] != "UPDATED_KEY" || cleared["description"] != nil || cleared["masked_preview"] != "upda\u00b7\u00b7\u00b7\u00b7cret" {
		t.Fatalf("updated generic = %#v", cleared)
	}
	updatedAt := cleared["updated_at"]
	noOp := compatibilitySecretObjectRequest(t, app, http.MethodPatch, "/api/secrets/"+genericID, "local", map[string]any{
		"provider": "github", "credential_type": "ignored",
	}, http.StatusOK)
	if noOp["provider"] != "generic" || noOp["credential_type"] != nil || noOp["updated_at"] != updatedAt {
		t.Fatalf("unknown update fields were not ignored = %#v", noOp)
	}
	missing := compatibilitySecretObjectRequest(t, app, http.MethodPatch, "/api/secrets/"+genericID, "other", map[string]any{
		"description": "cross-user mutation",
	}, http.StatusNotFound)
	if missing["detail"] != "Secret "+genericID+" not found" {
		t.Fatalf("cross-user update = %#v", missing)
	}

	assertCompatibilitySecretValidation(t, app)
	assertCompatibilitySecretConcurrentProviderUniqueness(t, app)
	literature := compatibilitySecretObjectRequest(t, app, http.MethodPost, "/api/secrets", "local", map[string]any{
		"provider": "literature", "name": "  Literature Custom  ", "credentials": map[string]any{},
	}, http.StatusCreated)
	if literature["name"] != "  Literature Custom  " || literature["masked_preview"] != "(empty)" {
		t.Fatalf("empty literature projection = %#v", literature)
	}
	literatureNoOp := compatibilitySecretObjectRequest(t, app, http.MethodPatch,
		"/api/secrets/"+literature["id"].(string), "local", map[string]any{"provider": "generic"}, http.StatusOK)
	if literatureNoOp["name"] != literature["name"] || literatureNoOp["updated_at"] != literature["updated_at"] {
		t.Fatalf("literature no-op changed persisted state = %#v", literatureNoOp)
	}

	vault, err := os.ReadFile(filepath.Join(root, "secrets", "vault.enc"))
	if err != nil {
		t.Fatal(err)
	}
	for _, plaintext := range []string{
		"oracle-secret", "updated-secret", "gcp-private-key", "aws-original-secret", "aws-updated-secret",
		"github-concurrent-token-one", "github-concurrent-token-two",
	} {
		if bytes.Contains(vault, []byte(plaintext)) {
			t.Fatalf("encrypted vault leaked %q", plaintext)
		}
	}

	restartedServer := New(Options{FileRoot: root})
	restarted := restartedServer.Handler()
	listed := compatibilitySecretArrayRequest(t, restarted, http.MethodGet, "/api/secrets", "local", nil, http.StatusOK)
	if len(listed) != 5 {
		t.Fatalf("restarted secrets = %#v", listed)
	}
	names := make([]string, 0, len(listed))
	for _, secret := range listed {
		names = append(names, secret["name"].(string))
		assertCompatibilitySecretRedacted(t, secret, "updated-secret", "gcp-private-key", "aws-updated-secret")
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("secret list is not name-sorted: %#v", names)
	}
	storedGCP, found, err := restartedServer.secretStore.ResolveForUser(gcp["id"].(string), "local")
	if err != nil || !found {
		t.Fatalf("resolve restarted GCP found=%v err=%v", found, err)
	}
	storedServiceAccount, ok := storedGCP.CredentialObject()["service_account_json"].(map[string]any)
	if !ok || storedServiceAccount["project_id"] != "project-oracle" || storedServiceAccount["private_key"] != "gcp-private-key" {
		t.Fatalf("structured GCP credential was not preserved = %#v", storedGCP.CredentialObject())
	}
	storedAWS, found, err := restartedServer.secretStore.ResolveForUser(awsID, "local")
	if err != nil || !found || storedAWS.Credentials["access_key_id"] != "ABCDEFGHIJKLMNOP" ||
		storedAWS.Credentials["secret_access_key"] != "aws-updated-secret" {
		t.Fatalf("merged AWS credential = %#v found=%v err=%v", storedAWS, found, err)
	}
	storedLiterature, found, err := restartedServer.secretStore.ResolveForUser(literature["id"].(string), "local")
	if err != nil || !found || storedLiterature.CredentialObject() == nil || len(storedLiterature.CredentialObject()) != 0 {
		t.Fatalf("empty literature credentials found=%v err=%v value=%#v", found, err, storedLiterature.CredentialObject())
	}
}

func TestCompatibilitySecretDeleteIsOwnerScopedAndDurable(t *testing.T) {
	root := t.TempDir()
	app := New(Options{FileRoot: root}).Handler()
	created := compatibilitySecretObjectRequest(t, app, http.MethodPost, "/api/secrets", "owner", map[string]any{
		"provider": "generic", "name": "delete_owner_only", "value": "delete-secret-value",
	}, http.StatusCreated)
	id := created["id"].(string)

	foreign := compatibilitySecretObjectRequest(t, app, http.MethodDelete, "/api/secrets/"+id, "other", nil, http.StatusNotFound)
	if foreign["detail"] != "Secret "+id+" not found" {
		t.Fatalf("foreign delete = %#v", foreign)
	}
	if listed := compatibilitySecretArrayRequest(t, app, http.MethodGet, "/api/secrets", "owner", nil, http.StatusOK); len(listed) != 1 {
		t.Fatalf("owner list after foreign delete = %#v", listed)
	}

	deleted := compatibilitySecretObjectRequest(t, app, http.MethodDelete, "/api/secrets/"+id, "owner", nil, http.StatusOK)
	if len(deleted) != 2 || deleted["deleted"] != true || deleted["id"] != id {
		t.Fatalf("delete response = %#v", deleted)
	}
	restarted := New(Options{FileRoot: root}).Handler()
	if listed := compatibilitySecretArrayRequest(t, restarted, http.MethodGet, "/api/secrets", "owner", nil, http.StatusOK); len(listed) != 0 {
		t.Fatalf("list after delete restart = %#v", listed)
	}
	missing := compatibilitySecretObjectRequest(t, restarted, http.MethodDelete, "/api/secrets/"+id, "owner", nil, http.StatusNotFound)
	if missing["detail"] != "Secret "+id+" not found" {
		t.Fatalf("repeat delete = %#v", missing)
	}
}

func assertCompatibilitySecretProjection(t *testing.T, secret map[string]any, name, provider string) {
	t.Helper()
	if len(secret) != 11 || secret["id"] == "" || secret["name"] != name || secret["provider"] != provider {
		t.Fatalf("secret projection = %#v", secret)
	}
	for _, key := range []string{"created_at", "updated_at"} {
		value, ok := secret[key].(string)
		if !ok {
			t.Fatalf("secret %s = %#v", key, secret[key])
		}
		if _, err := time.Parse("2006-01-02T15:04:05.000Z", value); err != nil {
			t.Fatalf("secret %s is not v1.1 ISO milliseconds: %q: %v", key, value, err)
		}
	}
}

func assertCompatibilitySecretValidation(t *testing.T, app http.Handler) {
	t.Helper()
	tests := []struct {
		name   string
		body   map[string]any
		detail string
	}{
		{name: "invalid provider", body: map[string]any{"provider": "unsupported", "credentials": map[string]any{}}, detail: "Invalid provider: unsupported"},
		{name: "reserved generic", body: map[string]any{"provider": "generic", "name": "PATH", "value": "x"}, detail: "Secret name 'PATH' is reserved and cannot be used"},
		{name: "github token", body: map[string]any{"provider": "github", "credentials": map[string]any{}}, detail: "GitHub credentials require a non-empty 'token'"},
		{name: "aws access id", body: map[string]any{"provider": "aws", "credentials": map[string]any{"access_key_id": "short", "secret_access_key": "secret"}}, detail: "AWS access_key_id must be 16-128 characters"},
		{name: "empty value", body: map[string]any{"provider": "generic", "name": "EMPTY", "value": ""}, detail: "value must be at least 1 character"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := compatibilitySecretObjectRequest(t, app, http.MethodPost, "/api/secrets", "local", test.body, http.StatusBadRequest)
			if response["detail"] != test.detail {
				t.Fatalf("validation response = %#v, want %q", response, test.detail)
			}
		})
	}
}

func assertCompatibilitySecretConcurrentProviderUniqueness(t *testing.T, app http.Handler) {
	t.Helper()
	start := make(chan struct{})
	statuses := make(chan int, 2)
	var wait sync.WaitGroup
	requests := make([]*http.Request, 0, 2)
	for _, token := range []string{"github-concurrent-token-one", "github-concurrent-token-two"} {
		requests = append(requests, compatibilitySecretRequest(t, http.MethodPost, "/api/secrets", "local", map[string]any{
			"provider": "github", "credentials": map[string]any{"token": token},
		}))
	}
	for _, request := range requests {
		wait.Add(1)
		go func(request *http.Request) {
			defer wait.Done()
			<-start
			response := httptest.NewRecorder()
			app.ServeHTTP(response, request)
			statuses <- response.Code
		}(request)
	}
	close(start)
	wait.Wait()
	close(statuses)
	got := make([]int, 0, 2)
	for status := range statuses {
		got = append(got, status)
	}
	sort.Ints(got)
	if len(got) != 2 || got[0] != http.StatusCreated || got[1] != http.StatusBadRequest {
		t.Fatalf("concurrent GitHub statuses = %#v", got)
	}
}

func compatibilitySecretObjectRequest(
	t *testing.T, handler http.Handler, method, target, userID string, body any, wantStatus int,
) map[string]any {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, compatibilitySecretRequest(t, method, target, userID, body))
	if response.Code != wantStatus {
		t.Fatalf("%s %s status = %d, want %d: %s", method, target, response.Code, wantStatus, response.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode %s %s: %v: %s", method, target, err, response.Body.String())
	}
	return decoded
}

func compatibilitySecretArrayRequest(
	t *testing.T, handler http.Handler, method, target, userID string, body any, wantStatus int,
) []map[string]any {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, compatibilitySecretRequest(t, method, target, userID, body))
	if response.Code != wantStatus {
		t.Fatalf("%s %s status = %d, want %d: %s", method, target, response.Code, wantStatus, response.Body.String())
	}
	var decoded []map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode %s %s: %v: %s", method, target, err, response.Body.String())
	}
	return decoded
}

func compatibilitySecretRequest(t *testing.T, method, target, userID string, body any) *http.Request {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, target, bytes.NewReader(encoded))
	request.RemoteAddr = "127.0.0.1:12345"
	if userID != "" {
		request.Header.Set("X-Synon-User-Id", userID)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

func assertCompatibilitySecretRedacted(t *testing.T, value any, forbidden ...string) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range forbidden {
		if strings.Contains(string(raw), text) {
			t.Fatalf("secret response leaked %q: %s", text, raw)
		}
	}
	if secret, ok := value.(map[string]any); ok {
		for _, key := range []string{"value", "credentials", "credentialData"} {
			if _, exposed := secret[key]; exposed {
				t.Fatalf("secret response exposed private field %q: %s", key, raw)
			}
		}
	}
}
