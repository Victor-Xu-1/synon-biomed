package server

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"synon-go/internal/ketchermcp"
)

const (
	ketcherMCPAppServerID     = "bundled:ketcher-chemistry"
	mcpAppResourceTicketTTL   = 2 * time.Minute
	mcpAppResourceTicketLimit = 1024
	mcpAppTicketResponseLimit = 5 << 20
)

var errMCPAppResourceTicketsClosed = errors.New("MCP app resource ticket store is closed")

type mcpAppResourceTicket struct {
	UserID           string
	ServerID         string
	ResourceURI      string
	ResourceSHA256   string
	ParentOrigin     string
	SandboxAuthority string
	ExpiresAt        time.Time
}

type mcpAppResourceTicketStore struct {
	mu      sync.Mutex
	tickets map[string]mcpAppResourceTicket
	now     func() time.Time
	limit   int
	closed  bool
}

func newMCPAppResourceTicketStore() *mcpAppResourceTicketStore {
	return &mcpAppResourceTicketStore{
		tickets: map[string]mcpAppResourceTicket{}, now: time.Now, limit: mcpAppResourceTicketLimit,
	}
}

func (s *mcpAppResourceTicketStore) Issue(
	userID, serverID, resourceURI, resourceSHA256, parentOrigin, sandboxAuthority string,
) (string, time.Time, error) {
	if s == nil {
		return "", time.Time{}, errMCPAppResourceTicketsClosed
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	now := s.now().UTC()
	expiresAt := now.Add(mcpAppResourceTicketTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", time.Time{}, errMCPAppResourceTicketsClosed
	}
	s.pruneLocked(now)
	if s.limit <= 0 || len(s.tickets) >= s.limit {
		return "", time.Time{}, errors.New("MCP app resource ticket capacity is exhausted")
	}
	s.tickets[token] = mcpAppResourceTicket{
		UserID: strings.TrimSpace(userID), ServerID: strings.TrimSpace(serverID),
		ResourceURI: strings.TrimSpace(resourceURI), ResourceSHA256: strings.TrimSpace(resourceSHA256),
		ParentOrigin: parentOrigin, SandboxAuthority: sandboxAuthority,
		ExpiresAt: expiresAt,
	}
	return token, expiresAt, nil
}

func (s *mcpAppResourceTicketStore) Consume(token, sandboxAuthority string) (mcpAppResourceTicket, bool) {
	if s == nil {
		return mcpAppResourceTicket{}, false
	}
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return mcpAppResourceTicket{}, false
	}
	s.pruneLocked(now)
	ticket, found := s.tickets[token]
	if !found || ticket.SandboxAuthority != sandboxAuthority {
		return mcpAppResourceTicket{}, false
	}
	delete(s.tickets, token)
	return ticket, true
}

func (s *mcpAppResourceTicketStore) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.tickets = map[string]mcpAppResourceTicket{}
}

func (s *mcpAppResourceTicketStore) pruneLocked(now time.Time) {
	for token, ticket := range s.tickets {
		if !ticket.ExpiresAt.After(now) {
			delete(s.tickets, token)
		}
	}
}

type mcpAppViewerBinding struct {
	ServerID        string  `json:"serverId"`
	ServerName      string  `json:"serverName"`
	ResourceURI     string  `json:"resourceUri"`
	OpenTool        string  `json:"openTool"`
	ContentParam    string  `json:"contentParam"`
	ContentEncoding string  `json:"contentEncoding"`
	NameParam       *string `json:"nameParam"`
	DocsURL         *string `json:"docsUrl"`
	InProcess       bool    `json:"inProcess"`
}

func (s *Server) handleMCPAppViewerBindings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeV11Detail(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s == nil || s.mcpDirectory == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "MCP directory is unavailable")
		return
	}
	connector, found, err := s.mcpDirectory.ResolveRuntimeConnector(r.Context(), compatAgentUserID(r), ketcherMCPAppServerID)
	if err != nil {
		writeV11Detail(w, http.StatusInternalServerError, "failed to resolve MCP app viewer")
		return
	}
	if !found || connector.Config.Disabled {
		writeJSON(w, http.StatusOK, map[string]any{"bindings": []mcpAppViewerBinding{}})
		return
	}
	nameParam := "filename"
	docsURL := "https://github.com/epam/ketcher/blob/master/documentation/help.md#ketcher-overview"
	writeJSON(w, http.StatusOK, map[string]any{"bindings": []mcpAppViewerBinding{{
		ServerID: connector.ID, ServerName: connector.Name,
		ResourceURI: ketchermcp.ResourceURI, OpenTool: "open_sketcher",
		ContentParam: "ket", ContentEncoding: "utf8", NameParam: &nameParam,
		DocsURL: &docsURL, InProcess: true,
	}}})
}

