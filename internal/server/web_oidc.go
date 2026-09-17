package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/subtle"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"
)

const (
	webGoogleProviderID = "google"
	webGoogleIssuerURL  = "https://accounts.google.com"
	webAppleProviderID  = "apple"
	webAppleIssuerURL   = "https://appleid.apple.com"
	webOIDCRoutePrefix  = "/api/auth/oidc/"
)

type webOIDCProvider struct {
	id               string
	displayName      string
	issuerURL        string
	clientID         string
	clientSecret     string
	clientSecretFunc func(time.Time) (string, error)
	scopes           []string
	responseMode     string
	httpClient       *http.Client

	mu       sync.Mutex
	provider *oidc.Provider
}

func newWebGoogleOIDCProvider(
	options WebOIDCProviderOptions,
	httpClient *http.Client,
	remote bool,
) (*webOIDCProvider, error) {
	options.ClientID = strings.TrimSpace(options.ClientID)
	options.ClientSecret = strings.TrimSpace(options.ClientSecret)
	options.IssuerURL = strings.TrimSpace(options.IssuerURL)
	if options.ClientID == "" {
		if options.ClientSecret != "" {
			return nil, errors.New("Google OIDC client secret requires a client ID")
		}
		return nil, nil
	}
	if remote && options.ClientSecret == "" {
		return nil, errors.New("remote Google OIDC requires a confidential client secret")
	}
	if options.IssuerURL == "" {
		options.IssuerURL = webGoogleIssuerURL
	}
	issuer, err := url.Parse(options.IssuerURL)
	if err != nil || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" {
		return nil, errors.New("web_auth.google.issuer_url is invalid")
	}
	if issuer.Scheme != "https" && !isLoopbackURLHost(issuer.Host) {
		return nil, errors.New("web_auth.google.issuer_url must use HTTPS outside controlled loopback tests")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: webAuthOperationTimeout}
	}
	return newWebOIDCProvider(webGoogleProviderID, "Google", issuer.String(), options.ClientID,
		options.ClientSecret, nil, []string{oidc.ScopeOpenID, oidc.ScopeProfile, oidc.ScopeEmail}, "", httpClient), nil
}

func newWebAppleOIDCProvider(
	options WebAppleOIDCProviderOptions,
	httpClient *http.Client,
	remote bool,
) (*webOIDCProvider, error) {
	options.IssuerURL = strings.TrimSpace(options.IssuerURL)
	options.ClientID = strings.TrimSpace(options.ClientID)
	options.TeamID = strings.TrimSpace(options.TeamID)
	options.KeyID = strings.TrimSpace(options.KeyID)
	options.PrivateKey = strings.TrimSpace(options.PrivateKey)
	if options.ClientID == "" && options.TeamID == "" && options.KeyID == "" && options.PrivateKey == "" {
		return nil, nil
	}
	if options.ClientID == "" || options.TeamID == "" || options.KeyID == "" || options.PrivateKey == "" {
		return nil, errors.New("Apple Sign in with Apple requires client ID, team ID, key ID, and private key")
	}
	key, err := parseApplePrivateKey(options.PrivateKey)
	if err != nil {
		return nil, err
	}
	if options.IssuerURL == "" {
		options.IssuerURL = webAppleIssuerURL
	}
	issuer, err := url.Parse(options.IssuerURL)
	if err != nil || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" {
		return nil, errors.New("web_auth.apple.issuer_url is invalid")
	}
	if issuer.Scheme != "https" && !isLoopbackURLHost(issuer.Host) {
		return nil, errors.New("web_auth.apple.issuer_url must use HTTPS outside controlled loopback tests")
	}
	if remote && issuer.String() != webAppleIssuerURL && !isLoopbackURLHost(issuer.Host) {
		return nil, errors.New("remote Apple OIDC must use the official Apple issuer")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: webAuthOperationTimeout}
	}
	responseMode := ""
	if remote {
		responseMode = "form_post"
	}
	return newWebOIDCProvider(webAppleProviderID, "Apple", issuer.String(), options.ClientID, "",
		func(now time.Time) (string, error) {
			return createAppleClientSecret(key, options.TeamID, options.KeyID, options.ClientID, now)
		}, []string{oidc.ScopeOpenID, oidc.ScopeEmail, "name"}, responseMode, httpClient), nil
}

