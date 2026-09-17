package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	webWeChatProviderID         = "wechat"
	webWeChatDisplayName        = "WeChat"
	webWeChatOAuthRoutePrefix   = "/api/auth/oauth/wechat/"
	webWeChatAuthorizeURL       = "https://open.weixin.qq.com/connect/qrconnect"
	webWeChatAccessTokenURL     = "https://api.weixin.qq.com/sns/oauth2/access_token"
	webWeChatUserInfoURL        = "https://api.weixin.qq.com/sns/userinfo"
	webWeChatOAuthScope         = "snsapi_login"
	webWeChatResponseLimitBytes = 64 * 1024
)

type webWeChatOAuthProvider struct {
	id             string
	displayName    string
	appID          string
	appSecret      string
	authorizeURL   string
	accessTokenURL string
	userInfoURL    string
	httpClient     *http.Client
}

type webWeChatTokenResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	OpenID       string `json:"openid"`
	Scope        string `json:"scope"`
	UnionID      string `json:"unionid"`
	ErrorCode    int    `json:"errcode"`
	ErrorMessage string `json:"errmsg"`
}

type webWeChatUserInfoResponse struct {
	OpenID       string `json:"openid"`
	Nickname     string `json:"nickname"`
	UnionID      string `json:"unionid"`
	ErrorCode    int    `json:"errcode"`
	ErrorMessage string `json:"errmsg"`
}

func newWebWeChatOAuthProvider(
	options WebWeChatOAuthOptions,
	httpClient *http.Client,
) (*webWeChatOAuthProvider, error) {
	options.AppID = strings.TrimSpace(options.AppID)
	options.AppSecret = strings.TrimSpace(options.AppSecret)
	if options.AppID == "" && options.AppSecret == "" {
		return nil, nil
	}
	if options.AppID == "" || options.AppSecret == "" {
		return nil, errors.New("WeChat OAuth requires both AppID and AppSecret")
	}
	if len(options.AppID) > 512 || len(options.AppSecret) > 2048 {
		return nil, errors.New("WeChat OAuth credentials exceed the configured size limit")
	}

	authorizeURL, err := validatedWebWeChatEndpoint(options.AuthorizeURL, webWeChatAuthorizeURL, "authorize")
	if err != nil {
		return nil, err
	}
	accessTokenURL, err := validatedWebWeChatEndpoint(options.AccessTokenURL, webWeChatAccessTokenURL, "access token")
	if err != nil {
		return nil, err
	}
	userInfoURL, err := validatedWebWeChatEndpoint(options.UserInfoURL, webWeChatUserInfoURL, "user info")
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: webAuthOperationTimeout}
	if httpClient != nil {
		copyClient := *httpClient
		client = &copyClient
		if client.Timeout <= 0 || client.Timeout > webAuthOperationTimeout {
			client.Timeout = webAuthOperationTimeout
		}
	}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &webWeChatOAuthProvider{
		id: webWeChatProviderID, displayName: webWeChatDisplayName,
		appID: options.AppID, appSecret: options.AppSecret,
		authorizeURL: authorizeURL, accessTokenURL: accessTokenURL,
		userInfoURL: userInfoURL, httpClient: client,
	}, nil
}

func validatedWebWeChatEndpoint(value, fallback, label string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("web_auth.wechat %s URL is invalid", label)
	}
	if parsed.Scheme != "https" && !isLoopbackURLHost(parsed.Host) {
		return "", fmt.Errorf("web_auth.wechat %s URL must use HTTPS outside controlled loopback tests", label)
	}
	return parsed.String(), nil
}