func (s *Server) handleMCPAppResourceTickets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeV11Detail(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s == nil || s.mcpDirectory == nil || s.mcpAppResourceTickets == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "MCP app resource broker is unavailable")
		return
	}
	parentAuthority, sandboxAuthority, allowed := mcpAppSandboxBinding(r.Host)
	if !allowed || !mcpAppRemoteIsLoopback(r.RemoteAddr) {
		http.NotFound(w, r)
		return
	}
	var body struct {
		ServerID    string         `json:"server_id"`
		ResourceURI string         `json:"resource_uri"`
		Arguments   map[string]any `json:"arguments"`
	}
	if !decodeMCPAppBridgeBody(w, r, &body) {
		return
	}
	body.ServerID = strings.TrimSpace(body.ServerID)
	body.ResourceURI = strings.TrimSpace(body.ResourceURI)
	if body.ServerID != ketcherMCPAppServerID || body.ResourceURI != ketchermcp.ResourceURI {
		writeV11Detail(w, http.StatusNotFound, "MCP app viewer not found")
		return
	}
	connector, found, err := s.mcpDirectory.ResolveRuntimeConnector(r.Context(), compatAgentUserID(r), body.ServerID)
	if err != nil {
		writeV11Detail(w, http.StatusInternalServerError, "failed to resolve MCP app viewer")
		return
	}
	if !found || connector.Config.Disabled {
		writeV11Detail(w, http.StatusNotFound, "MCP app viewer not found")
		return
	}
	toolResult, err := ketchermcp.OpenSketcher(body.Arguments)
	if err != nil {
		writeV11Detail(w, http.StatusBadRequest, "MCP app mount input is invalid")
		return
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	token, expiresAt, err := s.mcpAppResourceTickets.Issue(
		compatAgentUserID(r), body.ServerID, body.ResourceURI, ketchermcp.ExpectedWidgetSHA256,
		scheme+"://"+parentAuthority, sandboxAuthority,
	)
	if err != nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "failed to issue MCP app resource ticket")
		return
	}
	if body.Arguments == nil {
		body.Arguments = map[string]any{}
	}
	writeMCPAppTicketJSON(w, http.StatusCreated, map[string]any{
		"ticket": token, "expires_at": expiresAt,
		"tool_input": body.Arguments, "tool_result": toolResult,
	})
}

func writeMCPAppTicketJSON(w http.ResponseWriter, status int, value any) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		writeV11Detail(w, http.StatusInternalServerError, "failed to encode MCP app ticket")
		return
	}
	payload := unescapeMCPAppJSONLineSeparators(encoded.Bytes())
	if len(payload) > mcpAppTicketResponseLimit {
		writeV11Detail(w, http.StatusInternalServerError, "MCP app ticket response is too large")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

// encoding/json always escapes the two JavaScript line separators even when
// HTML escaping is disabled. JSON.stringify leaves them as UTF-8, and the
// mount budget is defined against that representation. Undo only structural
// escapes emitted by the encoder; an input string containing the literal text
// `\u2028` or `\u2029` remains escaped and round-trips unchanged.
func unescapeMCPAppJSONLineSeparators(encoded []byte) []byte {
	if !bytes.Contains(encoded, []byte(`\u2028`)) && !bytes.Contains(encoded, []byte(`\u2029`)) {
		return encoded
	}
	result := make([]byte, 0, len(encoded))
	for index := 0; index < len(encoded); {
		if index+6 <= len(encoded) && encoded[index] == '\\' &&
			bytes.Equal(encoded[index:index+5], []byte(`\u202`)) &&
			(encoded[index+5] == '8' || encoded[index+5] == '9') {
			precedingBackslashes := 0
			for cursor := index - 1; cursor >= 0 && encoded[cursor] == '\\'; cursor-- {
				precedingBackslashes++
			}
			if precedingBackslashes%2 == 0 {
				result = append(result, 0xe2, 0x80, 0xa8+(encoded[index+5]-'8'))
				index += 6
				continue
			}
		}
		result = append(result, encoded[index])
		index++
	}
	return result
}

func (s *Server) handleMCPAppSandboxResource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeV11Detail(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	authority, ok := canonicalMCPAppSandboxAuthority(r.Host)
	if !ok || !mcpAppRemoteIsLoopback(r.RemoteAddr) || s == nil || s.mcpAppResourceTickets == nil {
		http.NotFound(w, r)
		return
	}
	ticket, found := s.mcpAppResourceTickets.Consume(strings.TrimSpace(r.URL.Query().Get("ticket")), authority)
	if !found || ticket.ServerID != ketcherMCPAppServerID || ticket.ResourceURI != ketchermcp.ResourceURI ||
		ticket.ResourceSHA256 != ketchermcp.ExpectedWidgetSHA256 {
		writeV11Detail(w, http.StatusGone, "MCP app resource ticket is invalid or expired")
		return
	}
	html, err := ketchermcp.LoadWidgetHTML(ketchermcp.DiscoverWidgetPath())
	if err != nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "MCP app resource is unavailable")
		return
	}
	writeMCPAppHTML(w, html, ticket.ParentOrigin)
}

