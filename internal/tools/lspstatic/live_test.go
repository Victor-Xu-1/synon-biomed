package lspstatic

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLiveLSPBridgeUsesExternalStdioServer(t *testing.T) {
	CloseLiveLSPPool()
	defer CloseLiveLSPPool()
	root := t.TempDir()
	source := "package main\n\nfunc LiveSymbol() {}\n"
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte(source), 0o600); err != nil {
		t.Fatalf("write sample: %v", err)
	}
	config := map[string]any{
		"fixture": map[string]any{
			"command": os.Args[0],
			"args":    []string{"-test.run=TestLiveLSPBridgeFixture", "--"},
			"env": map[string]string{
				"GO_WANT_LSP_FIXTURE": "1",
			},
			"extensionToLanguage": map[string]string{".go": "go"},
			"startupTimeout":      5000,
			"shutdownTimeout":     1000,
		},
	}
	rawConfig, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".lsp.json"), rawConfig, 0o600); err != nil {
		t.Fatalf("write lsp config: %v", err)
	}

	output, err := Run(t.Context(), Input{Operation: "documentSymbol", FilePath: "sample.go"}, Options{Root: root})
	if err != nil {
		t.Fatalf("Run live LSP error = %v", err)
	}
	if !strings.Contains(output.Result, "Live LSP server \"fixture\"") {
		t.Fatalf("result did not come from live LSP: %s", output.Result)
	}
	if !strings.Contains(output.Result, "LiveSymbol") {
		t.Fatalf("result missing live symbol: %s", output.Result)
	}
	if output.ResultCount != 1 {
		t.Fatalf("ResultCount = %d, want 1", output.ResultCount)
	}
	if output.Source != "live" || !output.LiveAttempted || output.LiveServer != "fixture" || output.LiveTransport != "stdio" {
		t.Fatalf("live metadata = source:%q attempted:%v server:%q transport:%q", output.Source, output.LiveAttempted, output.LiveServer, output.LiveTransport)
	}
}

func TestLiveLSPBridgeReusesPooledExternalStdioServer(t *testing.T) {
	CloseLiveLSPPool()
	defer CloseLiveLSPPool()
	root := t.TempDir()
	source := "package main\n\nfunc LiveSymbol() {}\n"
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte(source), 0o600); err != nil {
		t.Fatalf("write sample: %v", err)
	}
	startCounter := filepath.Join(root, "starts.txt")
	config := map[string]any{
		"fixture": map[string]any{
			"command": os.Args[0],
			"args":    []string{"-test.run=TestLiveLSPBridgeFixture", "--"},
			"env": map[string]string{
				"GO_WANT_LSP_FIXTURE": "1",
				"LSP_START_COUNTER":   startCounter,
			},
			"extensionToLanguage": map[string]string{".go": "go"},
			"startupTimeout":      5000,
			"shutdownTimeout":     1000,
		},
	}
	rawConfig, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".lsp.json"), rawConfig, 0o600); err != nil {
		t.Fatalf("write lsp config: %v", err)
	}

	first, err := Run(t.Context(), Input{Operation: "documentSymbol", FilePath: "sample.go"}, Options{Root: root})
	if err != nil {
		t.Fatalf("first Run live LSP error = %v", err)
	}
	second, err := Run(t.Context(), Input{Operation: "documentSymbol", FilePath: "sample.go"}, Options{Root: root})
	if err != nil {
		t.Fatalf("second Run live LSP error = %v", err)
	}
	if first.LiveSessionReused {
		t.Fatalf("first call unexpectedly reused session: %#v", first)
	}
	if first.Source != "live" {
		t.Fatalf("first call did not use live LSP: %#v", first)
	}
	if second.Source != "live" {
		t.Fatalf("second call did not use live LSP: %#v", second)
	}
	if !second.LiveSessionReused || second.LiveSessionPoolSize != 1 {
		t.Fatalf("second call pool metadata = reused:%v size:%d", second.LiveSessionReused, second.LiveSessionPoolSize)
	}
	rawStarts, err := os.ReadFile(startCounter)
	if err != nil {
		t.Fatalf("read start counter: %v", err)
	}
	starts := strings.Count(string(rawStarts), "start\n")
	if starts != 1 {
		t.Fatalf("LSP fixture starts = %d, content=%q", starts, string(rawStarts))
	}
}

