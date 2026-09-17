package server

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxWebOfficeProxyHTMLBytes = 16 << 20

var (
	errWebOfficeProxyInvalid  = errors.New("invalid Office preview proxy request")
	errWebOfficeProxyInactive = errors.New("Office preview is not active")
)

func (s *Server) handleWebOfficeProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeWebOfficeProxyError(w, fmt.Errorf("%w: GET or HEAD required", errWebFSMethod))
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	route, port, suffix, proxyBase, err := parseWebOfficeProxyPath(r.URL.Path)
	if err != nil {
		writeWebOfficeProxyError(w, err)
		return
	}
	if s.activeWebOfficeWatch(userID, port, route) == nil {
		writeWebOfficeProxyError(w, errWebOfficeProxyInactive)
		return
	}

	upstreamHost := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	transport := &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   3 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ResponseHeaderTimeout: 15 * time.Second,
	}
	defer transport.CloseIdleConnections()
	proxy := &httputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: -1,
		Director: func(out *http.Request) {
			out.URL.Scheme = "http"
			out.URL.Host = upstreamHost
			out.URL.Path = suffix
			out.URL.RawPath = ""
			out.Host = upstreamHost
			out.Header.Del("Accept-Encoding")
			sanitizeWebOfficeProxyRequestHeaders(out.Header)
		},
		ModifyResponse: func(response *http.Response) error {
			return modifyWebOfficeProxyResponse(response, proxyBase, upstreamHost)
		},
		ErrorHandler: func(response http.ResponseWriter, _ *http.Request, _ error) {
			writeWebOfficeProxyError(response, errors.New("Office preview upstream request failed"))
		},
	}
	proxy.ServeHTTP(w, r)
}

func parseWebOfficeProxyPath(value string) (route string, port int, suffix string, base string, err error) {
	for _, candidate := range []string{"office-watch-proxy", "ppt-proxy"} {
		prefix := "/api/" + candidate + "/"
		if !strings.HasPrefix(value, prefix) {
			continue
		}
		remainder := strings.TrimPrefix(value, prefix)
		parts := strings.SplitN(remainder, "/", 2)
		if parts[0] == "" {
			break
		}
		parsed, parseErr := strconv.Atoi(parts[0])
		if parseErr != nil || parsed < 1024 || parsed > 65535 {
			break
		}
		suffix = "/"
		if len(parts) == 2 && parts[1] != "" {
			suffix += parts[1]
		}
		if !validWebOfficeProxySuffix(suffix) {
			break
		}
		return candidate, parsed, suffix, prefix + parts[0], nil
	}
	return "", 0, "", "", errWebOfficeProxyInvalid
}

func validWebOfficeProxySuffix(value string) bool {
	if value == "" || value[0] != '/' || strings.ContainsAny(value, "\\\x00") {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." || segment == "." {
			return false
		}
	}
	return true
}

func (s *Server) activeWebOfficeWatch(userID string, port int, route string) *webOfficeWatch {
	s.webOfficeMu.Lock()
	defer s.webOfficeMu.Unlock()
	for key, watch := range s.webOfficeWatches {
		if watch == nil || watch.Port != port {
			continue
		}
		select {
		case <-watch.Done:
			delete(s.webOfficeWatches, key)
			if watch.Control != nil {
				_ = watch.Control.Close()
			}
			return nil
		default:
		}
		if watch.UserID != userID {
			return nil
		}
		if (route == "ppt-proxy") != (watch.Kind == "ppt") {
			return nil
		}
		return watch
	}
	return nil
}

func sanitizeWebOfficeProxyRequestHeaders(header http.Header) {
	for key := range header {
		lower := strings.ToLower(key)
		if lower == "authorization" || lower == "proxy-authorization" ||
			lower == "cookie" || strings.HasPrefix(lower, "x-synon-") {
			header.Del(key)
		}
	}
}

