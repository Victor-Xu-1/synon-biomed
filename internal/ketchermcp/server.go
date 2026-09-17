// Package ketchermcp provides the native stdio MCP server for the bundled
// Ketcher Chemistry app. The browser widget remains the upstream JavaScript
// application, while the release no longer needs Node to expose it over MCP.
package ketchermcp

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	ResourceURI          = "ui://ketcher-chemistry/editor"
	ResourceMIMEType     = "text/html;profile=mcp-app"
	ExpectedWidgetSHA256 = "b7ada3e39c2ef4b1a686eb7c26a912adadef06381f2a83c2cdd7a9c14602b20f"
	maxRequestBytes      = 1024 * 1024
	maxWidgetBytes       = 32 * 1024 * 1024
	maxMountInputBytes   = 2 * 1024 * 1024
	modernProtocol       = "2026-07-28"
	legacyProtocol       = "2025-11-25"
	originalProtocol     = "2024-11-05"
	protocolMetaKey      = "io.modelcontextprotocol/protocolVersion"
	serverInfoMetaKey    = "io.modelcontextprotocol/serverInfo"
)

type Options struct {
	Input          io.Reader
	Output         io.Writer
	WidgetGzipPath string
	WidgetSHA256   string
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Run serves newline-delimited JSON-RPC until input closes or ctx is canceled.
func Run(ctx context.Context, options Options) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if options.Input == nil {
		options.Input = os.Stdin
	}
	if options.Output == nil {
		options.Output = os.Stdout
	}
	options.WidgetGzipPath = strings.TrimSpace(options.WidgetGzipPath)
	if options.WidgetGzipPath == "" {
		return errors.New("Ketcher widget gzip path is required")
	}
	if options.WidgetSHA256 == "" {
		options.WidgetSHA256 = ExpectedWidgetSHA256
	}

	scanner := bufio.NewScanner(options.Input)
	scanner.Buffer(make([]byte, 64*1024), maxRequestBytes)
	encoder := json.NewEncoder(options.Output)
	encoder.SetEscapeHTML(false)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var request rpcRequest
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			if err := encodeRPCError(encoder, json.RawMessage("null"), -32700, "parse error"); err != nil {
				return err
			}
			continue
		}
		if request.JSONRPC != "2.0" || strings.TrimSpace(request.Method) == "" {
			id := request.ID
			if len(id) == 0 {
				id = json.RawMessage("null")
			}
			if err := encodeRPCError(encoder, id, -32600, "invalid request"); err != nil {
				return err
			}
			continue
		}

		result, rpcErr := handleRequest(request, options)
		if len(request.ID) == 0 {
			continue
		}
		if rpcErr == nil && request.Method != "initialize" && modernRequest(request.Params) {
			result = decorateModernResult(result)
		}
		response := rpcResponse{JSONRPC: "2.0", ID: request.ID, Result: result, Error: rpcErr}
		if err := encoder.Encode(response); err != nil {
			return fmt.Errorf("write Ketcher MCP response: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read Ketcher MCP request: %w", err)
	}
	return ctx.Err()
}