func TestLiveLSPBridgeReceivesPublishDiagnostics(t *testing.T) {
	CloseLiveLSPPool()
	defer CloseLiveLSPPool()
	root := t.TempDir()
	source := "package main\n\nfunc Broken() {}\n"
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte(source), 0o600); err != nil {
		t.Fatalf("write sample: %v", err)
	}
	writeFixtureLSPConfig(t, root)

	output, err := Run(t.Context(), Input{Operation: "diagnostics", FilePath: "sample.go"}, Options{Root: root})
	if err != nil {
		t.Fatalf("Run live diagnostics error = %v", err)
	}
	if !strings.Contains(output.Result, "textDocument/publishDiagnostics") {
		t.Fatalf("result did not come from live diagnostics: %s", output.Result)
	}
	if !strings.Contains(output.Result, "fixture diagnostic") {
		t.Fatalf("result missing fixture diagnostic: %s", output.Result)
	}
	if output.ResultCount != 1 {
		t.Fatalf("ResultCount = %d, want 1", output.ResultCount)
	}
}

func TestLSPStatusReportsConfigMatchAndPendingDiagnostics(t *testing.T) {
	CloseLiveLSPPool()
	defer CloseLiveLSPPool()
	ResetAllLSPDiagnosticState()
	defer ResetAllLSPDiagnosticState()
	root := t.TempDir()
	source := "package main\n\nfunc LiveSymbol() {}\n"
	samplePath := filepath.Join(root, "sample.go")
	if err := os.WriteFile(samplePath, []byte(source), 0o600); err != nil {
		t.Fatalf("write sample: %v", err)
	}
	writeFixtureLSPConfig(t, root)
	RegisterPendingLSPDiagnostic("fixture", []DiagnosticFile{{URI: fileURI(samplePath), Diagnostics: []json.RawMessage{json.RawMessage(`{"message":"queued"}`)}}})

	output, err := Run(t.Context(), Input{Operation: "status", FilePath: "sample.go"}, Options{Root: root})
	if err != nil {
		t.Fatalf("Run status error = %v", err)
	}
	if output.Source != "status" || !output.ConfigPresent || output.ConfigSource == "" {
		t.Fatalf("status config metadata = source:%q present:%v configSource:%q", output.Source, output.ConfigPresent, output.ConfigSource)
	}
	if output.LiveServer != "fixture" || output.LiveTransport != "stdio" || output.LanguageID != "go" {
		t.Fatalf("status match metadata = server:%q transport:%q language:%q", output.LiveServer, output.LiveTransport, output.LanguageID)
	}
	if len(output.LiveServers) != 1 || output.LiveServers[0] != "fixture" || output.PendingDiagnostics != 1 {
		t.Fatalf("status inventory metadata = servers:%v pending:%d", output.LiveServers, output.PendingDiagnostics)
	}
	for _, want := range []string{"configPresent: true", "matchedServer: fixture", "transport: stdio", "languageId: go"} {
		if !strings.Contains(output.Result, want) {
			t.Fatalf("status output missing %q in %q", want, output.Result)
		}
	}
}

func TestLiveLSPFallbackReportsStaticSourceAndReason(t *testing.T) {
	CloseLiveLSPPool()
	defer CloseLiveLSPPool()
	root := t.TempDir()
	source := "package main\n\nfunc StaticSymbol() {}\n"
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte(source), 0o600); err != nil {
		t.Fatalf("write sample: %v", err)
	}
	config := map[string]any{
		"broken": map[string]any{
			"command":             filepath.Join(root, "missing-lsp-server"),
			"extensionToLanguage": map[string]string{".go": "go"},
		},
	}
	rawConfig, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".lsp.json"), rawConfig, 0o600); err != nil {
		t.Fatalf("write lsp config: %v", err)
	}

	output, err := Run(t.Context(), Input{Operation: "documentSymbol", FilePath: "sample.go"}, Options{Root: root})
	if err != nil {
		t.Fatalf("Run fallback error = %v", err)
	}
	if output.Source != "static" || !output.LiveAttempted || output.FallbackReason == "" {
		t.Fatalf("fallback metadata = source:%q attempted:%v reason:%q", output.Source, output.LiveAttempted, output.FallbackReason)
	}
	if !strings.Contains(output.Result, "Live LSP failed:") || !strings.Contains(output.Result, "StaticSymbol") {
		t.Fatalf("fallback result = %q", output.Result)
	}
}

