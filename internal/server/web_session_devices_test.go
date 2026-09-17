package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWebAccountSecurityAPIProjectsAndRevokesDevices(t *testing.T) {
	app := New(Options{
		FileRoot: t.TempDir(),
		SynonLinkAuth: SynonLinkAuthOptions{
			Username: "operator", Password: "test-secret-password", UserID: "account-1",
		},
	})
	firstSession, _, firstDevice := loginWebDevice(t, app, nil)
	secondSession, _, _ := loginWebDevice(t, app, firstDevice)
	currentSession, currentCSRF, currentDevice := loginWebDevice(t, app, nil)
	if firstDevice.Value == currentDevice.Value {
		t.Fatal("separate browsers reused a device identity")
	}

	list := httptest.NewRequest(http.MethodGet, "/api/account/security", nil)
	list.RemoteAddr = "127.0.0.1:12345"
	list.AddCookie(currentSession)
	list.AddCookie(currentDevice)
	listResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(listResponse, list)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list devices status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}
	var payload struct {
		Sessions []webSessionView `json:"sessions"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Sessions) != 2 || !payload.Sessions[0].Current {
		t.Fatalf("device payload = %#v", payload.Sessions)
	}
	if payload.Sessions[0].IPAddress != "127.0.0.1" || payload.Sessions[1].IPAddress != "127.0.0.1" {
		t.Fatalf("device IP projection = %#v", payload.Sessions)
	}
	otherDeviceID := payload.Sessions[1].ID

	revoke := httptest.NewRequest(
		http.MethodPost,
		"/api/account/security/sessions/"+otherDeviceID+"/revoke",
		strings.NewReader("{}"),
	)
	revoke.RemoteAddr = "127.0.0.1:12345"
	revoke.AddCookie(currentSession)
	revoke.AddCookie(currentCSRF)
	revoke.Header.Set(webCSRFHeaderName, currentCSRF.Value)
	revokeResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(revokeResponse, revoke)
	if revokeResponse.Code != http.StatusOK || !strings.Contains(revokeResponse.Body.String(), `"revoked":2`) {
		t.Fatalf("revoke device status=%d body=%s", revokeResponse.Code, revokeResponse.Body.String())
	}
	assertWebSessionStatus(t, app, firstSession, http.StatusUnauthorized)
	assertWebSessionStatus(t, app, secondSession, http.StatusUnauthorized)
	assertWebSessionStatus(t, app, currentSession, http.StatusOK)
}

func TestWebAccountSecurityAPISeparatesTrustedDevicesFromLegacySessions(t *testing.T) {
	app := New(Options{
		FileRoot: t.TempDir(),
		SynonLinkAuth: SynonLinkAuthOptions{
			Username: "operator", Password: "test-secret-password", UserID: "account-1",
		},
	})
	legacyMetadata := webSessionMetadata{
		UserAgent: "Chrome · Windows", UserAgentHash: webSecretHash("same-legacy-ua"),
		NetworkClass: "remote", IPAddress: "198.51.100.10",
	}
	if _, _, _, err := app.webSessions.Create("account-1", "local", true, legacyMetadata); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := app.webSessions.Create("account-1", "local", true, legacyMetadata); err != nil {
		t.Fatal(err)
	}
	currentSession, _, currentDevice := loginWebDevice(t, app, nil)

	request := httptest.NewRequest(http.MethodGet, "/api/account/security", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.AddCookie(currentSession)
	request.AddCookie(currentDevice)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("list devices status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Sessions []webSessionView `json:"sessions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Sessions) != 3 {
		t.Fatalf("device payload = %#v, want one trusted device and two independent legacy sessions", payload.Sessions)
	}
	trusted, legacy := 0, 0
	for _, session := range payload.Sessions {
		if session.Legacy {
			legacy++
			continue
		}
		trusted++
		if !session.Current || session.IPAddress != "127.0.0.1" {
			t.Fatalf("trusted current device = %#v", session)
		}
	}
	if trusted != 1 || legacy != 2 {
		t.Fatalf("trusted=%d legacy=%d payload=%#v", trusted, legacy, payload.Sessions)
	}
}