func mcpAppSandboxAuthority(parentAuthority string) (string, bool) {
	_, sandboxAuthority, ok := mcpAppSandboxBinding(parentAuthority)
	return sandboxAuthority, ok
}

func mcpAppSandboxBinding(parentAuthority string) (string, string, bool) {
	host, port, ok := parseMCPAppAuthority(parentAuthority)
	if !ok || (host != "localhost" && host != "127.0.0.1" && host != "::1") {
		return "", "", false
	}
	canonicalParent := host
	if host == "::1" {
		canonicalParent = "[::1]"
	}
	if port == "" {
		return canonicalParent, "mcp-app.localhost", true
	}
	return canonicalParent + ":" + port, "mcp-app.localhost:" + port, true
}

func canonicalMCPAppSandboxAuthority(authority string) (string, bool) {
	host, port, ok := parseMCPAppAuthority(authority)
	if !ok || host != "mcp-app.localhost" {
		return "", false
	}
	if port == "" {
		return host, true
	}
	return host + ":" + port, true
}

func parseMCPAppAuthority(authority string) (string, string, bool) {
	if authority == "" || authority != strings.TrimSpace(authority) {
		return "", "", false
	}
	for _, character := range authority {
		if character > 0x7f || character <= 0x20 || strings.ContainsRune("/@?#\\", character) {
			return "", "", false
		}
	}
	host := authority
	port := ""
	hasExplicitPort := false
	switch strings.Count(authority, ":") {
	case 0:
	case 1:
		hasExplicitPort = true
		var err error
		host, port, err = net.SplitHostPort(authority)
		if err != nil {
			return "", "", false
		}
	default:
		if !strings.HasPrefix(authority, "[") {
			return "", "", false
		}
		if strings.HasSuffix(authority, "]") {
			host = strings.TrimSuffix(strings.TrimPrefix(authority, "["), "]")
			break
		}
		hasExplicitPort = true
		var err error
		host, port, err = net.SplitHostPort(authority)
		if err != nil {
			return "", "", false
		}
	}
	host = strings.ToLower(host)
	if host == "" || strings.HasSuffix(host, ".") {
		return "", "", false
	}
	if hasExplicitPort && port == "" {
		return "", "", false
	}
	if port != "" {
		for _, character := range port {
			if character < '0' || character > '9' {
				return "", "", false
			}
		}
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "", "", false
		}
		port = strconv.Itoa(value)
	}
	return host, port, true
}

func mcpAppRemoteIsLoopback(remoteAddress string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddress))
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func writeMCPAppHTML(w http.ResponseWriter, html []byte, parentOrigin string) {
	w.Header().Set("Content-Type", ketchermcp.ResourceMIMEType+"; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; object-src 'none'; form-action 'none'; frame-ancestors "+parentOrigin+"; script-src 'unsafe-inline' 'unsafe-eval' blob:; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; connect-src 'none'; worker-src blob:")
	w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(html)
}
