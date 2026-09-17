package mcpstdio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	latestProtocolVersion       = "2026-07-28"
	latestLegacyProtocolVersion = "2025-11-25"

	metaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	metaClientInfo         = "io.modelcontextprotocol/clientInfo"
	metaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
)

// mcpRPCError preserves the JSON-RPC code and data returned by an MCP peer.
// Protocol negotiation must branch on machine-readable error codes rather
// than matching localized or vendor-specific error strings.
type mcpRPCError struct {
	Method     string
	Code       int
	Message    string
	Data       json.RawMessage
	HTTPStatus int
}

func (e *mcpRPCError) Error() string {
	if e == nil {
		return ""
	}
	prefix := "MCP"
	if strings.TrimSpace(e.Method) != "" {
		prefix += " " + e.Method
	}
	if e.HTTPStatus > 0 {
		return fmt.Sprintf("%s failed with HTTP %d (JSON-RPC %d): %s", prefix, e.HTTPStatus, e.Code, e.Message)
	}
	return fmt.Sprintf("%s failed (JSON-RPC %d): %s", prefix, e.Code, e.Message)
}

func rpcErrorFor(method string, wire *rpcError, httpStatus int) error {
	if wire == nil {
		return nil
	}
	return &mcpRPCError{
		Method: method, Code: wire.Code, Message: strings.TrimSpace(wire.Message),
		Data: append(json.RawMessage(nil), wire.Data...), HTTPStatus: httpStatus,
	}
}

func redactedRPCErrorFor(method string, wire *rpcError, httpStatus int, secrets []string) error {
	if wire == nil {
		return nil
	}
	redacted := *wire
	redacted.Message = redactMCPErrorText(redacted.Message, secrets)
	if len(redacted.Data) > 0 {
		data := []byte(redactMCPErrorText(string(redacted.Data), secrets))
		if json.Valid(data) {
			redacted.Data = data
		} else {
			redacted.Data = nil
		}
	}
	return rpcErrorFor(method, &redacted, httpStatus)
}

func redactMCPErrorText(value string, secrets []string) string {
	value = strings.TrimSpace(value)
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	const maxErrorRunes = 4096
	runes := []rune(value)
	if len(runes) > maxErrorRunes {
		value = string(runes[:maxErrorRunes]) + "..."
	}
	return value
}

type discoverResult struct {
	SupportedVersions []string       `json:"supportedVersions"`
	Capabilities      map[string]any `json:"capabilities"`
}

type protocolLifecycle struct {
	modern          bool
	protocolVersion string
	nextID          int
	sessionID       string
}

type protocolSession interface {
	request(context.Context, int, string, any) (json.RawMessage, error)
	notify(context.Context, string, any) error
}

func modernRequestParams(params any, protocolVersion string) (map[string]any, error) {
	if strings.TrimSpace(protocolVersion) == "" {
		protocolVersion = latestProtocolVersion
	}
	out := map[string]any{}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, errors.New("modern MCP request params must encode as a JSON object")
		}
	}
	meta, _ := out["_meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	if _, exists := meta[metaProtocolVersion]; !exists {
		meta[metaProtocolVersion] = protocolVersion
	}
	if _, exists := meta[metaClientInfo]; !exists {
		meta[metaClientInfo] = productClientInfo()
	}
	if _, exists := meta[metaClientCapabilities]; !exists {
		meta[metaClientCapabilities] = map[string]any{}
	}
	out["_meta"] = meta
	return out, nil
}

func discoverParams(protocolVersion string) map[string]any {
	params, err := modernRequestParams(nil, protocolVersion)
	if err != nil {
		panic(err)
	}
	return params
}

func supportsProtocol(versions []string, wanted string) bool {
	for _, version := range versions {
		if strings.TrimSpace(version) == wanted {
			return true
		}
	}
	return false
}