func loginWebDevice(t *testing.T, app *Server, device *http.Cookie) (*http.Cookie, *http.Cookie, *http.Cookie) {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodPost,
		"/login",
		strings.NewReader(`{"username":"operator","password":"test-secret-password","remember":true}`),
	)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("User-Agent", "Mozilla/5.0 Chrome/140.0 Windows")
	if device != nil {
		request.AddCookie(device)
	}
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
	}
	var session, csrf, returnedDevice *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		switch cookie.Name {
		case webSessionCookieName:
			session = cookie
		case webCSRFCookieName:
			csrf = cookie
		case webDeviceCookieName:
			returnedDevice = cookie
		}
	}
	if session == nil || csrf == nil || returnedDevice == nil || !returnedDevice.HttpOnly || returnedDevice.MaxAge <= 0 {
		t.Fatalf("login cookies = %#v", response.Result().Cookies())
	}
	return session, csrf, returnedDevice
}

func TestWebSessionStoreListsDevicesInsteadOfLoginAttempts(t *testing.T) {
	store, err := openWebSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	chromeDevice := webSecretHash("chrome-device")
	firefoxDevice := webSecretHash("firefox-device")

	firstChromeToken, _, _, err := store.Create("account-1", "local", true, webSessionMetadata{
		UserAgent: "Chrome · Windows", UserAgentHash: webSecretHash("chrome-ua"),
		DeviceIDHash: chromeDevice, NetworkClass: "loopback",
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	secondChromeToken, _, _, err := store.Create("account-1", "google", true, webSessionMetadata{
		UserAgent: "Chrome · Windows", UserAgentHash: webSecretHash("chrome-ua"),
		DeviceIDHash: chromeDevice, NetworkClass: "loopback",
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	firefoxToken, _, _, err := store.Create("account-1", "local", false, webSessionMetadata{
		UserAgent: "Firefox · Linux", UserAgentHash: webSecretHash("firefox-ua"),
		DeviceIDHash: firefoxDevice, NetworkClass: "private",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := store.BindCurrentDevice("account-1", secondChromeToken, webSessionMetadata{
		DeviceIDHash: chromeDevice, IPAddress: "198.51.100.42", NetworkClass: "remote",
	}); err != nil {
		t.Fatal(err)
	}
	devices, err := store.List("account-1", secondChromeToken)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 {
		t.Fatalf("devices = %#v, want two physical devices for three login sessions", devices)
	}
	if !devices[0].Current || devices[0].UserAgent != "Chrome · Windows" || devices[0].AuthMethod != "google" {
		t.Fatalf("current Chrome device = %#v", devices[0])
	}
	if devices[0].IPAddress != "198.51.100.42" {
		t.Fatalf("migrated Chrome device IP = %q", devices[0].IPAddress)
	}
	if devices[0].ID == devices[1].ID || devices[1].Current || devices[1].UserAgent != "Firefox · Linux" {
		t.Fatalf("device identity projection = %#v", devices)
	}

	revoked, current, err := store.RevokeDevice("account-1", devices[0].ID, firefoxToken)
	if err != nil || revoked != 2 || current {
		t.Fatalf("revoke Chrome device = %d current=%v err=%v", revoked, current, err)
	}
	for name, token := range map[string]string{"first Chrome": firstChromeToken, "second Chrome": secondChromeToken} {
		if _, ok, authErr := store.Authenticate(token, false); authErr != nil || ok {
			t.Fatalf("%s session remained valid: ok=%v err=%v", name, ok, authErr)
		}
	}
	if _, ok, authErr := store.Authenticate(firefoxToken, false); authErr != nil || !ok {
		t.Fatalf("unrelated Firefox device was revoked: ok=%v err=%v", ok, authErr)
	}
}

func TestWebSessionStoreKeepsIdenticalLegacySessionsDistinctDuringMigration(t *testing.T) {
	store, err := openWebSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	legacyMetadata := webSessionMetadata{
		UserAgent: "Chrome · Windows", UserAgentHash: webSecretHash("same-chrome-ua"),
		NetworkClass: "remote", IPAddress: "198.51.100.10",
	}
	firstToken, _, firstSession, err := store.Create("account-1", "local", true, legacyMetadata)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	secondToken, _, secondSession, err := store.Create("account-1", "local", true, legacyMetadata)
	if err != nil {
		t.Fatal(err)
	}

	devices, err := store.List("account-1", secondToken)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 || devices[0].ID == devices[1].ID {
		t.Fatalf("identical legacy sessions must remain distinct devices: %#v", devices)
	}
	for _, device := range devices {
		if !device.Legacy {
			t.Fatalf("legacy session was projected as a trusted device: %#v", devices)
		}
		if device.ID == firstSession.ID || device.ID == secondSession.ID {
			t.Fatalf("legacy device id exposed a session id: %#v", devices)
		}
	}

	currentDevice := webSecretHash("current-device-cookie")
	if err := store.BindCurrentDevice("account-1", secondToken, webSessionMetadata{
		DeviceIDHash: currentDevice, IPAddress: "203.0.113.42", NetworkClass: "remote",
	}); err != nil {
		t.Fatal(err)
	}
	devices, err = store.List("account-1", secondToken)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 {
		t.Fatalf("migrating the current legacy session merged another session: %#v", devices)
	}
	if devices[0].Legacy || !devices[0].Current || !devices[1].Legacy || devices[1].Current {
		t.Fatalf("trusted and legacy projections were not separated: %#v", devices)
	}

	revoked, err := store.RevokeOthers("account-1", secondToken)
	if err != nil || revoked != 1 {
		t.Fatalf("revoke other legacy device = %d err=%v", revoked, err)
	}
	if _, ok, authErr := store.Authenticate(firstToken, false); authErr != nil || ok {
		t.Fatalf("other legacy session remained valid: ok=%v err=%v", ok, authErr)
	}
	if _, ok, authErr := store.Authenticate(secondToken, false); authErr != nil || !ok {
		t.Fatalf("current migrated session was revoked: ok=%v err=%v", ok, authErr)
	}
}

func TestWebSessionStoreRevokeOthersKeepsEverySessionOnCurrentDevice(t *testing.T) {
	store, err := openWebSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	currentDevice := webSecretHash("current-device")
	otherDevice := webSecretHash("other-device")

	currentToken, _, _, err := store.Create("account-1", "local", true, webSessionMetadata{DeviceIDHash: currentDevice})
	if err != nil {
		t.Fatal(err)
	}
	sameDeviceToken, _, _, err := store.Create("account-1", "local", true, webSessionMetadata{DeviceIDHash: currentDevice})
	if err != nil {
		t.Fatal(err)
	}
	otherToken, _, _, err := store.Create("account-1", "local", true, webSessionMetadata{DeviceIDHash: otherDevice})
	if err != nil {
		t.Fatal(err)
	}

	revoked, err := store.RevokeOthers("account-1", currentToken)
	if err != nil || revoked != 1 {
		t.Fatalf("revoke other devices = %d err=%v", revoked, err)
	}
	for name, token := range map[string]string{"current session": currentToken, "same-device session": sameDeviceToken} {
		if _, ok, authErr := store.Authenticate(token, false); authErr != nil || !ok {
			t.Fatalf("%s was revoked: ok=%v err=%v", name, ok, authErr)
		}
	}
	if _, ok, authErr := store.Authenticate(otherToken, false); authErr != nil || ok {
		t.Fatalf("other device remained valid: ok=%v err=%v", ok, authErr)
	}
}

func TestWebSessionMetadataKeepsStableOpaqueDeviceCookie(t *testing.T) {
	store, err := openWebSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, "http://127.0.0.1/api/auth/login", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 Chrome/140.0 Windows")
	request.RemoteAddr = "203.0.113.42:443"
	metadata, deviceToken, err := store.metadataForSessionCreation(request)
	if err != nil {
		t.Fatal(err)
	}
	if deviceToken == "" || !validWebSecretHash(metadata.DeviceIDHash) || metadata.IPAddress != "203.0.113.42" {
		t.Fatalf("device token/hash missing: token=%q metadata=%#v", deviceToken, metadata)
	}

	repeat, err := http.NewRequest(http.MethodPost, "http://127.0.0.1/api/auth/login", nil)
	if err != nil {
		t.Fatal(err)
	}
	repeat.AddCookie(&http.Cookie{Name: webDeviceCookieName, Value: deviceToken})
	repeatMetadata, repeatToken, err := store.metadataForSessionCreation(repeat)
	if err != nil {
		t.Fatal(err)
	}
	if repeatToken != deviceToken || repeatMetadata.DeviceIDHash != metadata.DeviceIDHash {
		t.Fatalf("stable device changed: first=%#v repeat=%#v", metadata, repeatMetadata)
	}
}