func TestLiveLSPBridgeUsesExternalSocketServer(t *testing.T) {
	CloseLiveLSPPool()
	defer CloseLiveLSPPool()
	root := t.TempDir()
	source := "package main\n\nfunc SocketSymbol() {}\n"
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte(source), 0o600); err != nil {
		t.Fatalf("write sample: %v", err)
	}
	config := map[string]any{
		"fixture": map[string]any{
			"command": os.Args[0],
			"args":    []string{"-test.run=TestLiveLSPSocketBridgeFixture", "--"},
			"env": map[string]string{
				"GO_WANT_LSP_SOCKET_FIXTURE": "1",
			},
			"extensionToLanguage": map[string]string{".go": "go"},
			"transport":           "socket",
			"startupTimeout":      5000,
			"shutdownTimeout":     1000,
		},
	}
	rawConfig, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".lsp.json"), rawConfig, 0o600); err != nil {
		t.Fatalf("write lsp config: %v", err)
	}

	output, err := Run(t.Context(), Input{Operation: "documentSymbol", FilePath: "sample.go"}, Options{Root: root})
	if err != nil {
		t.Fatalf("Run socket live LSP error = %v", err)
	}
	if !strings.Contains(output.Result, "Live LSP server \"fixture\"") {
		t.Fatalf("result did not come from socket live LSP: %s", output.Result)
	}
	if !strings.Contains(output.Result, "SocketSymbol") {
		t.Fatalf("result missing socket symbol: %s", output.Result)
	}
}

func writeFixtureLSPConfig(t *testing.T, root string) {
	t.Helper()
	config := map[string]any{
		"fixture": map[string]any{
			"command": os.Args[0],
			"args":    []string{"-test.run=TestLiveLSPBridgeFixture", "--"},
			"env": map[string]string{
				"GO_WANT_LSP_FIXTURE": "1",
			},
			"extensionToLanguage": map[string]string{".go": "go"},
			"startupTimeout":      5000,
			"shutdownTimeout":     1000,
		},
	}
	rawConfig, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".lsp.json"), rawConfig, 0o600); err != nil {
		t.Fatalf("write lsp config: %v", err)
	}
}

