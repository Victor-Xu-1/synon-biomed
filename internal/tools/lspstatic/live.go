package lspstatic

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultLiveLSPTimeout = 10 * time.Second
const maxLiveLSPMessageBytes = 10 * 1024 * 1024

type liveLSPConfig struct {
	Command               string            `json:"command"`
	Args                  []string          `json:"args,omitempty"`
	Env                   map[string]string `json:"env,omitempty"`
	ExtensionToLanguage   map[string]string `json:"extensionToLanguage"`
	Transport             string            `json:"transport,omitempty"`
	SocketAddress         string            `json:"socketAddress,omitempty"`
	InitializationOptions any               `json:"initializationOptions,omitempty"`
	Settings              any               `json:"settings,omitempty"`
	WorkspaceFolder       string            `json:"workspaceFolder,omitempty"`
	StartupTimeout        int               `json:"startupTimeout,omitempty"`
	ShutdownTimeout       int               `json:"shutdownTimeout,omitempty"`
	RestartOnCrash        bool              `json:"restartOnCrash,omitempty"`
	MaxRestarts           int               `json:"maxRestarts,omitempty"`
}

type liveLSPFileConfig map[string]liveLSPConfig

type lspRPCMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *lspRPCError    `json:"error,omitempty"`
}

type lspRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type liveLSPSession struct {
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	conn       io.Closer
	reader     *bufio.Reader
	writer     *bufio.Writer
	serverName string
	mu         sync.Mutex
	opMu       sync.Mutex
}

type pooledLiveLSPSession struct {
	key        string
	root       string
	serverName string
	config     liveLSPConfig
	session    *liveLSPSession
	createdAt  time.Time
	lastUsedAt time.Time
}

var liveLSPPool = struct {
	sync.Mutex
	sessions map[string]*pooledLiveLSPSession
}{sessions: map[string]*pooledLiveLSPSession{}}

func runLive(ctx context.Context, input Input, root string, displayPath string) (Output, bool, error) {
	if !liveSupportedOperation(input.Operation) {
		return Output{}, false, nil
	}
	configs, err := loadLiveLSPConfigs(root)
	if err != nil {
		return Output{}, true, err
	}
	if len(configs) == 0 {
		return Output{}, false, nil
	}
	config, serverName, languageID, ok := selectLiveLSPConfig(configs, input.FilePath)
	if !ok {
		return Output{}, false, nil
	}
	if !isSupportedLiveTransport(config.Transport) {
		return Output{}, true, fmt.Errorf("LSP server %q transport %q is not supported by synon-go live bridge yet", serverName, config.Transport)
	}
	timeout := liveLSPTimeout(config)
	liveCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	pooled, reused, err := getPooledLiveLSPSession(liveCtx, root, serverName, config)
	if err != nil {
		return Output{}, true, err
	}
	session := pooled.session
	session.opMu.Lock()
	defer session.opMu.Unlock()
	if needsLiveDocument(input.Operation) {
		if err := session.openDocument(liveCtx, root, input.FilePath, languageID); err != nil {
			discardPooledLiveLSPSession(pooled.key, session, config)
			return Output{}, true, err
		}
	}
	output, err := session.executeOperation(liveCtx, root, input, displayPath, serverName)
	if err != nil {
		discardPooledLiveLSPSession(pooled.key, session, config)
		return Output{}, true, err
	}
	touchPooledLiveLSPSession(pooled.key)
	output.Source = "live"
	output.LiveAttempted = true
	output.LiveServer = serverName
	output.LiveTransport = normalizeLiveTransport(config.Transport)
	output.LanguageID = languageID
	output.LiveSessionReused = reused
	output.LiveSessionPoolSize = LiveSessionPoolSize()
	return output, true, nil
}

func liveSupportedOperation(operation string) bool {
	switch operation {
	case "documentSymbol", "workspaceSymbol", "goToDefinition", "findReferences", "hover", "goToImplementation", "prepareCallHierarchy", "incomingCalls", "outgoingCalls", "diagnostics", "renamePreview":
		return true
	default:
		return false
	}
}

func needsLiveDocument(operation string) bool {
	return operation != "workspaceSymbol"
}

