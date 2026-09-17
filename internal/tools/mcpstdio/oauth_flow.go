package mcpstdio

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

func discoverOAuthAuthorization(ctx context.Context, config ServerConfig) (oauthDiscovery, error) {
	request := rpcMessage{JSONRPC: "2.0", ID: 1, Method: "server/discover", Params: discoverParams(latestProtocolVersion)}
	rawRequest, err := json.Marshal(request)
	if err != nil {
		return oauthDiscovery{}, err
	}
	if err := validateOAuthEndpoint(config.URL, "MCP OAuth resource"); err != nil {
		return oauthDiscovery{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(config.URL), bytes.NewReader(rawRequest))
	if err != nil {
		return oauthDiscovery{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", latestProtocolVersion)
	req.Header.Set("Mcp-Method", "server/discover")
	for key, value := range config.Headers {
		if strings.TrimSpace(key) != "" {
			req.Header.Set(key, value)
		}
	}
	resp, err := doOAuthRequest(ctx, req)
	if err != nil {
		return oauthDiscovery{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		return oauthDiscovery{}, fmt.Errorf("MCP server %q did not request OAuth authentication; HTTP %d", config.Name, resp.StatusCode)
	}
	return oauthDiscoveryFromAuthenticateHeader(ctx, resp.Header.Get("WWW-Authenticate"), config.URL)
}

func discoverOAuthMetadataURL(ctx context.Context, config ServerConfig) (string, error) {
	discovery, err := discoverOAuthAuthorization(ctx, config)
	if err != nil {
		return "", err
	}
	return discovery.MetadataURLs[0], nil
}

func oauthMetadataURLFromAuthenticateHeader(ctx context.Context, header string) (string, error) {
	discovery, err := oauthDiscoveryFromAuthenticateHeader(ctx, header, "")
	if err != nil {
		return "", err
	}
	return discovery.MetadataURLs[0], nil
}

func oauthDiscoveryFromAuthenticateHeader(ctx context.Context, header, defaultResource string) (oauthDiscovery, error) {
	discovery := oauthDiscovery{Resource: strings.TrimSpace(defaultResource)}
	if rawScope := quotedAuthParam(header, "scope"); rawScope != "" {
		if len(rawScope) > 4096 || strings.ContainsAny(rawScope, "\r\n\x00") {
			return oauthDiscovery{}, errors.New("MCP OAuth scope challenge is invalid")
		}
		discovery.Scope = strings.Join(strings.Fields(rawScope), " ")
	}
	if resource := quotedAuthParam(header, "resource_metadata"); resource != "" {
		metadata, err := fetchResourceMetadata(ctx, resource)
		if err != nil {
			return oauthDiscovery{}, err
		}
		if declaredResource, ok := metadata["resource"].(string); ok && strings.TrimSpace(declaredResource) != "" {
			declaredResource = strings.TrimSpace(declaredResource)
			if err := validateOAuthEndpoint(declaredResource, "MCP OAuth protected resource"); err != nil {
				return oauthDiscovery{}, err
			}
			if discovery.Resource != "" {
				configuredOrigin, configuredErr := canonicalRemoteMCPOrigin(discovery.Resource)
				declaredOrigin, declaredErr := canonicalRemoteMCPOrigin(declaredResource)
				if configuredErr != nil || declaredErr != nil || configuredOrigin != declaredOrigin {
					return oauthDiscovery{}, errors.New("MCP OAuth protected resource origin does not match the connector")
				}
			}
			discovery.Resource = declaredResource
		}
		if value, ok := metadata["authorization_metadata"].(string); ok && strings.TrimSpace(value) != "" {
			discovery.MetadataURLs = append(discovery.MetadataURLs, strings.TrimSpace(value))
		}
		if servers, ok := metadata["authorization_servers"].([]any); ok && len(servers) > 0 {
			for _, rawIssuer := range servers {
				issuer, ok := rawIssuer.(string)
				if !ok || strings.TrimSpace(issuer) == "" {
					continue
				}
				urls, err := oauthMetadataURLsForIssuer(issuer)
				if err != nil {
					return oauthDiscovery{}, err
				}
				discovery.MetadataURLs = append(discovery.MetadataURLs, urls...)
			}
		}
	}
	for _, key := range []string{"authorization_metadata", "authorization_uri"} {
		if value := quotedAuthParam(header, key); value != "" {
			discovery.MetadataURLs = append(discovery.MetadataURLs, value)
		}
	}
	discovery.MetadataURLs = uniqueOAuthMetadataURLs(discovery.MetadataURLs)
	if len(discovery.MetadataURLs) == 0 {
		return oauthDiscovery{}, errors.New("MCP server did not provide OAuth metadata in WWW-Authenticate")
	}
	if discovery.Resource == "" {
		discovery.Resource = strings.TrimSpace(defaultResource)
	}
	return discovery, nil
}

func oauthMetadataURLsForIssuer(issuer string) ([]string, error) {
	if err := validateOAuthEndpoint(issuer, "MCP OAuth authorization server issuer"); err != nil {
		return nil, err
	}
	parsed, err := url.Parse(strings.TrimSpace(issuer))
	if err != nil {
		return nil, err
	}
	issuerPath := strings.TrimRight(parsed.Path, "/")
	parsed.RawQuery = ""
	parsed.Path = "/.well-known/oauth-authorization-server" + issuerPath
	parsed.RawPath = ""
	oauthURL := parsed.String()
	parsed.Path = strings.TrimRight(issuerPath, "/") + "/.well-known/openid-configuration"
	if parsed.Path == "/.well-known/openid-configuration" {
		parsed.Path = "/.well-known/openid-configuration"
	}
	return []string{oauthURL, parsed.String()}, nil
}

func uniqueOAuthMetadataURLs(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if err := validateOAuthEndpoint(value, "MCP OAuth metadata"); err != nil {
			continue
		}
		if _, found := seen[value]; found {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func quotedAuthParam(header string, key string) string {
	pattern := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(key) + `="([^"]+)"`)
	match := pattern.FindStringSubmatch(header)
	if len(match) == 2 {
		return strings.TrimSpace(match[1])
	}
	return ""
}

func fetchResourceMetadata(ctx context.Context, metadataURL string) (map[string]any, error) {
	if err := validateOAuthEndpoint(metadataURL, "MCP OAuth resource metadata"); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := doOAuthRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch MCP OAuth resource metadata failed with HTTP %d", resp.StatusCode)
	}
	var decoded map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRemoteResponseBytes)).Decode(&decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func fetchOAuthMetadata(ctx context.Context, metadataURL string) (oauthMetadata, error) {
	if err := validateOAuthEndpoint(metadataURL, "MCP OAuth metadata"); err != nil {
		return oauthMetadata{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return oauthMetadata{}, err
	}
	resp, err := doOAuthRequest(ctx, req)
	if err != nil {
		return oauthMetadata{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return oauthMetadata{}, fmt.Errorf("fetch MCP OAuth metadata failed with HTTP %d", resp.StatusCode)
	}
	var metadata oauthMetadata
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRemoteResponseBytes)).Decode(&metadata); err != nil {
		return oauthMetadata{}, err
	}
	if strings.TrimSpace(metadata.AuthorizationEndpoint) == "" || strings.TrimSpace(metadata.TokenEndpoint) == "" {
		return oauthMetadata{}, errors.New("MCP OAuth metadata missing authorization_endpoint or token_endpoint")
	}
	if err := validateResolvedOAuthEndpoint(ctx, metadata.AuthorizationEndpoint, "MCP OAuth authorization endpoint"); err != nil {
		return oauthMetadata{}, err
	}
	if err := validateResolvedOAuthEndpoint(ctx, metadata.TokenEndpoint, "MCP OAuth token endpoint"); err != nil {
		return oauthMetadata{}, err
	}
	if metadata.RegistrationEndpoint != "" {
		if err := validateResolvedOAuthEndpoint(ctx, metadata.RegistrationEndpoint, "MCP OAuth registration endpoint"); err != nil {
			return oauthMetadata{}, err
		}
	}
	if metadata.Issuer != "" {
		if err := validateOAuthEndpoint(metadata.Issuer, "MCP OAuth issuer"); err != nil {
			return oauthMetadata{}, err
		}
	}
	return metadata, nil
}

func selectOAuthMetadata(ctx context.Context, metadataURLs []string) (oauthMetadata, error) {
	var fallback *oauthMetadata
	var failures []string
	for _, metadataURL := range uniqueOAuthMetadataURLs(metadataURLs) {
		metadata, err := fetchOAuthMetadata(ctx, metadataURL)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		if len(metadata.CodeChallengeMethodsSupported) > 0 && !containsOAuthValue(metadata.CodeChallengeMethodsSupported, "S256") {
			failures = append(failures, "authorization server does not support PKCE S256")
			continue
		}
		if metadata.RegistrationEndpoint != "" {
			return metadata, nil
		}
		if fallback == nil {
			copy := metadata
			fallback = &copy
		}
	}
	if fallback != nil {
		return *fallback, nil
	}
	if len(failures) == 0 {
		return oauthMetadata{}, errors.New("MCP OAuth discovery returned no usable authorization server")
	}
	return oauthMetadata{}, fmt.Errorf("MCP OAuth discovery returned no usable authorization server: %s", strings.Join(failures, "; "))
}

func startOAuthCallbackServer() (string, *http.Server, <-chan oauthCallbackResult, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, nil, err
	}
	callbacks := make(chan oauthCallbackResult, 1)
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		result := oauthCallbackResult{
			Code: r.URL.Query().Get("code"), State: r.URL.Query().Get("state"), ISS: r.URL.Query().Get("iss"),
		}
		if errorCode := r.URL.Query().Get("error"); errorCode != "" {
			result.Err = fmt.Errorf("OAuth error: %s", errorCode)
		}
		once.Do(func() { callbacks <- result })
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("MCP OAuth authorization received. You can close this window."))
	})
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(listener) }()
	return "http://" + listener.Addr().String() + "/callback", server, callbacks, nil
}