func (s *Server) handleWebWeChatOAuth(w http.ResponseWriter, r *http.Request) {
	setWebExternalAuthResponseHeaders(w)
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "GET is required"})
		return
	}
	if s.webExternalAuth == nil || s.webExternalAuth.wechat == nil {
		s.redirectWebAuthError(w, r, "provider_unavailable")
		return
	}
	operation := strings.Trim(strings.TrimPrefix(r.URL.Path, webWeChatOAuthRoutePrefix), "/")
	switch operation {
	case "start":
		s.handleWebWeChatOAuthStart(w, r, s.webExternalAuth.wechat)
	case "callback":
		s.handleWebWeChatOAuthCallback(w, r, s.webExternalAuth.wechat)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleWebWeChatOAuthStart(
	w http.ResponseWriter,
	r *http.Request,
	provider *webWeChatOAuthProvider,
) {
	baseURL, err := s.webExternalAuth.requestBaseURL(r)
	if err != nil {
		s.redirectWebAuthError(w, r, "request_origin_invalid")
		return
	}
	redirectURL := baseURL + webWeChatOAuthRoutePrefix + "callback"
	transaction, err := s.webExternalAuth.transactions.Create(
		provider.id,
		redirectURL,
		sanitizeWebAuthReturnTo(r.URL.Query().Get("return_to")),
		r.URL.Query().Get("remember") == "1",
	)
	if err != nil {
		s.redirectWebAuthError(w, r, "transaction_failed")
		return
	}
	authorizeURL, err := url.Parse(provider.authorizeURL)
	if err != nil {
		s.redirectWebAuthError(w, r, "provider_unavailable")
		return
	}
	query := authorizeURL.Query()
	query.Set("appid", provider.appID)
	query.Set("redirect_uri", redirectURL)
	query.Set("response_type", "code")
	query.Set("scope", webWeChatOAuthScope)
	query.Set("state", transaction.State)
	authorizeURL.RawQuery = query.Encode()
	authorizeURL.Fragment = "wechat_redirect"
	http.SetCookie(w, &http.Cookie{
		Name: webAuthTransactionCookieName, Value: transaction.CookieToken, Path: webAuthTransactionCookiePath,
		MaxAge: int(webAuthTransactionTTL.Seconds()), Expires: transaction.ExpiresAt,
		HttpOnly: true, Secure: s.webCookieSecure(r), SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, authorizeURL.String(), http.StatusSeeOther)
}

func (s *Server) handleWebWeChatOAuthCallback(
	w http.ResponseWriter,
	r *http.Request,
	provider *webWeChatOAuthProvider,
) {
	cookie, cookieErr := r.Cookie(webAuthTransactionCookieName)
	transaction, transactionErr := s.webExternalAuth.transactions.Consume(
		r.URL.Query().Get("state"), webCookieValue(cookie, cookieErr),
	)
	s.clearWebAuthTransactionCookie(w, r)
	if transactionErr != nil || transaction.ProviderID != provider.id {
		s.redirectWebAuthError(w, r, "transaction_invalid")
		return
	}
	if strings.TrimSpace(r.URL.Query().Get("error")) != "" ||
		strings.TrimSpace(r.URL.Query().Get("errcode")) != "" {
		s.redirectWebAuthError(w, r, "provider_cancelled")
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		s.redirectWebAuthError(w, r, "authorization_code_missing")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), webAuthOperationTimeout)
	defer cancel()
	token, err := provider.exchangeAuthorizationCode(ctx, code)
	if err != nil {
		s.redirectWebAuthError(w, r, "token_exchange_failed")
		return
	}
	if !containsWeChatScope(token.Scope, webWeChatOAuthScope) {
		s.redirectWebAuthError(w, r, "provider_scope_invalid")
		return
	}
	if token.UnionID == "" {
		s.redirectWebAuthError(w, r, "wechat_unionid_required")
		return
	}
	profile, err := provider.fetchUserInfo(ctx, token.AccessToken, token.OpenID)
	if err != nil || profile.OpenID != token.OpenID || profile.UnionID != token.UnionID {
		s.redirectWebAuthError(w, r, "provider_profile_invalid")
		return
	}
	s.completeWebExternalAuthentication(w, r, provider.id, transaction, webExternalIdentityClaims{
		Provider:    provider.id,
		Issuer:      "https://open.weixin.qq.com/app/" + provider.appID,
		Subject:     token.UnionID,
		DisplayName: profile.Nickname,
	})
}

func (p *webWeChatOAuthProvider) exchangeAuthorizationCode(
	ctx context.Context,
	code string,
) (webWeChatTokenResponse, error) {
	var response webWeChatTokenResponse
	err := p.getJSON(ctx, p.accessTokenURL, url.Values{
		"appid":      {p.appID},
		"secret":     {p.appSecret},
		"code":       {code},
		"grant_type": {"authorization_code"},
	}, &response)
	if err != nil {
		return webWeChatTokenResponse{}, err
	}
	if response.ErrorCode != 0 || strings.TrimSpace(response.AccessToken) == "" ||
		strings.TrimSpace(response.OpenID) == "" {
		return webWeChatTokenResponse{}, errors.New("WeChat token response is invalid")
	}
	response.AccessToken = strings.TrimSpace(response.AccessToken)
	response.OpenID = strings.TrimSpace(response.OpenID)
	response.UnionID = strings.TrimSpace(response.UnionID)
	response.Scope = strings.TrimSpace(response.Scope)
	return response, nil
}

func (p *webWeChatOAuthProvider) fetchUserInfo(
	ctx context.Context,
	accessToken string,
	openID string,
) (webWeChatUserInfoResponse, error) {
	var response webWeChatUserInfoResponse
	err := p.getJSON(ctx, p.userInfoURL, url.Values{
		"access_token": {accessToken},
		"openid":       {openID},
		"lang":         {"zh_CN"},
	}, &response)
	if err != nil {
		return webWeChatUserInfoResponse{}, err
	}
	if response.ErrorCode != 0 || strings.TrimSpace(response.OpenID) == "" {
		return webWeChatUserInfoResponse{}, errors.New("WeChat user info response is invalid")
	}
	response.OpenID = strings.TrimSpace(response.OpenID)
	response.UnionID = strings.TrimSpace(response.UnionID)
	response.Nickname = strings.TrimSpace(response.Nickname)
	return response, nil
}

func (p *webWeChatOAuthProvider) getJSON(
	ctx context.Context,
	endpoint string,
	query url.Values,
	destination any,
) error {
	requestURL, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	requestURL.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := p.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("WeChat OAuth endpoint returned HTTP %d", response.StatusCode)
	}
	limited := io.LimitReader(response.Body, webWeChatResponseLimitBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if len(raw) > webWeChatResponseLimitBytes {
		return errors.New("WeChat OAuth response exceeded the size limit")
	}
	if err := json.Unmarshal(raw, destination); err != nil {
		return fmt.Errorf("decode WeChat OAuth response: %w", err)
	}
	return nil
}

func containsWeChatScope(value, expected string) bool {
	for _, scope := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' }) {
		if scope == expected {
			return true
		}
	}
	return false
}