func loadLiveLSPConfigs(root string) (map[string]liveLSPConfig, error) {
	configs, _, _, err := loadLiveLSPConfigsWithSource(root)
	return configs, err
}

func loadLiveLSPConfigsWithSource(root string) (map[string]liveLSPConfig, string, bool, error) {
	if raw := strings.TrimSpace(os.Getenv("SYNON_LSP_CONFIG_JSON")); raw != "" {
		configs, err := parseLiveLSPConfig([]byte(raw), "SYNON_LSP_CONFIG_JSON")
		return configs, "SYNON_LSP_CONFIG_JSON", true, err
	}
	path := strings.TrimSpace(os.Getenv("SYNON_LSP_CONFIG"))
	if path == "" && root != "" {
		path = filepath.Join(root, ".lsp.json")
	}
	if path == "" {
		return map[string]liveLSPConfig{}, "", false, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]liveLSPConfig{}, path, false, nil
	}
	if err != nil {
		return nil, path, false, fmt.Errorf("read LSP config %s: %w", path, err)
	}
	configs, err := parseLiveLSPConfig(raw, path)
	return configs, path, true, err
}

func LiveServerCount(root string) (int, error) {
	configs, err := loadLiveLSPConfigs(root)
	if err != nil {
		return 0, err
	}
	return len(configs), nil
}

func parseLiveLSPConfig(raw []byte, source string) (map[string]liveLSPConfig, error) {
	var configs liveLSPFileConfig
	if err := json.Unmarshal(raw, &configs); err != nil {
		return nil, fmt.Errorf("parse LSP config %s: %w", source, err)
	}
	for name, config := range configs {
		if strings.TrimSpace(config.Command) == "" {
			return nil, fmt.Errorf("LSP server %q command is required", name)
		}
		if config.ExtensionToLanguage == nil || len(config.ExtensionToLanguage) == 0 {
			return nil, fmt.Errorf("LSP server %q extensionToLanguage is required", name)
		}
		if strings.TrimSpace(config.Transport) == "" {
			config.Transport = "stdio"
		}
		if !isSupportedLiveTransport(config.Transport) {
			return nil, fmt.Errorf("LSP server %q transport %q is not supported", name, config.Transport)
		}
		configs[name] = config
	}
	return configs, nil
}

func isSupportedLiveTransport(transport string) bool {
	switch strings.TrimSpace(strings.ToLower(transport)) {
	case "", "stdio", "socket":
		return true
	default:
		return false
	}
}

func normalizeLiveTransport(transport string) string {
	normalized := strings.TrimSpace(strings.ToLower(transport))
	if normalized == "" {
		return "stdio"
	}
	return normalized
}

func getPooledLiveLSPSession(ctx context.Context, root string, serverName string, config liveLSPConfig) (*pooledLiveLSPSession, bool, error) {
	key := liveLSPPoolKey(root, serverName, config)
	liveLSPPool.Lock()
	if pooled := liveLSPPool.sessions[key]; pooled != nil {
		pooled.lastUsedAt = time.Now().UTC()
		liveLSPPool.Unlock()
		return pooled, true, nil
	}
	liveLSPPool.Unlock()

	session, err := startLiveLSPSession(ctx, root, config)
	if err != nil {
		return nil, false, err
	}
	session.serverName = serverName
	if err := initializeLiveLSPSession(ctx, session, root, config); err != nil {
		session.close(config)
		return nil, false, err
	}
	pooled := &pooledLiveLSPSession{
		key:        key,
		root:       root,
		serverName: serverName,
		config:     config,
		session:    session,
		createdAt:  time.Now().UTC(),
		lastUsedAt: time.Now().UTC(),
	}
	liveLSPPool.Lock()
	if existing := liveLSPPool.sessions[key]; existing != nil {
		liveLSPPool.Unlock()
		session.close(config)
		return existing, true, nil
	}
	liveLSPPool.sessions[key] = pooled
	liveLSPPool.Unlock()
	return pooled, false, nil
}