func TestLiveLSPBridgeFixture(t *testing.T) {
	if os.Getenv("GO_WANT_LSP_FIXTURE") != "1" {
		return
	}
	if counter := os.Getenv("LSP_START_COUNTER"); counter != "" {
		file, err := os.OpenFile(counter, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err == nil {
			_, _ = file.WriteString("start\n")
			_ = file.Close()
		}
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		msg, err := readFixtureLSPMessage(reader)
		if err != nil {
			os.Exit(0)
		}
		switch msg.Method {
		case "initialize":
			writeFixtureLSPMessage(map[string]any{
				"jsonrpc": "2.0",
				"id":      msg.ID,
				"result": map[string]any{
					"capabilities": map[string]any{
						"documentSymbolProvider": true,
						"textDocumentSync":       1,
					},
					"serverInfo": map[string]any{"name": "fixture-lsp", "version": "1.0.0"},
				},
			})
		case "initialized", "exit":
			if msg.Method == "exit" {
				os.Exit(0)
			}
		case "textDocument/didOpen":
			uri := fixtureDidOpenURI(msg)
			writeFixtureLSPMessage(map[string]any{
				"jsonrpc": "2.0",
				"method":  "textDocument/publishDiagnostics",
				"params": map[string]any{
					"uri": uri,
					"diagnostics": []any{map[string]any{
						"range": map[string]any{
							"start": map[string]any{"line": 2, "character": 5},
							"end":   map[string]any{"line": 2, "character": 11},
						},
						"severity": 1,
						"source":   "fixture-lsp",
						"message":  "fixture diagnostic",
					}},
				},
			})
		case "textDocument/documentSymbol":
			writeFixtureLSPMessage(map[string]any{
				"jsonrpc": "2.0",
				"id":      msg.ID,
				"result": []any{map[string]any{
					"name": "LiveSymbol",
					"kind": 12,
					"range": map[string]any{
						"start": map[string]any{"line": 2, "character": 5},
						"end":   map[string]any{"line": 2, "character": 15},
					},
					"selectionRange": map[string]any{
						"start": map[string]any{"line": 2, "character": 5},
						"end":   map[string]any{"line": 2, "character": 15},
					},
				}},
			})
		case "shutdown":
			writeFixtureLSPMessage(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": nil})
		default:
			if msg.ID != nil {
				writeFixtureLSPMessage(map[string]any{
					"jsonrpc": "2.0",
					"id":      msg.ID,
					"error":   map[string]any{"code": -32601, "message": "method not supported in fixture"},
				})
			}
		}
	}
}

func TestLiveLSPSocketBridgeFixture(t *testing.T) {
	if os.Getenv("GO_WANT_LSP_SOCKET_FIXTURE") != "1" {
		return
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(2)
	}
	fmt.Fprintln(os.Stdout, listener.Addr().String())
	conn, err := listener.Accept()
	if err != nil {
		os.Exit(2)
	}
	defer conn.Close()
	defer listener.Close()
	serveFixtureLSP(conn, conn, "SocketSymbol")
}

func serveFixtureLSP(reader io.Reader, writer io.Writer, symbolName string) {
	bufReader := bufio.NewReader(reader)
	for {
		msg, err := readFixtureLSPMessageFrom(bufReader)
		if err != nil {
			os.Exit(0)
		}
		switch msg.Method {
		case "initialize":
			writeFixtureLSPMessageTo(writer, map[string]any{
				"jsonrpc": "2.0",
				"id":      msg.ID,
				"result": map[string]any{
					"capabilities": map[string]any{
						"documentSymbolProvider": true,
						"textDocumentSync":       1,
					},
					"serverInfo": map[string]any{"name": "fixture-lsp", "version": "1.0.0"},
				},
			})
		case "initialized", "exit":
			if msg.Method == "exit" {
				os.Exit(0)
			}
		case "textDocument/didOpen":
			uri := fixtureDidOpenURI(msg)
			writeFixtureLSPMessageTo(writer, map[string]any{
				"jsonrpc": "2.0",
				"method":  "textDocument/publishDiagnostics",
				"params": map[string]any{
					"uri": uri,
					"diagnostics": []any{map[string]any{
						"range": map[string]any{
							"start": map[string]any{"line": 2, "character": 5},
							"end":   map[string]any{"line": 2, "character": 11},
						},
						"severity": 1,
						"source":   "fixture-lsp",
						"message":  "fixture diagnostic",
					}},
				},
			})
		case "textDocument/documentSymbol":
			writeFixtureLSPMessageTo(writer, map[string]any{
				"jsonrpc": "2.0",
				"id":      msg.ID,
				"result": []any{map[string]any{
					"name": symbolName,
					"kind": 12,
					"range": map[string]any{
						"start": map[string]any{"line": 2, "character": 5},
						"end":   map[string]any{"line": 2, "character": 15},
					},
					"selectionRange": map[string]any{
						"start": map[string]any{"line": 2, "character": 5},
						"end":   map[string]any{"line": 2, "character": 15},
					},
				}},
			})
		case "shutdown":
			writeFixtureLSPMessageTo(writer, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": nil})
		default:
			if msg.ID != nil {
				writeFixtureLSPMessageTo(writer, map[string]any{
					"jsonrpc": "2.0",
					"id":      msg.ID,
					"error":   map[string]any{"code": -32601, "message": "method not supported in fixture"},
				})
			}
		}
	}
}

func fixtureDidOpenURI(msg lspRPCMessage) string {
	raw, err := json.Marshal(msg.Params)
	if err != nil {
		return "file://fixture"
	}
	var decoded struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "file://fixture"
	}
	if decoded.TextDocument.URI == "" {
		return "file://fixture"
	}
	return decoded.TextDocument.URI
}

func readFixtureLSPMessage(reader *bufio.Reader) (lspRPCMessage, error) {
	return readFixtureLSPMessageFrom(reader)
}

func readFixtureLSPMessageFrom(reader *bufio.Reader) (lspRPCMessage, error) {
	contentLength := -1
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return lspRPCMessage{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key), "Content-Length") {
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return lspRPCMessage{}, err
			}
			contentLength = parsed
		}
	}
	if contentLength < 0 {
		return lspRPCMessage{}, fmt.Errorf("missing content length")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, body); err != nil {
		return lspRPCMessage{}, err
	}
	var msg lspRPCMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return lspRPCMessage{}, err
	}
	return msg, nil
}

func writeFixtureLSPMessage(value map[string]any) {
	writeFixtureLSPMessageTo(os.Stdout, value)
}

func writeFixtureLSPMessageTo(writer io.Writer, value map[string]any) {
	raw, err := json.Marshal(value)
	if err != nil {
		os.Exit(2)
	}
	_, _ = fmt.Fprintf(writer, "Content-Length: %d\r\n\r\n", len(raw))
	_, _ = writer.Write(raw)
}
