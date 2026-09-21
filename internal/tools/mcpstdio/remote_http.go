package mcpstdio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/coder/websocket"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type remoteHTTPRPCOptions struct {
	ProtocolVersion string
	Modern          bool
}

func remoteHTTPRPC(ctx context.Context, root string, config ServerConfig, sessionID *string, id int, method string, params any) (json.RawMessage, error) {
	return remoteHTTPRPCWithOptions(ctx, root, config, sessionID, id, method, params, remoteHTTPRPCOptions{})
}

func remoteHTTPRPCOnce(ctx context.Context, root string, config ServerConfig, sessionID *string, id int, method string, params any, options remoteHTTPRPCOptions) (json.RawMessage, error) {
	requestURL, err := remoteMCPRequestURL(config.URL, config.QueryParams)
	if err != nil {
		return nil, err
	}
	// Validate and pin the exact URL that will be requested, including the
	// configured query parameters, instead of validating a base URL and then
	// sending a separately assembled destination.
	client, err := secureRemoteHTTPClient(ctx, requestURL)
	if err != nil {
		return nil, err
	}
	request := rpcMessage{JSONRPC: "2.0", Method: method}
	if id > 0 {
		request.ID = id
	}
	if params != nil {
		request.Params = params
	}
	rawRequest, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(rawRequest))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if strings.TrimSpace(options.ProtocolVersion) != "" {
		req.Header.Set("Mcp-Protocol-Version", options.ProtocolVersion)
	}
	if options.Modern {
		req.Header.Set("Mcp-Method", method)
		if name := routableMCPName(method, params); name != "" {
			req.Header.Set("Mcp-Name", name)
		}
	}
	for key, value := range config.Headers {
		if strings.TrimSpace(key) != "" {
			req.Header.Set(key, value)
		}
	}
	if req.Header.Get("Authorization") == "" {
		if token, ok := loadOAuthAccessTokenForRemote(root, config); ok {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	if !options.Modern && sessionID != nil && strings.TrimSpace(*sessionID) != "" {
		req.Header.Set("Mcp-Session-Id", *sessionID)
	}
	secrets := remoteMCPSecretValues(config, req.Header)
	// requestURL is the exact destination validated and DNS-pinned by secureRemoteHTTPClient above.
	resp, err := client.Do(req) // lgtm[go/request-forgery]
	if err != nil {
		return nil, &remoteMCPTransportError{message: fmt.Sprintf("remote MCP %s request failed: %s", method, redactMCPErrorText(err.Error(), secrets)), cause: err}
	}
	defer resp.Body.Close()
	if !options.Modern && sessionID != nil {
		if value := strings.TrimSpace(resp.Header.Get("Mcp-Session-Id")); value != "" {
			*sessionID = value
		}
	}
	if resp.StatusCode == http.StatusAccepted && id == 0 {
		return json.RawMessage(`null`), nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Error pages are bounded diagnostics, never successful source data.
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxRemoteResponseBytes))
		if err != nil {
			return nil, err
		}
		if rawMessage, decodeErr := decodeRemoteHTTPMessage(resp.Header.Get("Content-Type"), body); decodeErr == nil && rawMessage.Error != nil {
			return nil, redactedRPCErrorFor(method, rawMessage.Error, resp.StatusCode, secrets)
		}
		detail := redactMCPErrorText(string(body), secrets)
		if detail == "" {
			return nil, fmt.Errorf("remote MCP %s failed with HTTP %d", method, resp.StatusCode)
		}
		return nil, fmt.Errorf("remote MCP %s failed with HTTP %d: %s", method, resp.StatusCode, detail)
	}
	rawMessage, err := decodeMCPResponseStream(ctx, resp.Header.Get("Content-Type"), resp.Body)
	if id == 0 && errors.Is(err, io.EOF) {
		return json.RawMessage(`null`), nil
	}
	if err != nil {
		return nil, err
	}
	if rawMessage.Method != "" && rawMessage.ID != nil {
		return nil, fmt.Errorf("remote MCP %s returned an unsupported server request %q", method, rawMessage.Method)
	}
	if id > 0 && !idMatches(rawMessage.ID, id) {
		return nil, fmt.Errorf("remote MCP %s response id mismatch", method)
	}
	if rawMessage.Error != nil {
		return nil, redactedRPCErrorFor(method, rawMessage.Error, resp.StatusCode, secrets)
	}
	return rawMessage.Result, nil
}

func remoteMCPSecretValues(config ServerConfig, headers http.Header) []string {
	seen := map[string]struct{}{}
	values := make([]string, 0, len(config.Headers)+2)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, found := seen[value]; found {
			return
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	for _, value := range config.Headers {
		add(value)
	}
	for _, value := range config.QueryParams {
		add(value)
		add(url.QueryEscape(value))
	}
	authorization := strings.TrimSpace(headers.Get("Authorization"))
	add(authorization)
	if _, token, found := strings.Cut(authorization, " "); found {
		add(token)
	}
	return values
}

func routableMCPName(method string, params any) string {
	switch method {
	case "tools/call", "prompts/get":
		if object, ok := params.(map[string]any); ok {
			value, _ := object["name"].(string)
			return strings.TrimSpace(value)
		}
	case "resources/read":
		if object, ok := params.(map[string]any); ok {
			value, _ := object["uri"].(string)
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func decodeRemoteHTTPMessage(contentType string, body []byte) (rpcMessage, error) {
	return decodeMCPResponseStream(context.Background(), contentType, bytes.NewReader(body))
}

func decodeSSEMessage(body []byte) (rpcMessage, error) {
	return decodeMCPResponseStream(context.Background(), "text/event-stream", bytes.NewReader(body))
}

type remoteWebSocketSession struct {
	conn    *websocket.Conn
	secrets []string
}

func (s *remoteWebSocketSession) request(ctx context.Context, id int, method string, params any) (json.RawMessage, error) {
	request := rpcMessage{JSONRPC: "2.0", ID: id, Method: method}
	if params != nil {
		request.Params = params
	}
	if err := s.send(ctx, request); err != nil {
		return nil, err
	}
	for {
		messageType, reader, err := s.conn.Reader(ctx)
		if err != nil {
			return nil, fmt.Errorf("read remote MCP WebSocket response for %s: %w", method, err)
		}
		if messageType != websocket.MessageText {
			if _, err := io.Copy(io.Discard, reader); err != nil {
				return nil, err
			}
			continue
		}
		response, err := decodeMCPResponseStream(ctx, "application/json", reader)
		if err != nil {
			return nil, err
		}
		if response.Method != "" && response.ID != nil {
			_ = s.respondToServerRequest(ctx, response)
			continue
		}
		if !idMatches(response.ID, id) {
			continue
		}
		if response.Error != nil {
			return nil, redactedRPCErrorFor(method, response.Error, 0, s.secrets)
		}
		return response.Result, nil
	}
}