func newWebOIDCProvider(
	id string,
	displayName string,
	issuerURL string,
	clientID string,
	clientSecret string,
	clientSecretFunc func(time.Time) (string, error),
	scopes []string,
	responseMode string,
	httpClient *http.Client,
) *webOIDCProvider {
	return &webOIDCProvider{
		id: id, displayName: displayName, issuerURL: issuerURL,
		clientID: clientID, clientSecret: clientSecret, clientSecretFunc: clientSecretFunc,
		scopes: append([]string(nil), scopes...), responseMode: responseMode, httpClient: httpClient,
	}
}

func parseApplePrivateKey(value string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(value))
	if block == nil {
		return nil, errors.New("web_auth.apple.private_key must be PEM encoded")
	}
	var key any
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		key, err = x509.ParseECPrivateKey(block.Bytes)
	}
	if err != nil {
		return nil, errors.New("web_auth.apple.private_key is not a valid EC private key")
	}
	ecdsaKey, ok := key.(*ecdsa.PrivateKey)
	if !ok || ecdsaKey.Curve != elliptic.P256() {
		return nil, errors.New("web_auth.apple.private_key must use the P-256 curve")
	}
	return ecdsaKey, nil
}

func createAppleClientSecret(
	key *ecdsa.PrivateKey,
	teamID string,
	keyID string,
	clientID string,
	now time.Time,
) (string, error) {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, (&jose.SignerOptions{}).
		WithType("JWT").WithHeader("kid", keyID))
	if err != nil {
		return "", fmt.Errorf("create Apple client secret signer: %w", err)
	}
	payload, err := json.Marshal(map[string]any{
		"iss": teamID,
		"iat": now.UTC().Unix(),
		"exp": now.UTC().Add(5 * time.Minute).Unix(),
		"aud": webAppleIssuerURL,
		"sub": clientID,
	})
	if err != nil {
		return "", fmt.Errorf("encode Apple client secret claims: %w", err)
	}
	signed, err := signer.Sign(payload)
	if err != nil {
		return "", fmt.Errorf("sign Apple client secret: %w", err)
	}
	return signed.CompactSerialize()
}

func (p *webOIDCProvider) clientSecretValue(now time.Time) (string, error) {
	if p.clientSecretFunc != nil {
		return p.clientSecretFunc(now)
	}
	return p.clientSecret, nil
}

func (p *webOIDCProvider) isApple() bool {
	return p != nil && p.id == webAppleProviderID
}

func (p *webOIDCProvider) discovered(ctx context.Context) (*oidc.Provider, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.provider != nil {
		return p.provider, nil
	}
	ctx = oidc.ClientContext(ctx, p.httpClient)
	provider, err := oidc.NewProvider(ctx, p.issuerURL)
	if err != nil {
		return nil, fmt.Errorf("discover %s OIDC provider: %w", p.displayName, err)
	}
	p.provider = provider
	return provider, nil
}