func handleRequest(request rpcRequest, options Options) (any, *rpcError) {
	switch request.Method {
	case "server/discover":
		return map[string]any{
			"supportedVersions": []string{modernProtocol, legacyProtocol, originalProtocol},
			"capabilities": map[string]any{
				"resources": map[string]any{"listChanged": false},
				"tools":     map[string]any{"listChanged": false},
			},
		}, nil
	case "initialize":
		return map[string]any{
			"protocolVersion": negotiatedLegacyProtocol(request.Params),
			"capabilities": map[string]any{
				"resources": map[string]any{"listChanged": false},
				"tools":     map[string]any{"listChanged": false},
			},
			"serverInfo": map[string]any{"name": "ketcher-chemistry", "version": "0.1.0"},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": []any{ketcherTool()}}, nil
	case "tools/call":
		return callOpenSketcher(request.Params)
	case "resources/list":
		return map[string]any{"resources": []any{ketcherResource()}}, nil
	case "resources/read":
		return readKetcherResource(request.Params, options)
	case "notifications/initialized", "notifications/cancelled":
		return map[string]any{}, nil
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found"}
	}
}

func negotiatedLegacyProtocol(raw json.RawMessage) string {
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(raw, &params) == nil && params.ProtocolVersion == legacyProtocol {
		return legacyProtocol
	}
	return originalProtocol
}

func modernRequest(raw json.RawMessage) bool {
	var params struct {
		Meta map[string]any `json:"_meta"`
	}
	if json.Unmarshal(raw, &params) != nil {
		return false
	}
	version, _ := params.Meta[protocolMetaKey].(string)
	return version == modernProtocol
}

func decorateModernResult(result any) any {
	object, ok := result.(map[string]any)
	if !ok {
		return result
	}
	copy := make(map[string]any, len(object)+2)
	for key, value := range object {
		copy[key] = value
	}
	copy["resultType"] = "complete"
	meta, _ := copy["_meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	meta[serverInfoMetaKey] = map[string]any{"name": "ketcher-chemistry", "version": "0.1.0"}
	copy["_meta"] = meta
	return copy
}

func ketcherResource() map[string]any {
	return map[string]any{
		"uri":         ResourceURI,
		"name":        "Ketcher Chemistry",
		"description": "Interactive 2D molecule sketcher (Ketcher)",
		"mimeType":    ResourceMIMEType,
	}
}

func ketcherTool() map[string]any {
	stringProperty := func() map[string]any { return map[string]any{"type": "string"} }
	properties := map[string]any{
		"smiles": stringProperty(), "molfile": stringProperty(), "ket": stringProperty(),
		"rxn": stringProperty(),
		"filename": map[string]any{
			"type":        "string",
			"description": "Name for the saved artifact (e.g. 'benzene'). Extension is added automatically.",
		},
	}
	outputProperties := map[string]any{
		"smiles": stringProperty(), "molfile": stringProperty(), "ket": stringProperty(), "rxn": stringProperty(),
	}
	return map[string]any{
		"name":        "open_sketcher",
		"title":       "Open molecule sketcher",
		"description": "Open the interactive 2D molecule sketcher. Pass one of {ket, molfile, rxn, smiles} to seed the canvas (ket preferred — lossless), or omit for a blank canvas. Pass {filename} (e.g. 'benzene') to name the saved artifact. Returns an artifact_id — pass it to the sketcher's set_structure / highlight_atoms / get_structure tools (which appear in your tool list once the tile is mounted).",
		"inputSchema": map[string]any{
			"type": "object", "properties": properties, "additionalProperties": false,
		},
		"outputSchema": map[string]any{
			"type": "object", "properties": outputProperties, "additionalProperties": false,
		},
		"_meta": map[string]any{
			"ui":             map[string]any{"resourceUri": ResourceURI},
			"ui/resourceUri": ResourceURI,
			"operon.dev/viewer": map[string]any{
				"opensMimeTypes": []string{"chemical/x-daylight-smiles", "chemical/x-mdl-*"},
				"opensExtensions": []string{
					".smi", ".smiles", ".cxsmiles", ".cdxml", ".mol", ".sdf", ".ket", ".rxn",
				},
				"contentParam":  "ket",
				"nameParam":     "filename",
				"docsUrl":       "https://github.com/epam/ketcher/blob/master/documentation/help.md#ketcher-overview",
				"contextSchema": ketcherContextSchema(),
				"save": map[string]any{
					"appTool":        "get_structure",
					"resultField":    "ket",
					"mimeType":       "application/json",
					"extension":      ".ket",
					"filenameStem":   "sketcher",
					"hasChangeField": "has_change",
					"emptyTemplate":  `{"root":{"nodes":[]}}`,
				},
				"promptHint": `Save molecules and reactions as .ket/.mol/.rxn artifacts — they open in the sketcher where the user can edit. Do NOT render as static PNGs unless asked. Drive the live tile via host.app("ketcher-chemistry").<handler>(artifact_id=...).`,
			},
		},
	}
}

func ketcherContextSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"smiles":            map[string]any{"type": "string", "maxLength": 500, "charset": "smiles"},
			"rxn_smiles":        map[string]any{"type": "string", "maxLength": 1000, "charset": "rxn_smiles"},
			"prev_smiles":       map[string]any{"type": "string", "maxLength": 500, "charset": "smiles"},
			"has_reaction":      map[string]any{"type": "boolean"},
			"has_change":        map[string]any{"type": "boolean"},
			"selected_atoms":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "maxItems": 256},
			"highlighted_atoms": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "maxItems": 256},
		},
	}
}

func callOpenSketcher(raw json.RawMessage) (any, *rpcError) {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &params); err != nil || params.Name == "" {
		return nil, &rpcError{Code: -32602, Message: "invalid tools/call parameters"}
	}
	if params.Name != "open_sketcher" {
		return nil, &rpcError{Code: -32602, Message: "unknown tool"}
	}
	result, err := OpenSketcher(params.Arguments)
	if err != nil {
		return nil, &rpcError{Code: -32602, Message: err.Error()}
	}
	return result, nil
}

