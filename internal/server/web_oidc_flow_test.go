package server

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type testOIDCProvider struct {
	server                *httptest.Server
	key                   *rsa.PrivateKey
	clientID              string
	clientSecret          string
	mu                    sync.Mutex
	nonce                 string
	challenge             string
	redirectURL           string
	scope                 string
	responseMode          string
	verifierSeen          bool
	clientSecretValidator func(string) bool
}

func newTestOIDCProvider(t *testing.T) *testOIDCProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &testOIDCProvider{key: key, clientID: "synon-test-client", clientSecret: "synon-test-secret"}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (p *testOIDCProvider) serveHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		writeTestOIDCJSON(w, http.StatusOK, map[string]any{
			"issuer":                                p.server.URL,
			"authorization_endpoint":                p.server.URL + "/authorize",
			"token_endpoint":                        p.server.URL + "/token",
			"jwks_uri":                              p.server.URL + "/keys",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
			"code_challenge_methods_supported":      []string{"S256"},
		})
	case "/keys":
		exponent := big.NewInt(int64(p.key.PublicKey.E)).Bytes()
		writeTestOIDCJSON(w, http.StatusOK, map[string]any{
			"keys": []map[string]any{{
				"kty": "RSA", "use": "sig", "kid": "test-key", "alg": "RS256",
				"n": base64.RawURLEncoding.EncodeToString(p.key.PublicKey.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(exponent),
			}},
		})
	case "/authorize":
		p.handleAuthorize(w, r)
	case "/token":
		p.handleToken(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (p *testOIDCProvider) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if query.Get("client_id") != p.clientID || query.Get("response_type") != "code" ||
		query.Get("code_challenge_method") != "S256" || query.Get("nonce") == "" ||
		query.Get("state") == "" || query.Get("redirect_uri") == "" {
		http.Error(w, "invalid authorization request", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.nonce = query.Get("nonce")
	p.challenge = query.Get("code_challenge")
	p.redirectURL = query.Get("redirect_uri")
	p.scope = query.Get("scope")
	p.responseMode = query.Get("response_mode")
	p.mu.Unlock()
	callback, err := url.Parse(query.Get("redirect_uri"))
	if err != nil {
		http.Error(w, "invalid redirect", http.StatusBadRequest)
		return
	}
	values := callback.Query()
	values.Set("code", "fixture-code")
	values.Set("state", query.Get("state"))
	callback.RawQuery = values.Encode()
	http.Redirect(w, r, callback.String(), http.StatusSeeOther)
}

func (p *testOIDCProvider) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.ParseForm() != nil {
		http.Error(w, "invalid token request", http.StatusBadRequest)
		return
	}
	clientID, clientSecret, basic := r.BasicAuth()
	if !basic {
		clientID, clientSecret = r.Form.Get("client_id"), r.Form.Get("client_secret")
	}
	p.mu.Lock()
	nonce, challenge, redirectURL := p.nonce, p.challenge, p.redirectURL
	p.mu.Unlock()
	verifier := r.Form.Get("code_verifier")
	validClientSecret := clientSecret == p.clientSecret
	if p.clientSecretValidator != nil {
		validClientSecret = p.clientSecretValidator(clientSecret)
	}
	if clientID != p.clientID || !validClientSecret ||
		r.Form.Get("code") != "fixture-code" || r.Form.Get("grant_type") != "authorization_code" ||
		r.Form.Get("redirect_uri") != redirectURL || webPKCEChallenge(verifier) != challenge {
		http.Error(w, "invalid token exchange", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.verifierSeen = true
	p.mu.Unlock()
	now := time.Now().UTC()
	idToken, err := p.signIDToken(map[string]any{
		"iss": p.server.URL, "sub": "google-subject-42", "aud": p.clientID,
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "nonce": nonce,
		"email": "scientist@example.org", "email_verified": true, "name": "Dr. Synon",
	})
	if err != nil {
		http.Error(w, "sign token", http.StatusInternalServerError)
		return
	}
	writeTestOIDCJSON(w, http.StatusOK, map[string]any{
		"access_token": "fixture-access-token-must-not-persist",
		"token_type":   "Bearer", "expires_in": 300, "id_token": idToken,
	})
}

func (p *testOIDCProvider) signIDToken(claims map[string]any) (string, error) {
	header, err := json.Marshal(map[string]any{"alg": "RS256", "kid": "test-key", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func writeTestOIDCJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func newTestApplePrivateKey(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func validTestAppleClientSecret(value, teamID, keyID, clientID string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return false
	}
	headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	payloadRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var header struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
	}
	var claims struct {
		Issuer    string `json:"iss"`
		Subject   string `json:"sub"`
		Audience  string `json:"aud"`
		IssuedAt  int64  `json:"iat"`
		ExpiresAt int64  `json:"exp"`
	}
	if json.Unmarshal(headerRaw, &header) != nil || json.Unmarshal(payloadRaw, &claims) != nil {
		return false
	}
	now := time.Now().UTC().Unix()
	return header.Algorithm == "ES256" && header.KeyID == keyID &&
		claims.Issuer == teamID && claims.Subject == clientID &&
		claims.Audience == webAppleIssuerURL && claims.IssuedAt <= now && claims.ExpiresAt > now
}

func TestWebGoogleOIDCProtocolFlowUsesPKCENonceAndOpaqueSession(t *testing.T) {
	provider := newTestOIDCProvider(t)
	root := t.TempDir()
	app := New(Options{
		FileRoot:   root,
		HTTPClient: provider.server.Client(),
		WebAuth: WebAuthOptions{Google: WebOIDCProviderOptions{
			IssuerURL: provider.server.URL, ClientID: provider.clientID, ClientSecret: provider.clientSecret,
		}},
	})
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Jar = jar
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}

	start, err := client.Get(server.URL + "/api/auth/oidc/google/start?remember=1&return_to=%2F%23%2Fsettings%2Faccount")
	if err != nil {
		t.Fatal(err)
	}
	start.Body.Close()
	if start.StatusCode != http.StatusSeeOther || !strings.HasPrefix(start.Header.Get("Location"), provider.server.URL+"/authorize") {
		t.Fatalf("start status=%d location=%q", start.StatusCode, start.Header.Get("Location"))
	}
	if !strings.Contains(start.Header.Get("Set-Cookie"), "SameSite=Lax") {
		t.Fatalf("Google transaction cookie = %q, want SameSite=Lax", start.Header.Get("Set-Cookie"))
	}
	authorize, err := client.Get(start.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	authorize.Body.Close()
	if authorize.StatusCode != http.StatusSeeOther || !strings.HasPrefix(authorize.Header.Get("Location"), server.URL+"/api/auth/oidc/google/callback") {
		t.Fatalf("authorize status=%d location=%q", authorize.StatusCode, authorize.Header.Get("Location"))
	}
	callbackURL := authorize.Header.Get("Location")
	callback, err := client.Get(callbackURL)
	if err != nil {
		t.Fatal(err)
	}
	callback.Body.Close()
	if callback.StatusCode != http.StatusSeeOther || callback.Header.Get("Location") != "/#/settings/account" {
		t.Fatalf("callback status=%d location=%q", callback.StatusCode, callback.Header.Get("Location"))
	}
	provider.mu.Lock()
	verifierSeen := provider.verifierSeen
	provider.mu.Unlock()
	if !verifierSeen {
		t.Fatal("token endpoint did not receive a valid PKCE verifier")
	}

	current, err := client.Get(server.URL + "/api/auth/user")
	if err != nil {
		t.Fatal(err)
	}
	defer current.Body.Close()
	var currentBody struct {
		Success bool `json:"success"`
		User    struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Email    string `json:"email"`
			Provider string `json:"provider"`
		} `json:"user"`
	}
	if err := json.NewDecoder(current.Body).Decode(&currentBody); err != nil {
		t.Fatal(err)
	}
	if current.StatusCode != http.StatusOK || !currentBody.Success || currentBody.User.ID == "" ||
		currentBody.User.Email != "scientist@example.org" || currentBody.User.Provider != "google" {
		t.Fatalf("current status=%d body=%#v", current.StatusCode, currentBody)
	}
	security, err := client.Get(server.URL + "/api/account/security")
	if err != nil {
		t.Fatal(err)
	}
	defer security.Body.Close()
	var securityBody struct {
		Success      bool             `json:"success"`
		LoginMethods []string         `json:"loginMethods"`
		Sessions     []webSessionView `json:"sessions"`
	}
	if err := json.NewDecoder(security.Body).Decode(&securityBody); err != nil {
		t.Fatal(err)
	}
	if security.StatusCode != http.StatusOK || !securityBody.Success ||
		len(securityBody.LoginMethods) != 1 || securityBody.LoginMethods[0] != "google" ||
		len(securityBody.Sessions) != 1 || !securityBody.Sessions[0].Current {
		t.Fatalf("security status=%d body=%#v", security.StatusCode, securityBody)
	}

	for _, name := range []string{"webui-accounts.json", "webui-sessions.json"} {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "fixture-access-token-must-not-persist") ||
			strings.Contains(string(raw), "google-subject-42.fixture") {
			t.Fatalf("provider token material persisted in %s", name)
		}
	}

	replay, err := client.Get(callbackURL)
	if err != nil {
		t.Fatal(err)
	}
	replay.Body.Close()
	if replay.StatusCode != http.StatusSeeOther || !strings.Contains(replay.Header.Get("Location"), "auth_error=transaction_invalid") {
		t.Fatalf("replay status=%d location=%q", replay.StatusCode, replay.Header.Get("Location"))
	}
}

func TestWebAppleOIDCProtocolFlowUsesAppleClientSecretAndFormPostMode(t *testing.T) {
	provider := newTestOIDCProvider(t)
	provider.clientID = "com.synon.biomed.web"
	provider.clientSecretValidator = func(value string) bool {
		return validTestAppleClientSecret(value, "apple-team-id", "apple-key-id", provider.clientID)
	}
	root := t.TempDir()
	server := httptest.NewUnstartedServer(http.NotFoundHandler())
	publicBaseURL := "https://" + server.Listener.Addr().String()
	app := New(Options{
		FileRoot:   root,
		HTTPClient: provider.server.Client(),
		WebAuth: WebAuthOptions{
			PublicBaseURL: publicBaseURL,
			Apple: WebAppleOIDCProviderOptions{
				IssuerURL: provider.server.URL, ClientID: provider.clientID,
				TeamID: "apple-team-id", KeyID: "apple-key-id", PrivateKey: newTestApplePrivateKey(t),
			},
		},
	})
	server.Config.Handler = app.Handler()
	server.StartTLS()
	t.Cleanup(server.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Jar = jar
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}

	start, err := client.Get(server.URL + "/api/auth/oidc/apple/start?remember=1&return_to=%2F%23%2Fsettings%2Faccount")
	if err != nil {
		t.Fatal(err)
	}
	start.Body.Close()
	if start.StatusCode != http.StatusSeeOther || !strings.HasPrefix(start.Header.Get("Location"), provider.server.URL+"/authorize") {
		t.Fatalf("start status=%d location=%q", start.StatusCode, start.Header.Get("Location"))
	}
	if cookie := start.Header.Get("Set-Cookie"); !strings.Contains(cookie, "SameSite=None") || !strings.Contains(cookie, "Secure") {
		t.Fatalf("Apple transaction cookie = %q, want SameSite=None; Secure", cookie)
	}
	authorize, err := client.Get(start.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	authorize.Body.Close()
	if authorize.StatusCode != http.StatusSeeOther || !strings.HasPrefix(authorize.Header.Get("Location"), server.URL+"/api/auth/oidc/apple/callback") {
		t.Fatalf("authorize status=%d location=%q", authorize.StatusCode, authorize.Header.Get("Location"))
	}
	callbackURL, err := url.Parse(authorize.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	form := callbackURL.Query()
	callbackURL.RawQuery = ""
	callback, err := client.PostForm(callbackURL.String(), form)
	if err != nil {
		t.Fatal(err)
	}
	callback.Body.Close()
	if callback.StatusCode != http.StatusSeeOther || callback.Header.Get("Location") != "/#/settings/account" {
		t.Fatalf("callback status=%d location=%q", callback.StatusCode, callback.Header.Get("Location"))
	}
	provider.mu.Lock()
	scope, responseMode, verifierSeen := provider.scope, provider.responseMode, provider.verifierSeen
	provider.mu.Unlock()
	if scope != "openid email name" || responseMode != "form_post" || !verifierSeen {
		t.Fatalf("Apple authorization scope=%q response_mode=%q verifier=%v", scope, responseMode, verifierSeen)
	}

	current, err := client.Get(server.URL + "/api/auth/user")
	if err != nil {
		t.Fatal(err)
	}
	defer current.Body.Close()
	var currentBody struct {
		Success bool `json:"success"`
		User    struct {
			Provider string `json:"provider"`
		} `json:"user"`
	}
	if err := json.NewDecoder(current.Body).Decode(&currentBody); err != nil {
		t.Fatal(err)
	}
	if current.StatusCode != http.StatusOK || !currentBody.Success || currentBody.User.Provider != webAppleProviderID {
		t.Fatalf("current status=%d body=%#v", current.StatusCode, currentBody)
	}
}

func TestWebOIDCReturnToRejectsOpenRedirects(t *testing.T) {
	for _, value := range []string{
		"https://attacker.example/", "//attacker.example/path", "///attacker.example/path",
		"/\\attacker.example/path", "/api/auth/oidc/google/callback", "javascript:alert(1)",
	} {
		if actual := sanitizeWebAuthReturnTo(value); actual != "/#/" {
			t.Fatalf("value=%q return=%q", value, actual)
		}
	}
	if actual := sanitizeWebAuthReturnTo("/#/settings/account"); actual != "/#/settings/account" {
		t.Fatalf("safe return = %q", actual)
	}
}

func TestWebOIDCRemoteOriginRequiresConfidentialClient(t *testing.T) {
	_, err := newWebExternalAuthRuntime(WebAuthOptions{
		PublicBaseURL: "https://biomed.example",
		Google:        WebOIDCProviderOptions{ClientID: "public-client"},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "confidential client secret") {
		t.Fatalf("newWebExternalAuthRuntime() error = %v", err)
	}
	if _, err := newWebExternalAuthRuntime(WebAuthOptions{
		Google: WebOIDCProviderOptions{ClientID: "loopback-public-client"},
	}, nil); err != nil {
		t.Fatalf("loopback PKCE client error = %v", err)
	}
}