func (s *Server) handleWebOIDC(w http.ResponseWriter, r *http.Request) {
	setWebExternalAuthResponseHeaders(w)
	tail := strings.TrimPrefix(r.URL.Path, webOIDCRoutePrefix)
	parts := strings.Split(strings.Trim(tail, "/"), "/")
	if len(parts) != 2 || s.webExternalAuth == nil {
		http.NotFound(w, r)
		return
	}
	providerID, operation := parts[0], parts[1]
	provider := s.webExternalAuth.oidcProviders[providerID]
	if provider == nil {
		s.redirectWebAuthError(w, r, "provider_unavailable")
		return
	}
	if r.Method != http.MethodGet && !(r.Method == http.MethodPost && operation == "callback" && provider.isApple()) {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "GET is required"})
		return
	}
	switch operation {
	case "start":
		s.handleWebOIDCStart(w, r, provider)
	case "callback":
		s.handleWebOIDCCallback(w, r, provider)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleWebOIDCStart(w http.ResponseWriter, r *http.Request, provider *webOIDCProvider) {
	baseURL, err := s.webExternalAuth.requestBaseURL(r)
	if err != nil {
		s.redirectWebAuthError(w, r, "request_origin_invalid")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), webAuthOperationTimeout)
	defer cancel()
	discovered, err := provider.discovered(ctx)
	if err != nil {
		s.redirectWebAuthError(w, r, "provider_unavailable")
		return
	}
	clientSecret, err := provider.clientSecretValue(time.Now().UTC())
	if err != nil {
		s.redirectWebAuthError(w, r, "provider_unavailable")
		return
	}
	returnTo := sanitizeWebAuthReturnTo(r.URL.Query().Get("return_to"))
	redirectURL := baseURL + webOIDCRoutePrefix + provider.id + "/callback"
	transaction, err := s.webExternalAuth.transactions.Create(
		provider.id, redirectURL, returnTo, r.URL.Query().Get("remember") == "1",
	)
	if err != nil {
		s.redirectWebAuthError(w, r, "transaction_failed")
		return
	}
	config := oauth2.Config{
		ClientID: provider.clientID, ClientSecret: clientSecret,
		Endpoint: discovered.Endpoint(), RedirectURL: redirectURL,
		Scopes: provider.scopes,
	}
	http.SetCookie(w, &http.Cookie{
		Name: webAuthTransactionCookieName, Value: transaction.CookieToken, Path: webAuthTransactionCookiePath,
		MaxAge: int(webAuthTransactionTTL.Seconds()), Expires: transaction.ExpiresAt,
		HttpOnly: true, Secure: s.webCookieSecure(r), SameSite: webOIDCTransactionCookieSameSite(provider, s.webCookieSecure(r)),
	})
	authorizeURL := config.AuthCodeURL(
		transaction.State,
		oidc.Nonce(transaction.Nonce),
		oauth2.SetAuthURLParam("code_challenge", webPKCEChallenge(transaction.PKCEVerifier)),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
	if provider.responseMode != "" {
		query := authorizeURL
		parsed, parseErr := url.Parse(query)
		if parseErr != nil {
			s.redirectWebAuthError(w, r, "provider_unavailable")
			return
		}
		values := parsed.Query()
		values.Set("response_mode", provider.responseMode)
		parsed.RawQuery = values.Encode()
		authorizeURL = parsed.String()
	}
	http.Redirect(w, r, authorizeURL, http.StatusSeeOther)
}

func (s *Server) handleWebOIDCCallback(w http.ResponseWriter, r *http.Request, provider *webOIDCProvider) {
	cookie, cookieErr := r.Cookie(webAuthTransactionCookieName)
	transaction, transactionErr := s.webExternalAuth.transactions.Consume(
		r.FormValue("state"), webCookieValue(cookie, cookieErr),
	)
	s.clearWebOIDCTransactionCookie(w, r, provider)
	if transactionErr != nil || transaction.ProviderID != provider.id {
		s.redirectWebAuthError(w, r, "transaction_invalid")
		return
	}
	if strings.TrimSpace(r.FormValue("error")) != "" {
		s.redirectWebAuthError(w, r, "provider_cancelled")
		return
	}
	code := strings.TrimSpace(r.FormValue("code"))
	if code == "" {
		s.redirectWebAuthError(w, r, "authorization_code_missing")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), webAuthOperationTimeout)
	defer cancel()
	discovered, err := provider.discovered(ctx)
	if err != nil {
		s.redirectWebAuthError(w, r, "provider_unavailable")
		return
	}
	clientSecret, err := provider.clientSecretValue(time.Now().UTC())
	if err != nil {
		s.redirectWebAuthError(w, r, "provider_unavailable")
		return
	}
	config := oauth2.Config{
		ClientID: provider.clientID, ClientSecret: clientSecret,
		Endpoint: discovered.Endpoint(), RedirectURL: transaction.RedirectURL,
		Scopes: provider.scopes,
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, provider.httpClient)
	token, err := config.Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", transaction.PKCEVerifier))
	if err != nil {
		s.redirectWebAuthError(w, r, "token_exchange_failed")
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || strings.TrimSpace(rawIDToken) == "" {
		s.redirectWebAuthError(w, r, "id_token_missing")
		return
	}
	idToken, err := discovered.Verifier(&oidc.Config{ClientID: provider.clientID}).Verify(ctx, rawIDToken)
	if err != nil {
		s.redirectWebAuthError(w, r, "id_token_invalid")
		return
	}
	var claims struct {
		Nonce         string `json:"nonce"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err := idToken.Claims(&claims); err != nil ||
		subtle.ConstantTimeCompare([]byte(webSecretHash(claims.Nonce)), []byte(webSecretHash(transaction.Nonce))) != 1 {
		s.redirectWebAuthError(w, r, "id_token_claims_invalid")
		return
	}
	s.completeWebExternalAuthentication(w, r, provider.id, transaction, webExternalIdentityClaims{
		Provider: provider.id, Issuer: provider.issuerURL, Subject: idToken.Subject,
		Email: claims.Email, EmailVerified: claims.EmailVerified, DisplayName: claims.Name,
		RequireVerifiedEmail: true,
	})
}