func modifyWebOfficeProxyResponse(response *http.Response, proxyBase string, upstreamHost string) error {
	response.Header.Del("Set-Cookie")
	response.Header.Set("X-Frame-Options", "SAMEORIGIN")
	response.Header.Set("X-Content-Type-Options", "nosniff")
	if location := response.Header.Get("Location"); location != "" {
		response.Header.Set("Location", rewriteWebOfficeProxyLocation(location, proxyBase, upstreamHost))
	}
	if response.Request.Method == http.MethodHead ||
		!strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/html") {
		return nil
	}
	originalBody := response.Body
	body, err := io.ReadAll(io.LimitReader(originalBody, maxWebOfficeProxyHTMLBytes+1))
	closeErr := originalBody.Close()
	if err != nil {
		return fmt.Errorf("read Office preview HTML: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("close Office preview HTML: %w", closeErr)
	}
	if len(body) > maxWebOfficeProxyHTMLBytes {
		return errors.New("Office preview HTML exceeds the configured limit")
	}
	body = injectWebOfficeNavigationGuard(body, proxyBase)
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	response.Header.Set("Content-Length", strconv.Itoa(len(body)))
	response.Header.Del("Content-MD5")
	response.Header.Del("Digest")
	response.Header.Del("ETag")
	return nil
}

func rewriteWebOfficeProxyLocation(location string, proxyBase string, upstreamHost string) string {
	reference, err := url.Parse(location)
	if err != nil {
		return location
	}
	if reference.IsAbs() {
		if reference.Scheme != "http" && reference.Scheme != "https" {
			return proxyBase + "/"
		}
		host := strings.ToLower(reference.Hostname())
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			return location
		}
		if reference.Port() != "" && reference.Port() != portFromHost(upstreamHost) {
			return location
		}
	}
	path := reference.EscapedPath()
	if path == "" {
		path = "/"
	} else if path[0] != '/' {
		path = "/" + path
	}
	result := proxyBase + path
	if reference.RawQuery != "" {
		result += "?" + reference.RawQuery
	}
	if reference.Fragment != "" {
		result += "#" + reference.EscapedFragment()
	}
	return result
}

func portFromHost(host string) string {
	_, port, err := net.SplitHostPort(host)
	if err != nil {
		return ""
	}
	return port
}

func injectWebOfficeNavigationGuard(body []byte, proxyBase string) []byte {
	guard := []byte(fmt.Sprintf(`<script data-synon-office-proxy>(function(){const b=%s;const r=(v)=>{if(typeof v!=="string")return v;try{const u=new URL(v,location.href);if((u.hostname==="127.0.0.1"||u.hostname==="localhost"||u.hostname==="::1")||v.startsWith("/"))return b+u.pathname+u.search+u.hash}catch(e){}return v};for(const n of ["pushState","replaceState"]){const f=history[n].bind(history);history[n]=function(s,t,v){return f(s,t,r(v))}}document.addEventListener("click",e=>{const a=e.target&&e.target.closest?e.target.closest("a[href]"):null;if(!a)return;const v=r(a.getAttribute("href"));if(v!==a.getAttribute("href"))a.setAttribute("href",v)},true);try{for(const n of ["assign","replace"]){const f=Location.prototype[n];Location.prototype[n]=function(v){return f.call(this,r(v))}}}catch(e){}})();</script>`, strconv.Quote(proxyBase)))
	lower := bytes.ToLower(body)
	head := bytes.Index(lower, []byte("<head"))
	if head >= 0 {
		if closeIndex := bytes.IndexByte(body[head:], '>'); closeIndex >= 0 {
			position := head + closeIndex + 1
			result := make([]byte, 0, len(body)+len(guard))
			result = append(result, body[:position]...)
			result = append(result, guard...)
			return append(result, body[position:]...)
		}
	}
	return append(guard, body...)
}

func writeWebOfficeProxyError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	switch {
	case errors.Is(err, errWebFSMethod):
		status = http.StatusMethodNotAllowed
	case errors.Is(err, errWebOfficeProxyInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, errWebOfficeProxyInactive):
		status = http.StatusNotFound
	}
	w.Header().Set("Cache-Control", "no-store")
	writeWorkspaceJSON(w, status, map[string]any{"ok": false, "error": err.Error()})
}