func buildOAuthAuthURL(endpoint string, resource string, redirectURI string, state string, challenge string) (string, error) {
	return buildOAuthAuthURLForClient(endpoint, resource, redirectURI, state, challenge, "", "synon-go")
}

func buildOAuthAuthURLForClient(endpoint, resource, redirectURI, state, challenge, scope, clientID string) (string, error) {
	if err := validateOAuthCallbackURI(redirectURI); err != nil {
		return "", err
	}
	if err := validateOAuthEndpoint(resource, "MCP OAuth resource"); err != nil {
		return "", err
	}
	if err := validateOAuthEndpoint(endpoint, "MCP OAuth authorization endpoint"); err != nil {
		return "", err
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" || len(clientID) > 16*1024 || strings.ContainsAny(clientID, "\r\n\x00") {
		return "", errors.New("MCP OAuth client_id is invalid")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("response_type", "code")
	query.Set("client_id", clientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("state", state)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	query.Set("resource", resource)
	if scope = strings.Join(strings.Fields(scope), " "); scope != "" {
		query.Set("scope", scope)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func registerOAuthClient(ctx context.Context, metadata oauthMetadata, redirectURI string) (oauthClientCredentials, error) {
	if err := validateOAuthCallbackURI(redirectURI); err != nil {
		return oauthClientCredentials{}, err
	}
	if strings.TrimSpace(metadata.RegistrationEndpoint) == "" {
		return oauthClientCredentials{ClientID: "synon-go", TokenEndpointAuthMethod: "none"}, nil
	}
	method := preferredOAuthClientAuthMethod(metadata.TokenEndpointAuthMethodsSupported)
	if method == "" {
		return oauthClientCredentials{}, errors.New("MCP OAuth authorization server has no supported dynamic-client authentication method")
	}
	payload := map[string]any{
		"client_name":                "Synon Biomed",
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": method,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return oauthClientCredentials{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, metadata.RegistrationEndpoint, bytes.NewReader(raw))
	if err != nil {
		return oauthClientCredentials{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := doOAuthRequest(ctx, req)
	if err != nil {
		return oauthClientCredentials{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return oauthClientCredentials{}, fmt.Errorf("MCP OAuth dynamic client registration failed with HTTP %d", resp.StatusCode)
	}
	var decoded struct {
		ClientID                string `json:"client_id"`
		ClientSecret            string `json:"client_secret"`
		TokenEndpointAuthMethod string `json:"token_endpoint_auth_method"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRemoteResponseBytes)).Decode(&decoded); err != nil {
		return oauthClientCredentials{}, err
	}
	credentials := oauthClientCredentials{
		ClientID: strings.TrimSpace(decoded.ClientID), ClientSecret: strings.TrimSpace(decoded.ClientSecret),
		TokenEndpointAuthMethod: strings.TrimSpace(decoded.TokenEndpointAuthMethod),
	}
	if credentials.TokenEndpointAuthMethod == "" {
		credentials.TokenEndpointAuthMethod = method
	}
	if credentials.ClientID == "" || len(credentials.ClientID) > 16*1024 || strings.ContainsAny(credentials.ClientID, "\r\n\x00") {
		return oauthClientCredentials{}, errors.New("MCP OAuth dynamic client registration response missing a valid client_id")
	}
	if !containsOAuthValue([]string{"none", "client_secret_post", "client_secret_basic"}, credentials.TokenEndpointAuthMethod) {
		return oauthClientCredentials{}, fmt.Errorf("MCP OAuth dynamic client registration returned unsupported token authentication method %q", credentials.TokenEndpointAuthMethod)
	}
	if credentials.TokenEndpointAuthMethod != "none" && (credentials.ClientSecret == "" || len(credentials.ClientSecret) > 16*1024 || strings.ContainsAny(credentials.ClientSecret, "\r\n\x00")) {
		return oauthClientCredentials{}, errors.New("MCP OAuth dynamic client registration response missing a valid client_secret")
	}
	return credentials, nil
}

func preferredOAuthClientAuthMethod(methods []string) string {
	for _, preferred := range []string{"none", "client_secret_post", "client_secret_basic"} {
		if containsOAuthValue(methods, preferred) {
			return preferred
		}
	}
	return ""
}

func containsOAuthValue(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), wanted) {
			return true
		}
	}
	return false
}

func completeOAuthAuthorization(ctx context.Context, root string, serverName string, metadata oauthMetadata, credentials oauthClientCredentials, resource string, redirectURI string, state string, verifier string, server *http.Server, callbacks <-chan oauthCallbackResult) {
	defer func() { _ = server.Shutdown(context.Background()) }()
	select {
	case callback := <-callbacks:
		if callback.Err != nil || callback.Code == "" || callback.State != state {
			return
		}
		if metadata.AuthorizationResponseISSParameterSupported && !sameOAuthIssuer(callback.ISS, metadata.Issuer) {
			return
		}
		token, err := exchangeOAuthCodeForClient(ctx, metadata.TokenEndpoint, resource, callback.Code, redirectURI, verifier, credentials)
		if err != nil {
			return
		}
		_ = saveOAuthTokenForRemoteOrigin(root, serverName, resource, token)
	case <-time.After(2 * time.Minute):
		return
	}
}

func exchangeOAuthCode(ctx context.Context, tokenEndpoint string, resource string, code string, redirectURI string, verifier string) (oauthTokenCache, error) {
	return exchangeOAuthCodeForClient(ctx, tokenEndpoint, resource, code, redirectURI, verifier, oauthClientCredentials{ClientID: "synon-go", TokenEndpointAuthMethod: "none"})
}

func exchangeOAuthCodeForClient(ctx context.Context, tokenEndpoint string, resource string, code string, redirectURI string, verifier string, credentials oauthClientCredentials) (oauthTokenCache, error) {
	if err := validateOAuthCallbackURI(redirectURI); err != nil {
		return oauthTokenCache{}, err
	}
	if err := validateOAuthEndpoint(resource, "MCP OAuth resource"); err != nil {
		return oauthTokenCache{}, err
	}
	if err := validateOAuthEndpoint(tokenEndpoint, "MCP OAuth token endpoint"); err != nil {
		return oauthTokenCache{}, err
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", credentials.ClientID)
	form.Set("code_verifier", verifier)
	form.Set("resource", resource)
	method := strings.TrimSpace(credentials.TokenEndpointAuthMethod)
	if method == "" {
		method = "none"
	}
	if method == "client_secret_post" {
		form.Set("client_secret", credentials.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return oauthTokenCache{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if method == "client_secret_basic" {
		req.SetBasicAuth(credentials.ClientID, credentials.ClientSecret)
	}
	resp, err := doOAuthRequest(ctx, req)
	if err != nil {
		return oauthTokenCache{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return oauthTokenCache{}, fmt.Errorf("MCP OAuth token exchange failed with HTTP %d", resp.StatusCode)
	}
	var decoded struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRemoteResponseBytes)).Decode(&decoded); err != nil {
		return oauthTokenCache{}, err
	}
	if strings.TrimSpace(decoded.AccessToken) == "" {
		return oauthTokenCache{}, errors.New("MCP OAuth token response missing access_token")
	}
	if strings.TrimSpace(decoded.TokenType) == "" {
		decoded.TokenType = "Bearer"
	}
	expiresAt := time.Now().Add(time.Hour).Unix()
	if decoded.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(decoded.ExpiresIn) * time.Second).Unix()
	}
	return oauthTokenCache{AccessToken: decoded.AccessToken, TokenType: decoded.TokenType, ExpiresAt: expiresAt}, nil
}

func sameOAuthIssuer(callbackISS, metadataIssuer string) bool {
	callbackISS = strings.TrimRight(strings.TrimSpace(callbackISS), "/")
	metadataIssuer = strings.TrimRight(strings.TrimSpace(metadataIssuer), "/")
	return callbackISS != "" && metadataIssuer != "" && callbackISS == metadataIssuer
}

func doOAuthRequest(ctx context.Context, req *http.Request) (*http.Response, error) {
	if err := validateOAuthEndpoint(req.URL.String(), "MCP OAuth request"); err != nil {
		return nil, err
	}
	client, err := oauthHTTPClientForContext(ctx)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

func oauthHTTPClientForContext(ctx context.Context) (*http.Client, error) {
	baseClient := httpClientForContext(ctx)
	client := *baseClient
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	if pinned, ok := baseClient.Transport.(PublicHTTPSPinnedTransport); ok && pinned.PublicHTTPSPinned() {
		// The MCP directory transport already validates each OAuth endpoint and
		// pins its resolved public address. Replacing it here would discard that
		// contract and make an otherwise valid connector fail during OAuth.
		return &client, nil
	}

	var transport *http.Transport
	switch baseTransport := baseClient.Transport.(type) {
	case nil:
		transport = http.DefaultTransport.(*http.Transport).Clone()
	case *http.Transport:
		transport = baseTransport.Clone()
	default:
		return nil, fmt.Errorf("MCP OAuth HTTP client transport %T cannot enforce address pinning", baseClient.Transport)
	}
	if transport.DialTLSContext != nil {
		return nil, errors.New("MCP OAuth HTTP client cannot use a custom DialTLSContext")
	}
	originalDial := transport.DialContext
	if originalDial == nil {
		dialer := &net.Dialer{Timeout: defaultTimeout, KeepAlive: 30 * time.Second}
		originalDial = dialer.DialContext
	}
	transport.Proxy = nil
	transport.DialContext = func(dialCtx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("invalid MCP OAuth dial address %q: %w", address, err)
		}
		pinned, err := resolvePublicOAuthAddress(dialCtx, host)
		if err != nil {
			return nil, err
		}
		return originalDial(dialCtx, network, net.JoinHostPort(pinned.String(), port))
	}
	client.Transport = transport
	return &client, nil
}

func resolvePublicOAuthAddress(ctx context.Context, host string) (net.IP, error) {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return nil, fmt.Errorf("MCP OAuth destination %q is local", host)
	}
	if literal := net.ParseIP(host); literal != nil {
		if !isPublicOAuthIP(literal) {
			return nil, fmt.Errorf("MCP OAuth destination %q is loopback, private, link-local, or reserved", host)
		}
		return literal, nil
	}
	addresses, err := oauthResolverForContext(ctx).LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve MCP OAuth destination %q: %w", host, err)
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("MCP OAuth destination %q resolved to no addresses", host)
	}
	for _, address := range addresses {
		if !isPublicOAuthIP(address.IP) {
			return nil, fmt.Errorf("MCP OAuth destination %q resolved to loopback, private, link-local, or reserved address %s", host, address.IP)
		}
	}
	return addresses[0].IP, nil
}

var reservedOAuthNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fec0::/10"),
}

func isPublicOAuthIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsUnspecified() {
		return false
	}
	for _, network := range reservedOAuthNetworks {
		if network.Contains(address) {
			return false
		}
	}
	return true
}

func randomURLToken(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func validateOAuthEndpoint(value, label string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("%s must be an absolute HTTPS URL without credentials or fragment", label)
	}
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(parsed.Hostname())), ".")
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return fmt.Errorf("%s must not target a local host", label)
	}
	if address := net.ParseIP(host); address != nil && !isPublicOAuthIP(address) {
		return fmt.Errorf("%s must not target a loopback, private, link-local, or reserved address", label)
	}
	return nil
}

func validateResolvedOAuthEndpoint(ctx context.Context, value, label string) error {
	if err := validateOAuthEndpoint(value, label); err != nil {
		return err
	}
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("parse %s: %w", label, err)
	}
	if _, err := resolvePublicOAuthAddress(ctx, parsed.Hostname()); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

func validateOAuthCallbackURI(value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" {
		return errors.New("MCP OAuth callback must be an HTTP IPv4 loopback URL")
	}
	if parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" || parsed.Path != "/callback" {
		return errors.New("MCP OAuth callback must use http://127.0.0.1:<port>/callback")
	}
	port, err := strconv.ParseUint(parsed.Port(), 10, 16)
	if err != nil || port == 0 {
		return errors.New("MCP OAuth callback must include a valid loopback port")
	}
	return nil
}
