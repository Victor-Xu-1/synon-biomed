package server

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const webAuthOperationTimeout = 15 * time.Second

type WebOIDCProviderOptions struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
}

type WebAppleOIDCProviderOptions struct {
	IssuerURL  string
	ClientID   string
	TeamID     string
	KeyID      string
	PrivateKey string
}

type WebWeChatOAuthOptions struct {
	AppID          string
	AppSecret      string
	AuthorizeURL   string
	AccessTokenURL string
	UserInfoURL    string
}

type WebAuthOptions struct {
	PublicBaseURL string
	Google        WebOIDCProviderOptions
	Apple         WebAppleOIDCProviderOptions
	WeChat        WebWeChatOAuthOptions
}

type webExternalAuthProviderStatus struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Enabled     bool   `json:"enabled"`
	StartPath   string `json:"startPath"`
}

type webExternalAuthRuntime struct {
	publicBaseURL *url.URL
	oidcProviders map[string]*webOIDCProvider
	wechat        *webWeChatOAuthProvider
	transactions  *webAuthTransactionStore
}

func newWebExternalAuthRuntime(options WebAuthOptions, httpClient *http.Client) (*webExternalAuthRuntime, error) {
	runtime := &webExternalAuthRuntime{
		oidcProviders: map[string]*webOIDCProvider{},
		transactions:  newWebAuthTransactionStore(),
	}
	if strings.TrimSpace(options.PublicBaseURL) != "" {
		parsed, err := url.Parse(strings.TrimSpace(options.PublicBaseURL))
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
			parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
			return nil, errors.New("web_auth.public_base_url must be an HTTPS origin without path, query, credentials, or fragment")
		}
		parsed.Path = ""
		runtime.publicBaseURL = parsed
	}

	google, err := newWebGoogleOIDCProvider(options.Google, httpClient, runtime.publicBaseURL != nil)
	if err != nil {
		return nil, err
	}
	if google != nil {
		runtime.oidcProviders[google.id] = google
	}
	apple, err := newWebAppleOIDCProvider(options.Apple, httpClient, runtime.publicBaseURL != nil)
	if err != nil {
		return nil, err
	}
	if apple != nil {
		runtime.oidcProviders[apple.id] = apple
	}
	runtime.wechat, err = newWebWeChatOAuthProvider(options.WeChat, httpClient)
	if err != nil {
		return nil, err
	}
	return runtime, nil
}

func (r *webExternalAuthRuntime) enabled() bool {
	return r != nil && (len(r.oidcProviders) > 0 || r.wechat != nil)
}

func (r *webExternalAuthRuntime) statuses() []webExternalAuthProviderStatus {
	if r == nil {
		return []webExternalAuthProviderStatus{}
	}
	statuses := make([]webExternalAuthProviderStatus, 0, len(r.oidcProviders)+1)
	for _, providerID := range []string{webGoogleProviderID, webAppleProviderID} {
		provider, ok := r.oidcProviders[providerID]
		if !ok {
			continue
		}
		statuses = append(statuses, webExternalAuthProviderStatus{
			ID: provider.id, DisplayName: provider.displayName, Enabled: true,
			StartPath: webOIDCRoutePrefix + provider.id + "/start",
		})
	}
	if r.wechat != nil {
		statuses = append(statuses, webExternalAuthProviderStatus{
			ID: r.wechat.id, DisplayName: r.wechat.displayName, Enabled: true,
			StartPath: webWeChatOAuthRoutePrefix + "start",
		})
	}
	return statuses
}

func (r *webExternalAuthRuntime) requestBaseURL(request *http.Request) (string, error) {
	if r == nil {
		return "", errors.New("external authentication is not configured")
	}
	if r.publicBaseURL != nil {
		if !strings.EqualFold(strings.TrimSpace(request.Host), r.publicBaseURL.Host) {
			return "", errors.New("request host does not match configured public base URL")
		}
		if request.TLS == nil && !isLoopbackRequest(request) {
			return "", errors.New("public external authentication requests require TLS or a loopback reverse proxy")
		}
		return strings.TrimSuffix(r.publicBaseURL.String(), "/"), nil
	}
	if !isLoopbackRequest(request) || !isLoopbackURLHost(request.Host) {
		return "", errors.New("external authentication without public_base_url is restricted to loopback")
	}
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + request.Host, nil
}

func (s *Server) handleWebAuthProviders(w http.ResponseWriter, r *http.Request) {
	setWebExternalAuthResponseHeaders(w)
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "GET is required"})
		return
	}
	if s.webExternalAuthError != nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{
			"success": false, "message": "external authentication configuration is unavailable",
		})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"success":       true,
		"localPassword": s.synonLinkAuth != nil && s.synonLinkAuth.enabled,
		"providers":     s.webExternalAuth.statuses(),
	})
}

func setWebExternalAuthResponseHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func (s *Server) completeWebExternalAuthentication(
	w http.ResponseWriter,
	r *http.Request,
	providerID string,
	transaction webAuthTransaction,
	claims webExternalIdentityClaims,
) {
	if s.webAccounts == nil || s.webSessions == nil || s.webAccountsError != nil || s.webSessionsError != nil {
		s.redirectWebAuthError(w, r, "auth_store_unavailable")
		return
	}
	user, created, err := s.webAccounts.ResolveExternalIdentity(claims)
	if err != nil {
		var validation *webAccountValidationError
		if errors.As(err, &validation) {
			s.redirectWebAuthError(w, r, strings.ToLower(validation.code))
			return
		}
		s.redirectWebAuthError(w, r, "account_resolution_failed")
		return
	}
	metadata, deviceToken, err := s.webSessions.metadataForSessionCreation(r)
	if err != nil {
		s.redirectWebAuthError(w, r, "session_failed")
		return
	}
	sessionToken, csrfToken, session, err := s.webSessions.Create(user.ID, providerID, transaction.Remember, metadata)
	if err != nil {
		s.redirectWebAuthError(w, r, "session_failed")
		return
	}
	if created {
		_ = s.webSessions.RecordEvent(storedWebSecurityEvent{
			AccountID: user.ID, SessionID: session.ID, Type: "identity_created",
			AuthMethod: providerID, Success: true, UserAgent: session.UserAgent,
			NetworkClass: session.NetworkClass,
		})
	}
	s.writeWebSessionCookies(w, r, sessionToken, csrfToken, deviceToken, transaction.Remember, session.ExpiresAt)
	http.Redirect(w, r, transaction.ReturnTo, http.StatusSeeOther)
}

func (s *Server) webCookieSecure(r *http.Request) bool {
	return r.TLS != nil || (s.webExternalAuth != nil && s.webExternalAuth.publicBaseURL != nil &&
		s.webExternalAuth.publicBaseURL.Scheme == "https" &&
		strings.EqualFold(strings.TrimSpace(r.Host), s.webExternalAuth.publicBaseURL.Host))
}

func (s *Server) approvedPasswordAuthRequest(r *http.Request) bool {
	if isLoopbackRequest(r) {
		return true
	}
	return s.webExternalAuth != nil && s.webExternalAuth.publicBaseURL != nil &&
		r.TLS != nil &&
		strings.EqualFold(strings.TrimSpace(r.Host), s.webExternalAuth.publicBaseURL.Host)
}

func (s *Server) clearWebAuthTransactionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: webAuthTransactionCookieName, Value: "", Path: webAuthTransactionCookiePath,
		MaxAge: -1, Expires: time.Unix(1, 0).UTC(), HttpOnly: true,
		Secure: s.webCookieSecure(r), SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearWebOIDCTransactionCookie(w http.ResponseWriter, r *http.Request, provider *webOIDCProvider) {
	http.SetCookie(w, &http.Cookie{
		Name: webAuthTransactionCookieName, Value: "", Path: webAuthTransactionCookiePath,
		MaxAge: -1, Expires: time.Unix(1, 0).UTC(), HttpOnly: true,
		Secure: s.webCookieSecure(r), SameSite: webOIDCTransactionCookieSameSite(provider, s.webCookieSecure(r)),
	})
}

func webOIDCTransactionCookieSameSite(provider *webOIDCProvider, secure bool) http.SameSite {
	if provider != nil && provider.isApple() && secure {
		return http.SameSiteNoneMode
	}
	return http.SameSiteLaxMode
}

func (s *Server) redirectWebAuthError(w http.ResponseWriter, r *http.Request, code string) {
	location := &url.URL{Path: "/", RawQuery: "auth_error=" + url.QueryEscape(code), Fragment: "/login"}
	http.Redirect(w, r, location.String(), http.StatusSeeOther)
}

func sanitizeWebAuthReturnTo(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "\r\n\\") {
		return "/#/"
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.User != nil ||
		!strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(parsed.Path, "//") ||
		strings.HasPrefix(parsed.Path, webAuthTransactionCookiePath) {
		return "/#/"
	}
	return parsed.String()
}

func isWebExternalAuthRequestPath(path string) bool {
	return strings.HasPrefix(path, webOIDCRoutePrefix) || strings.HasPrefix(path, webWeChatOAuthRoutePrefix)
}

func isLoopbackURLHost(hostport string) bool {
	host := strings.Trim(strings.TrimSpace(hostport), "[]")
	if parsedHost, _, err := net.SplitHostPort(hostport); err == nil {
		host = strings.Trim(parsedHost, "[]")
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func webCookieValue(cookie *http.Cookie, err error) string {
	if err != nil || cookie == nil {
		return ""
	}
	return cookie.Value
}