func initializeLiveLSPSession(ctx context.Context, session *liveLSPSession, root string, config liveLSPConfig) error {
	workspaceRoot, err := liveWorkspaceRoot(root, config)
	if err != nil {
		return err
	}
	if _, err := session.request(ctx, 1, "initialize", map[string]any{
		"processId":             nil,
		"rootUri":               fileURI(workspaceRoot),
		"workspaceFolders":      []any{map[string]any{"uri": fileURI(workspaceRoot), "name": filepath.Base(workspaceRoot)}},
		"initializationOptions": config.InitializationOptions,
		"capabilities":          liveClientCapabilities(),
	}); err != nil {
		return err
	}
	if err := session.notify(ctx, "initialized", map[string]any{}); err != nil {
		return err
	}
	if config.Settings != nil {
		_ = session.notify(ctx, "workspace/didChangeConfiguration", map[string]any{"settings": config.Settings})
	}
	return nil
}

func discardPooledLiveLSPSession(key string, session *liveLSPSession, config liveLSPConfig) {
	liveLSPPool.Lock()
	if pooled := liveLSPPool.sessions[key]; pooled != nil && pooled.session == session {
		delete(liveLSPPool.sessions, key)
	}
	liveLSPPool.Unlock()
	go session.close(config)
}

func touchPooledLiveLSPSession(key string) {
	liveLSPPool.Lock()
	defer liveLSPPool.Unlock()
	if pooled := liveLSPPool.sessions[key]; pooled != nil {
		pooled.lastUsedAt = time.Now().UTC()
	}
}

func LiveSessionPoolSize() int {
	liveLSPPool.Lock()
	defer liveLSPPool.Unlock()
	return len(liveLSPPool.sessions)
}

func CloseLiveLSPPool() {
	liveLSPPool.Lock()
	sessions := make([]*pooledLiveLSPSession, 0, len(liveLSPPool.sessions))
	for key, pooled := range liveLSPPool.sessions {
		sessions = append(sessions, pooled)
		delete(liveLSPPool.sessions, key)
	}
	liveLSPPool.Unlock()
	for _, pooled := range sessions {
		pooled.session.close(pooled.config)
	}
}

func liveLSPPoolKey(root string, serverName string, config liveLSPConfig) string {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		absRoot = root
	}
	raw, _ := json.Marshal(config)
	sum := sha256.Sum256(raw)
	return strings.Join([]string{absRoot, serverName, hex.EncodeToString(sum[:])}, "\x00")
}

func liveStatus(root string, input Input, displayPath string) (Output, error) {
	configs, source, present, err := loadLiveLSPConfigsWithSource(root)
	if err != nil {
		return Output{}, err
	}
	names := make([]string, 0, len(configs))
	for name := range configs {
		names = append(names, name)
	}
	sort.Strings(names)
	config, serverName, languageID, matched := selectLiveLSPConfig(configs, input.FilePath)
	lines := []string{
		fmt.Sprintf("LSP status for %s", displayPath),
		fmt.Sprintf("configSource: %s", emptyAsNone(source)),
		fmt.Sprintf("configPresent: %t", present),
		fmt.Sprintf("liveServers: %d", len(names)),
		fmt.Sprintf("liveSessionPoolSize: %d", LiveSessionPoolSize()),
		fmt.Sprintf("pendingDiagnostics: %d", PendingLSPDiagnosticCount()),
	}
	if len(names) > 0 {
		lines = append(lines, "serverNames: "+strings.Join(names, ", "))
	}
	output := Output{
		Operation:           input.Operation,
		FilePath:            displayPath,
		Result:              "",
		ResultCount:         len(names),
		Source:              "status",
		ConfigSource:        source,
		ConfigPresent:       present,
		LiveServers:         names,
		LiveSessionPoolSize: LiveSessionPoolSize(),
		PendingDiagnostics:  PendingLSPDiagnosticCount(),
	}
	if matched {
		transport := normalizeLiveTransport(config.Transport)
		lines = append(lines,
			fmt.Sprintf("matchedServer: %s", serverName),
			fmt.Sprintf("transport: %s", transport),
			fmt.Sprintf("languageId: %s", languageID),
		)
		output.LiveServer = serverName
		output.LiveTransport = transport
		output.LanguageID = languageID
	} else if strings.TrimSpace(input.FilePath) != "" {
		lines = append(lines, "matchedServer: none")
	}
	output.Result = strings.Join(lines, "\n")
	return output, nil
}

func emptyAsNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "none"
	}
	return value
}

func selectLiveLSPConfig(configs map[string]liveLSPConfig, filePath string) (liveLSPConfig, string, string, bool) {
	ext := strings.ToLower(filepath.Ext(filePath))
	names := make([]string, 0, len(configs))
	for name := range configs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		config := configs[name]
		for configuredExt, language := range config.ExtensionToLanguage {
			if strings.ToLower(configuredExt) == ext {
				return config, name, language, true
			}
		}
	}
	if strings.TrimSpace(filePath) == "" {
		for _, name := range names {
			config := configs[name]
			for _, language := range config.ExtensionToLanguage {
				return config, name, language, true
			}
		}
	}
	return liveLSPConfig{}, "", "", false
}

func startLiveLSPSession(ctx context.Context, root string, config liveLSPConfig) (*liveLSPSession, error) {
	if strings.EqualFold(strings.TrimSpace(config.Transport), "socket") {
		return startLiveLSPSocketSession(ctx, root, config)
	}
	cmd := exec.Command(config.Command, config.Args...)
	if root != "" {
		cmd.Dir = root
	}
	cmd.Env = os.Environ()
	for key, value := range config.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &limitedLSPBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &liveLSPSession{cmd: cmd, stdin: stdin, reader: bufio.NewReaderSize(stdout, 64*1024), writer: bufio.NewWriter(stdin)}, nil
}

func startLiveLSPSocketSession(ctx context.Context, root string, config liveLSPConfig) (*liveLSPSession, error) {
	cmd := exec.Command(config.Command, config.Args...)
	if root != "" {
		cmd.Dir = root
	}
	cmd.Env = os.Environ()
	if strings.TrimSpace(config.SocketAddress) != "" {
		cmd.Env = append(cmd.Env, "SYNON_LSP_SOCKET_ADDR="+strings.TrimSpace(config.SocketAddress))
	}
	for key, value := range config.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &limitedLSPBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	address := strings.TrimSpace(config.SocketAddress)
	if address == "" {
		line, err := readSocketAddressLine(ctx, stdout)
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil, fmt.Errorf("read LSP socket address from server stdout: %w", err)
		}
		address = line
	}
	network, dialAddress, err := normalizeLSPDialAddress(address)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, network, dialAddress)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	return &liveLSPSession{cmd: cmd, conn: conn, reader: bufio.NewReaderSize(conn, 64*1024), writer: bufio.NewWriter(conn)}, nil
}

func readSocketAddressLine(ctx context.Context, reader io.Reader) (string, error) {
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		line, err := bufio.NewReader(reader).ReadString('\n')
		done <- result{line: strings.TrimSpace(line), err: err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-done:
		if result.err != nil && strings.TrimSpace(result.line) == "" {
			return "", result.err
		}
		return result.line, nil
	}
}

func normalizeLSPDialAddress(address string) (string, string, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", "", errors.New("LSP socket address is required")
	}
	if strings.HasPrefix(address, "tcp://") {
		parsed, err := url.Parse(address)
		if err != nil {
			return "", "", err
		}
		return "tcp", parsed.Host, nil
	}
	if strings.HasPrefix(address, "unix://") {
		parsed, err := url.Parse(address)
		if err != nil {
			return "", "", err
		}
		return "unix", parsed.Path, nil
	}
	if strings.HasPrefix(address, "unix:") {
		return "unix", strings.TrimPrefix(address, "unix:"), nil
	}
	return "tcp", address, nil
}