func negotiateLocalProtocol(ctx context.Context, sess protocolSession) (protocolLifecycle, error) {
	lifecycle := protocolLifecycle{protocolVersion: latestProtocolVersion, nextID: 1}
	raw, discoverErr := sess.request(ctx, lifecycle.nextID, "server/discover", discoverParams(latestProtocolVersion))
	lifecycle.nextID++
	if discoverErr == nil {
		var discovered discoverResult
		if err := json.Unmarshal(raw, &discovered); err != nil {
			return protocolLifecycle{}, fmt.Errorf("decode MCP server/discover result: %w", err)
		}
		if supportsProtocol(discovered.SupportedVersions, latestProtocolVersion) {
			lifecycle.modern = true
			return lifecycle, nil
		}
	}

	lifecycle.protocolVersion = latestLegacyProtocolVersion
	initialized, err := sess.request(ctx, lifecycle.nextID, "initialize", map[string]any{
		"protocolVersion": lifecycle.protocolVersion,
		"capabilities":    map[string]any{"roots": map[string]any{"listChanged": false}},
		"clientInfo":      productClientInfo(),
	})
	if err != nil {
		if discoverErr != nil {
			return protocolLifecycle{}, errors.Join(discoverErr, err)
		}
		return protocolLifecycle{}, err
	}
	var negotiated struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(initialized, &negotiated) == nil && strings.TrimSpace(negotiated.ProtocolVersion) != "" {
		lifecycle.protocolVersion = strings.TrimSpace(negotiated.ProtocolVersion)
	}
	lifecycle.nextID++
	if err := sess.notify(ctx, "notifications/initialized", map[string]any{}); err != nil {
		return protocolLifecycle{}, err
	}
	return lifecycle, nil
}

func (l *protocolLifecycle) requestLocal(ctx context.Context, sess protocolSession, method string, params any) (json.RawMessage, error) {
	if l == nil {
		return nil, errors.New("MCP protocol lifecycle is required")
	}
	requestParams := params
	if l.modern {
		var err error
		requestParams, err = modernRequestParams(params, l.protocolVersion)
		if err != nil {
			return nil, err
		}
	}
	result, err := sess.request(ctx, l.nextID, method, requestParams)
	l.nextID++
	return result, err
}

func negotiateRemoteProtocol(ctx context.Context, root string, config ServerConfig) (protocolLifecycle, error) {
	lifecycle := protocolLifecycle{protocolVersion: latestProtocolVersion, nextID: 1}
	raw, discoverErr := remoteHTTPRPCWithOptions(
		ctx, root, config, nil, lifecycle.nextID, "server/discover", discoverParams(latestProtocolVersion),
		remoteHTTPRPCOptions{ProtocolVersion: latestProtocolVersion, Modern: true},
	)
	lifecycle.nextID++
	if discoverErr == nil {
		var discovered discoverResult
		if err := json.Unmarshal(raw, &discovered); err != nil {
			return protocolLifecycle{}, fmt.Errorf("decode remote MCP server/discover result: %w", err)
		}
		if supportsProtocol(discovered.SupportedVersions, latestProtocolVersion) {
			lifecycle.modern = true
			return lifecycle, nil
		}
	}

	lifecycle.protocolVersion = latestLegacyProtocolVersion
	initialized, err := remoteHTTPRPC(ctx, root, config, &lifecycle.sessionID, lifecycle.nextID, "initialize", map[string]any{
		"protocolVersion": lifecycle.protocolVersion,
		"capabilities":    map[string]any{"roots": map[string]any{"listChanged": false}},
		"clientInfo":      productClientInfo(),
	})
	if err != nil {
		if discoverErr != nil {
			return protocolLifecycle{}, errors.Join(discoverErr, err)
		}
		return protocolLifecycle{}, err
	}
	var negotiated struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(initialized, &negotiated) == nil && strings.TrimSpace(negotiated.ProtocolVersion) != "" {
		lifecycle.protocolVersion = strings.TrimSpace(negotiated.ProtocolVersion)
	}
	lifecycle.nextID++
	if _, err := remoteHTTPRPCWithOptions(
		ctx, root, config, &lifecycle.sessionID, 0, "notifications/initialized", map[string]any{},
		remoteHTTPRPCOptions{ProtocolVersion: lifecycle.protocolVersion},
	); err != nil {
		return protocolLifecycle{}, err
	}
	return lifecycle, nil
}

func (l *protocolLifecycle) requestRemote(ctx context.Context, root string, config ServerConfig, method string, params any) (json.RawMessage, error) {
	if l == nil {
		return nil, errors.New("MCP protocol lifecycle is required")
	}
	requestParams := params
	if l.modern {
		var err error
		requestParams, err = modernRequestParams(params, l.protocolVersion)
		if err != nil {
			return nil, err
		}
	}
	result, err := remoteHTTPRPCWithOptions(
		ctx, root, config, &l.sessionID, l.nextID, method, requestParams,
		remoteHTTPRPCOptions{ProtocolVersion: l.protocolVersion, Modern: l.modern},
	)
	l.nextID++
	return result, err
}
