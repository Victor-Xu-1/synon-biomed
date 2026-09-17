package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

type webOfficeProxyEcho struct {
	Method        string `json:"method"`
	Path          string `json:"path"`
	Query         string `json:"query"`
	Authorization string `json:"authorization"`
	Cookie        string `json:"cookie"`
	SynonUser     string `json:"synon_user"`
}

func newWebOfficeProxyTestServer(t *testing.T, kind string) (*Server, http.Handler, int) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/echo":
			w.Header().Add("Set-Cookie", "office_session=unsafe; Path=/")
			_ = json.NewEncoder(w).Encode(webOfficeProxyEcho{
				Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
				Authorization: r.Header.Get("Authorization"),
				Cookie:        r.Header.Get("Cookie"),
				SynonUser:     r.Header.Get("X-Synon-User-Id"),
			})
		case "/redirect":
			w.Header().Set("Location", "http://"+r.Host+"/deck/2?mode=view#slide")
			w.WriteHeader(http.StatusTemporaryRedirect)
		case "/html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("ETag", `"upstream"`)
			_, _ = w.Write([]byte("<!doctype html><html><head><title>Deck</title></head><body>ok</body></html>"))
		default:
			_, _ = w.Write([]byte("office-preview-ready"))
		}
	}))
	t.Cleanup(upstream.Close)
	parsed, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Options{})
	srv.webOfficeWatches["proxy-test"] = &webOfficeWatch{
		UserID: "owner", Kind: kind, Port: port, Done: make(chan error),
	}
	return srv, srv.Handler(), port
}

func TestWebOfficeProxyForwardsOnlyToRegisteredOwnerWatch(t *testing.T) {
	_, handler, port := newWebOfficeProxyTestServer(t, "word")
	path := fmt.Sprintf("/api/office-watch-proxy/%d/echo?q=slide%%202", port)
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("X-Synon-User-Id", "owner")
	request.Header.Set("X-Synon-CSRF-Token", "secret")
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Cookie", "synon_session=secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var echoed webOfficeProxyEcho
	if err := json.Unmarshal(response.Body.Bytes(), &echoed); err != nil {
		t.Fatal(err)
	}
	if echoed.Method != http.MethodGet || echoed.Path != "/echo" || echoed.Query != "q=slide%202" {
		t.Fatalf("forwarded request = %#v", echoed)
	}
	if echoed.Authorization != "" || echoed.Cookie != "" || echoed.SynonUser != "" {
		t.Fatalf("sensitive headers reached upstream: %#v", echoed)
	}
	if response.Header().Get("Set-Cookie") != "" {
		t.Fatalf("upstream cookie escaped proxy: %q", response.Header().Get("Set-Cookie"))
	}
	if response.Header().Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Fatalf("X-Frame-Options = %q", response.Header().Get("X-Frame-Options"))
	}

	redirect := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/office-watch-proxy/%d/redirect", port), nil)
	redirect.Header.Set("X-Synon-User-Id", "owner")
	redirectResponse := httptest.NewRecorder()
	handler.ServeHTTP(redirectResponse, redirect)
	wantLocation := fmt.Sprintf("/api/office-watch-proxy/%d/deck/2?mode=view#slide", port)
	if redirectResponse.Code != http.StatusTemporaryRedirect ||
		redirectResponse.Header().Get("Location") != wantLocation {
		t.Fatalf("redirect status=%d location=%q want=%q",
			redirectResponse.Code, redirectResponse.Header().Get("Location"), wantLocation)
	}
}

func TestWebOfficeProxyInjectsNavigationGuardAndSupportsHead(t *testing.T) {
	_, handler, port := newWebOfficeProxyTestServer(t, "ppt")
	path := fmt.Sprintf("/api/ppt-proxy/%d/html", port)
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("X-Synon-User-Id", "owner")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	wantBase := fmt.Sprintf("/api/ppt-proxy/%d", port)
	if !strings.Contains(body, "data-synon-office-proxy") ||
		!strings.Contains(body, "const b="+strconv.Quote(wantBase)) ||
		!strings.Contains(body, "<head><script") {
		t.Fatalf("navigation guard was not injected after head: %s", body)
	}
	if response.Header().Get("ETag") != "" {
		t.Fatalf("mutated response retained ETag %q", response.Header().Get("ETag"))
	}
	if response.Header().Get("Content-Length") != strconv.Itoa(response.Body.Len()) {
		t.Fatalf("content length=%q body=%d", response.Header().Get("Content-Length"), response.Body.Len())
	}

	head := httptest.NewRequest(http.MethodHead,
		fmt.Sprintf("/api/ppt-proxy/%d/echo", port), nil)
	head.Header.Set("X-Synon-User-Id", "owner")
	headResponse := httptest.NewRecorder()
	handler.ServeHTTP(headResponse, head)
	if headResponse.Code != http.StatusOK || headResponse.Body.Len() != 0 {
		t.Fatalf("HEAD status=%d body=%q", headResponse.Code, headResponse.Body.String())
	}
}

func TestWebOfficeProxyRejectsUnregisteredCrossUserAndInvalidRequests(t *testing.T) {
	_, handler, port := newWebOfficeProxyTestServer(t, "word")
	tests := []struct {
		name   string
		method string
		path   string
		user   string
		status int
	}{
		{
			name: "foreign owner", method: http.MethodGet,
			path: fmt.Sprintf("/api/office-watch-proxy/%d/", port),
			user: "attacker", status: http.StatusNotFound,
		},
		{
			name: "wrong proxy kind", method: http.MethodGet,
			path: fmt.Sprintf("/api/ppt-proxy/%d/", port),
			user: "owner", status: http.StatusNotFound,
		},
		{
			name: "unregistered port", method: http.MethodGet,
			path: fmt.Sprintf("/api/office-watch-proxy/%d/", port+1),
			user: "owner", status: http.StatusNotFound,
		},
		{
			name: "privileged port", method: http.MethodGet,
			path: "/api/office-watch-proxy/80/", user: "owner",
			status: http.StatusBadRequest,
		},
		{
			name: "mutation", method: http.MethodPost,
			path: fmt.Sprintf("/api/office-watch-proxy/%d/", port),
			user: "owner", status: http.StatusMethodNotAllowed,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(testCase.method, testCase.path, nil)
			request.Header.Set("X-Synon-User-Id", testCase.user)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != testCase.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, testCase.status, response.Body.String())
			}
		})
	}

	if _, _, _, _, err := parseWebOfficeProxyPath(
		fmt.Sprintf("/api/office-watch-proxy/%d/../secret", port),
	); !errors.Is(err, errWebOfficeProxyInvalid) {
		t.Fatalf("traversal err=%v", err)
	}
	if got := rewriteWebOfficeProxyLocation(
		"https://example.test/deck", "/api/office-watch-proxy/9000", "127.0.0.1:9000",
	); got != "https://example.test/deck" {
		t.Fatalf("external redirect was rewritten to %q", got)
	}
	if got := rewriteWebOfficeProxyLocation(
		"javascript:alert(1)", "/api/office-watch-proxy/9000", "127.0.0.1:9000",
	); got != "/api/office-watch-proxy/9000/" {
		t.Fatalf("unsafe redirect was preserved as %q", got)
	}
}
