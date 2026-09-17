package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"synon-go/internal/account"
	settingsstore "synon-go/internal/persistence/settings"
)

func TestLocalAccountManagementReaderHonorsCanceledReadsBeforeStoreAccess(t *testing.T) {
	reader := newLocalAccountManagementReader(webAuthenticationState{}, nil)
	_, err := reader.Read(context.Background(), account.ManagementReadRequest{})
	if err == nil {
		t.Fatal("missing account was unexpectedly accepted")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = reader.Read(canceled, account.ManagementReadRequest{})
	if err != context.Canceled {
		t.Fatalf("canceled read error = %v", err)
	}
}

func TestLocalAccountManagementReaderProjectsEffectiveLocalPolicyWithoutCentralClaims(t *testing.T) {
	root := t.TempDir()
	settings := settingsstore.New(root + "/settings.json")
	if _, err := settings.Set(approvalDefaultsSettingKey, map[string]any{"mode": "ask"}); err != nil {
		t.Fatal(err)
	}
	state := webAuthenticationState{webSessions: &webSessionStore{}}
	localAuth := newSynonLinkAuthenticator(SynonLinkAuthOptions{
		Username: "operator", Password: "test-secret-password", UserID: "operator",
	})
	reader := newLocalAccountManagementReaderWithConfig(state, localAuth, localAccountManagementConfig{
		settings: settings, allowedDomains: []string{"example.org"}, mcpConfigured: true,
	})
	snapshot, err := reader.Read(context.Background(), account.ManagementReadRequest{AccountID: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ControlPlane.Managed || snapshot.ControlPlane.CentralRevocation {
		t.Fatalf("local reader made central claims: %#v", snapshot.ControlPlane)
	}
	if !snapshot.ControlPlane.SessionProtection {
		t.Fatal("local session protection was not reported")
	}
	if snapshot.RuntimePolicy.ApprovalMode != "ask" || snapshot.RuntimePolicy.NetworkMode != "allowlist" ||
		snapshot.RuntimePolicy.MCPBoundary != "per-connector" {
		t.Fatalf("runtime policy = %#v", snapshot.RuntimePolicy)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("projected snapshot rejected: %v", err)
	}
}

func TestAccountManagementRequiresAuthenticationAndReturnsVersionedReadModel(t *testing.T) {
	app := New(Options{
		FileRoot: t.TempDir(),
		SynonLinkAuth: SynonLinkAuthOptions{
			Username: "operator", Password: "test-secret-password", UserID: "operator",
		},
	}).Handler()

	unauthenticated := httptest.NewRecorder()
	app.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/account/management", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated management status = %d, body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}

	cookie := loginWebCookie(t, app)
	methodNotAllowed := httptest.NewRecorder()
	methodRequest := httptest.NewRequest(http.MethodPost, "/api/account/management", nil)
	methodRequest.AddCookie(cookie)
	app.ServeHTTP(methodNotAllowed, methodRequest)
	if methodNotAllowed.Code != http.StatusForbidden {
		t.Fatalf("POST management status = %d", methodNotAllowed.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/account/management", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("authenticated management status = %d, body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers = %#v", response.Header())
	}

	var payload struct {
		Success bool                       `json:"success"`
		Data    map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Success || payload.Data == nil {
		t.Fatalf("management payload = %#v", payload)
	}
	var schema int
	if err := json.Unmarshal(payload.Data["schemaVersion"], &schema); err != nil || schema != account.ManagementSchemaVersion {
		t.Fatalf("schemaVersion = %d, err=%v", schema, err)
	}
	var controlPlane map[string]any
	if err := json.Unmarshal(payload.Data["controlPlane"], &controlPlane); err != nil {
		t.Fatal(err)
	}
	if controlPlane["mode"] != "local-runtime" || controlPlane["dataResidency"] != "local" ||
		controlPlane["centralRevocation"] != false || controlPlane["managed"] != false ||
		controlPlane["sessionProtection"] != true {
		t.Fatalf("controlPlane = %#v", controlPlane)
	}
	var boundaries []map[string]any
	if err := json.Unmarshal(payload.Data["controlBoundaries"], &boundaries); err != nil {
		t.Fatal(err)
	}
	if len(boundaries) != 6 || boundaries[0]["id"] != "identity" || boundaries[3]["status"] != "not-configured" {
		t.Fatalf("controlBoundaries = %#v", boundaries)
	}
	var runtimePolicy map[string]any
	if err := json.Unmarshal(payload.Data["runtimePolicy"], &runtimePolicy); err != nil {
		t.Fatal(err)
	}
	if runtimePolicy["source"] != "local-runtime" || runtimePolicy["approvalMode"] != "confirm" {
		t.Fatalf("runtimePolicy = %#v", runtimePolicy)
	}
	var posture map[string]any
	if err := json.Unmarshal(payload.Data["securityPosture"], &posture); err != nil {
		t.Fatal(err)
	}
	if posture["mfaEnabled"] != false || posture["passkeyEnabled"] != false || posture["recoveryConfigured"] != false {
		t.Fatalf("securityPosture = %#v", posture)
	}
	var methods []map[string]any
	if err := json.Unmarshal(payload.Data["loginMethods"], &methods); err != nil {
		t.Fatal(err)
	}
	if len(methods) != 1 || methods[0]["id"] != "local" || methods[0]["connected"] != true {
		t.Fatalf("loginMethods = %#v", methods)
	}
	if strings.Contains(response.Body.String(), cookie.Value) {
		t.Fatal("management response leaked the session token")
	}
}

func TestAccountManagementExposesConfiguredProvidersSeparatelyFromConnectedMethods(t *testing.T) {
	app := New(Options{
		FileRoot: t.TempDir(),
		SynonLinkAuth: SynonLinkAuthOptions{
			Username: "operator", Password: "test-secret-password", UserID: "operator",
		},
		WebAuth: WebAuthOptions{
			Google: WebOIDCProviderOptions{
				IssuerURL: "https://accounts.example.test", ClientID: "client-id", ClientSecret: "client-secret",
			},
		},
	}).Handler()

	cookie := loginWebCookie(t, app)
	request := httptest.NewRequest(http.MethodGet, "/api/account/management", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("management status = %d, body=%s", response.Code, response.Body.String())
	}

	var payload struct {
		Data struct {
			LoginMethods []struct {
				ID        string `json:"id"`
				Enabled   bool   `json:"enabled"`
				Connected bool   `json:"connected"`
			} `json:"loginMethods"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data.LoginMethods) != 2 {
		t.Fatalf("loginMethods = %#v", payload.Data.LoginMethods)
	}
	for _, method := range payload.Data.LoginMethods {
		if method.ID == "google" && (!method.Enabled || method.Connected) {
			t.Fatalf("configured but unconnected Google method = %#v", method)
		}
	}
}