func (s *liveLSPSession) executeOperation(ctx context.Context, root string, input Input, displayPath string, serverName string) (Output, error) {
	switch input.Operation {
	case "documentSymbol":
		return s.requestForOutput(ctx, input, displayPath, serverName, "textDocument/documentSymbol", map[string]any{"textDocument": textDocumentIdentifier(root, input.FilePath)})
	case "workspaceSymbol":
		return s.requestForOutput(ctx, input, displayPath, serverName, "workspace/symbol", map[string]any{"query": input.Query})
	case "goToDefinition":
		return s.requestForOutput(ctx, input, displayPath, serverName, "textDocument/definition", textDocumentPositionParams(root, input))
	case "goToImplementation":
		return s.requestForOutput(ctx, input, displayPath, serverName, "textDocument/implementation", textDocumentPositionParams(root, input))
	case "findReferences":
		params := textDocumentPositionParams(root, input)
		params["context"] = map[string]any{"includeDeclaration": true}
		return s.requestForOutput(ctx, input, displayPath, serverName, "textDocument/references", params)
	case "hover":
		return s.requestForOutput(ctx, input, displayPath, serverName, "textDocument/hover", textDocumentPositionParams(root, input))
	case "prepareCallHierarchy":
		return s.requestForOutput(ctx, input, displayPath, serverName, "textDocument/prepareCallHierarchy", textDocumentPositionParams(root, input))
	case "incomingCalls", "outgoingCalls":
		return s.callHierarchy(ctx, root, input, displayPath, serverName)
	case "diagnostics":
		return s.diagnostics(ctx, root, input, displayPath, serverName)
	case "renamePreview":
		params := textDocumentPositionParams(root, input)
		params["newName"] = input.NewName
		return s.requestForOutput(ctx, input, displayPath, serverName, "textDocument/rename", params)
	default:
		return Output{}, fmt.Errorf("unsupported live LSP operation: %s", input.Operation)
	}
}

func (s *liveLSPSession) callHierarchy(ctx context.Context, root string, input Input, displayPath string, serverName string) (Output, error) {
	raw, err := s.request(ctx, 20, "textDocument/prepareCallHierarchy", textDocumentPositionParams(root, input))
	if err != nil {
		return Output{}, err
	}
	items := []json.RawMessage{}
	if err := json.Unmarshal(raw, &items); err != nil || len(items) == 0 {
		return liveOutput(input.Operation, displayPath, serverName, "textDocument/prepareCallHierarchy", raw), nil
	}
	method := "callHierarchy/incomingCalls"
	if input.Operation == "outgoingCalls" {
		method = "callHierarchy/outgoingCalls"
	}
	return s.requestForOutput(ctx, input, displayPath, serverName, method, map[string]any{"item": json.RawMessage(items[0])})
}

func (s *liveLSPSession) diagnostics(ctx context.Context, root string, input Input, displayPath string, serverName string) (Output, error) {
	uri := textDocumentURI(root, input.FilePath)
	for {
		msg, err := s.read(ctx)
		if err != nil {
			return Output{}, err
		}
		if msg.Method != "" && msg.ID != nil {
			_ = s.respondToServerRequest(msg)
			continue
		}
		if msg.Method != "textDocument/publishDiagnostics" {
			continue
		}
		raw, err := json.Marshal(msg.Params)
		if err != nil {
			return Output{}, err
		}
		var decoded struct {
			URI         string            `json:"uri"`
			Diagnostics []json.RawMessage `json:"diagnostics"`
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return Output{}, err
		}
		if decoded.URI != "" && decoded.URI != uri {
			RegisterPendingLSPDiagnostic(serverName, []DiagnosticFile{{URI: decoded.URI, Diagnostics: decoded.Diagnostics}})
			continue
		}
		return liveDiagnosticsOutput(input.Operation, displayPath, serverName, raw, len(decoded.Diagnostics)), nil
	}
}

func (s *liveLSPSession) requestForOutput(ctx context.Context, input Input, displayPath string, serverName string, method string, params any) (Output, error) {
	raw, err := s.request(ctx, 10, method, params)
	if err != nil {
		return Output{}, err
	}
	return liveOutput(input.Operation, displayPath, serverName, method, raw), nil
}

func (s *liveLSPSession) openDocument(ctx context.Context, root string, filePath string, languageID string) error {
	fullPath, _, err := resolve(root, filePath)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return err
	}
	return s.notify(ctx, "textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        fileURI(fullPath),
			"languageId": languageID,
			"version":    1,
			"text":       string(data),
		},
	})
}