// OpenSketcher validates and executes the host-mount phase of the
// bundled Ketcher tool. Both stdio MCP calls and the browser resource ticket
// endpoint use this function so the tool input/result contract has one owner.
func OpenSketcher(arguments map[string]any) (map[string]any, error) {
	if arguments == nil {
		arguments = map[string]any{}
	}
	allowed := map[string]bool{"smiles": true, "molfile": true, "ket": true, "rxn": true, "filename": true}
	serializedBytes := 2 // opening and closing braces
	fieldCount := 0
	for key, value := range arguments {
		if !allowed[key] {
			return nil, errors.New("Ketcher mount input contains an unknown field")
		}
		text, ok := value.(string)
		if !ok {
			return nil, errors.New(key + " must be a string")
		}
		keyBytes, keyValid := jsonStringUTF8Bytes(key)
		valueBytes, valueValid := jsonStringUTF8Bytes(text)
		if !keyValid || !valueValid {
			return nil, errors.New("Ketcher mount input contains invalid UTF-8")
		}
		if fieldCount > 0 {
			serializedBytes++
		}
		serializedBytes += keyBytes + 1 + valueBytes // key, colon, value
		if serializedBytes > maxMountInputBytes {
			return nil, errors.New("Ketcher mount input is invalid or exceeds 2 MiB")
		}
		fieldCount++
	}
	structured := make(map[string]string, 4)
	for _, key := range []string{"smiles", "molfile", "ket", "rxn"} {
		if value, ok := arguments[key].(string); ok {
			structured[key] = value
		}
	}
	return map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": "Opened molecule sketcher."}},
		"structuredContent": structured,
	}, nil
}

// jsonStringUTF8Bytes matches JSON.stringify for a valid Unicode string. In
// particular it does not apply Go encoding/json's optional HTML escaping for
// <, >, &, U+2028, or U+2029. The browser and server therefore enforce one
// semantic 2 MiB mount-input boundary.
func jsonStringUTF8Bytes(value string) (int, bool) {
	if !utf8.ValidString(value) {
		return 0, false
	}
	bytes := 2 // surrounding quotes
	for _, code := range value {
		switch code {
		case '"', '\\', '\b', '\t', '\n', '\f', '\r':
			bytes += 2
		default:
			if code < 0x20 {
				bytes += 6
			} else {
				bytes += utf8.RuneLen(code)
			}
		}
	}
	return bytes, true
}

func readKetcherResource(raw json.RawMessage, options Options) (any, *rpcError) {
	var params struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(raw, &params); err != nil || params.URI != ResourceURI {
		return nil, &rpcError{Code: -32602, Message: "unknown Ketcher resource URI"}
	}
	html, err := loadWidgetHTML(options.WidgetGzipPath, options.WidgetSHA256)
	if err != nil {
		return nil, &rpcError{Code: -32603, Message: "load Ketcher widget: " + err.Error()}
	}
	return map[string]any{
		"contents": []any{map[string]any{"uri": ResourceURI, "mimeType": ResourceMIMEType, "text": string(html)}},
	}, nil
}

func loadWidgetHTML(path, expectedSHA256 string) ([]byte, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	contents, err := io.ReadAll(io.LimitReader(reader, maxWidgetBytes+1))
	if err != nil {
		return nil, err
	}
	if len(contents) > maxWidgetBytes {
		return nil, fmt.Errorf("decompressed widget exceeds %d bytes", maxWidgetBytes)
	}
	digest := sha256.Sum256(contents)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), expectedSHA256) {
		return nil, errors.New("decompressed widget checksum mismatch")
	}
	return contents, nil
}

// LoadWidgetHTML returns the verified, decompressed browser application.
// Callers must serve the result in a sandboxed browsing context.
func LoadWidgetHTML(path string) ([]byte, error) {
	return loadWidgetHTML(path, ExpectedWidgetSHA256)
}

func encodeRPCError(encoder *json.Encoder, id json.RawMessage, code int, message string) error {
	return encoder.Encode(rpcResponse{
		JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message},
	})
}

// DiscoverWidgetPath locates the verified Ketcher payload in source and
// installed layouts. An explicit environment override wins for managed hosts.
func DiscoverWidgetPath() string {
	if configured := strings.TrimSpace(os.Getenv("SYNON_KETCHER_WIDGET_GZIP")); configured != "" {
		return filepath.Clean(configured)
	}
	relative := filepath.Join("assets", "optional", "mcp-servers", "ketcher-chemistry", "widget", "index.html.gz")
	candidates := make([]string, 0, 8)
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(executable), relative))
	}
	if cwd, err := os.Getwd(); err == nil {
		for directory := filepath.Clean(cwd); ; directory = filepath.Dir(directory) {
			candidates = append(candidates, filepath.Join(directory, relative))
			next := filepath.Dir(directory)
			if next == directory {
				break
			}
		}
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			if absolute, err := filepath.Abs(candidate); err == nil {
				return absolute
			}
			return filepath.Clean(candidate)
		}
	}
	return relative
}