func (s *liveLSPSession) request(ctx context.Context, id int, method string, params any) (json.RawMessage, error) {
	request := lspRPCMessage{JSONRPC: "2.0", ID: id, Method: method}
	if params != nil {
		request.Params = params
	}
	if err := s.send(request); err != nil {
		return nil, err
	}
	for {
		msg, err := s.read(ctx)
		if err != nil {
			return nil, err
		}
		if msg.Method != "" && msg.ID != nil {
			_ = s.respondToServerRequest(msg)
			continue
		}
		if registerPublishDiagnostics(s.serverName, msg) {
			continue
		}
		if !lspIDMatches(msg.ID, id) {
			continue
		}
		if msg.Error != nil {
			return nil, fmt.Errorf("LSP %s failed: %s", method, msg.Error.Message)
		}
		return msg.Result, nil
	}
}

func (s *liveLSPSession) notify(ctx context.Context, method string, params any) error {
	_ = ctx
	message := lspRPCMessage{JSONRPC: "2.0", Method: method}
	if params != nil {
		message.Params = params
	}
	return s.send(message)
}

func (s *liveLSPSession) respondToServerRequest(msg lspRPCMessage) error {
	switch msg.Method {
	case "workspace/configuration":
		return s.send(lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON([]any{})})
	default:
		return s.send(lspRPCMessage{JSONRPC: "2.0", ID: msg.ID, Error: &lspRPCError{Code: -32601, Message: "method not supported by synon-go LSP bridge"}})
	}
}

func (s *liveLSPSession) send(msg lspRPCMessage) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := fmt.Fprintf(s.writer, "Content-Length: %d\r\n\r\n", len(raw)); err != nil {
		return err
	}
	if _, err := s.writer.Write(raw); err != nil {
		return err
	}
	return s.writer.Flush()
}

func (s *liveLSPSession) read(ctx context.Context) (lspRPCMessage, error) {
	type result struct {
		msg lspRPCMessage
		err error
	}
	done := make(chan result, 1)
	go func() {
		msg, err := s.readOne()
		done <- result{msg: msg, err: err}
	}()
	select {
	case <-ctx.Done():
		return lspRPCMessage{}, ctx.Err()
	case result := <-done:
		return result.msg, result.err
	}
}

func (s *liveLSPSession) readOne() (lspRPCMessage, error) {
	contentLength := -1
	for {
		line, err := s.reader.ReadString('\n')
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
		return lspRPCMessage{}, errors.New("LSP response missing Content-Length")
	}
	if contentLength > maxLiveLSPMessageBytes {
		return lspRPCMessage{}, fmt.Errorf("LSP response exceeded %d bytes", maxLiveLSPMessageBytes)
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(s.reader, body); err != nil {
		return lspRPCMessage{}, err
	}
	var msg lspRPCMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return lspRPCMessage{}, err
	}
	return msg, nil
}

func (s *liveLSPSession) close(config liveLSPConfig) {
	shutdownTimeout := time.Duration(config.ShutdownTimeout) * time.Millisecond
	if shutdownTimeout <= 0 {
		shutdownTimeout = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	_, _ = s.request(ctx, 90, "shutdown", nil)
	_ = s.notify(ctx, "exit", nil)
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if s.conn != nil {
		_ = s.conn.Close()
	}
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	_ = s.cmd.Wait()
}

func liveOutput(operation string, displayPath string, serverName string, method string, raw json.RawMessage) Output {
	var pretty bytes.Buffer
	if len(raw) > 0 && json.Indent(&pretty, raw, "", "  ") == nil {
		raw = pretty.Bytes()
	}
	count := liveResultCount(raw)
	return Output{
		Operation:   operation,
		FilePath:    displayPath,
		Result:      fmt.Sprintf("Live LSP server %q method %s result:\n%s", serverName, method, strings.TrimSpace(string(raw))),
		ResultCount: count,
		FileCount:   1,
	}
}

func liveResultCount(raw json.RawMessage) int {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0
	}
	var items []any
	if json.Unmarshal(raw, &items) == nil {
		return len(items)
	}
	return 1
}

func liveDiagnosticsOutput(operation string, displayPath string, serverName string, raw json.RawMessage, count int) Output {
	var pretty bytes.Buffer
	if len(raw) > 0 && json.Indent(&pretty, raw, "", "  ") == nil {
		raw = pretty.Bytes()
	}
	return Output{
		Operation:   operation,
		FilePath:    displayPath,
		Result:      fmt.Sprintf("Live LSP server %q notification textDocument/publishDiagnostics result:\n%s", serverName, strings.TrimSpace(string(raw))),
		ResultCount: count,
		FileCount:   1,
	}
}

func registerPublishDiagnostics(serverName string, msg lspRPCMessage) bool {
	if msg.Method != "textDocument/publishDiagnostics" {
		return false
	}
	raw, err := json.Marshal(msg.Params)
	if err != nil {
		return true
	}
	var decoded struct {
		URI         string            `json:"uri"`
		Diagnostics []json.RawMessage `json:"diagnostics"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return true
	}
	RegisterPendingLSPDiagnostic(serverName, []DiagnosticFile{{URI: decoded.URI, Diagnostics: decoded.Diagnostics}})
	return true
}

func textDocumentIdentifier(root string, filePath string) map[string]any {
	return map[string]any{"uri": textDocumentURI(root, filePath)}
}

func textDocumentURI(root string, filePath string) string {
	fullPath, _, err := resolve(root, filePath)
	if err != nil {
		fullPath = filepath.Join(root, filePath)
	}
	return fileURI(fullPath)
}

func textDocumentPositionParams(root string, input Input) map[string]any {
	params := map[string]any{
		"textDocument": textDocumentIdentifier(root, input.FilePath),
		"position": map[string]any{
			"line":      max(0, input.Line-1),
			"character": max(0, input.Character-1),
		},
	}
	return params
}

func liveWorkspaceRoot(root string, config liveLSPConfig) (string, error) {
	if strings.TrimSpace(config.WorkspaceFolder) == "" {
		return filepath.Abs(root)
	}
	candidate := config.WorkspaceFolder
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if err := ensureInside(rootAbs, abs); err != nil {
		return "", err
	}
	return abs, nil
}

func fileURI(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	slash := filepath.ToSlash(abs)
	if !strings.HasPrefix(slash, "/") {
		slash = "/" + slash
	}
	return (&url.URL{Scheme: "file", Path: slash}).String()
}

func liveClientCapabilities() map[string]any {
	return map[string]any{
		"textDocument": map[string]any{
			"documentSymbol": map[string]any{"hierarchicalDocumentSymbolSupport": true},
			"definition":     map[string]any{"linkSupport": true},
			"implementation": map[string]any{"linkSupport": true},
			"references":     map[string]any{},
			"hover":          map[string]any{"contentFormat": []string{"markdown", "plaintext"}},
			"rename":         map[string]any{"prepareSupport": true},
			"callHierarchy":  map[string]any{"dynamicRegistration": false},
		},
		"workspace": map[string]any{
			"symbol":        map[string]any{"symbolKind": map[string]any{}},
			"configuration": true,
		},
	}
}

func liveLSPTimeout(config liveLSPConfig) time.Duration {
	if config.StartupTimeout > 0 {
		return time.Duration(config.StartupTimeout) * time.Millisecond
	}
	if raw := strings.TrimSpace(os.Getenv("SYNON_LSP_TIMEOUT_SECONDS")); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err == nil && seconds > 0 {
			if seconds > 600 {
				seconds = 600
			}
			return time.Duration(seconds) * time.Second
		}
	}
	return defaultLiveLSPTimeout
}

func lspIDMatches(value any, expected int) bool {
	switch typed := value.(type) {
	case float64:
		return int(typed) == expected
	case int:
		return typed == expected
	case string:
		return typed == fmt.Sprint(expected)
	default:
		return false
	}
}

func mustRawJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return raw
}

type limitedLSPBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *limitedLSPBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := maxLiveLSPMessageBytes - len(b.data)
	if remaining > 0 {
		if len(data) > remaining {
			b.data = append(b.data, data[:remaining]...)
		} else {
			b.data = append(b.data, data...)
		}
	}
	return len(data), nil
}
