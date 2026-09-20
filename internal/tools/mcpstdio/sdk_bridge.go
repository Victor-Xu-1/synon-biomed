package mcpstdio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

func startSDKBridgeSession(ctx context.Context, root string, config ServerConfig) (*sdkSession, error) {
	command, args, ok, err := config.sdkBridgeCommand()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("sdk MCP server %q requires %s or bridgeCommand", config.sdkServerName(), sdkBridgeCommandEnv)
	}
	command = strings.TrimSpace(command)
	if err := validateMCPProcessSpec(command, args); err != nil {
		return nil, err
	}
	environment, err := buildMCPEnvironment(
		config.Env,
		config.BridgeEnv,
		trustedMCPEnvironment(config, map[string]string{
			"SYNON_MCP_SDK_SERVER_NAME": config.sdkServerName(),
		}),
	)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, command, args...)
	if root != "" {
		cmd.Dir = root
	}
	cmd.Env = environment
	cmd.WaitDelay = 2 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	process, err := prepareMCPProcess(cmd)
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = process.close()
		return nil, err
	}
	if err := process.attach(cmd); err != nil {
		_ = process.kill(cmd)
		_ = cmd.Wait()
		_ = process.close()
		return nil, fmt.Errorf("contain SDK MCP bridge %s: %w", command, err)
	}
	if err := ctx.Err(); err != nil {
		_ = process.kill(cmd)
		_ = cmd.Wait()
		_ = process.close()
		return nil, err
	}
	sess := &sdkSession{
		serverName: config.sdkServerName(),
		command:    command,
		cmd:        cmd,
		process:    process,
		stdin:      stdin,
		stdout:     stdout,
		stderrPipe: stderr,
		reader:     bufio.NewReaderSize(stdout, mcpResponseBufferBytes),
		encoder:    json.NewEncoder(stdin),
		stderr:     &limitedBuffer{},
	}
	go sess.stderr.readFrom(stderr)
	return sess, nil
}

func (s *session) request(ctx context.Context, id int, method string, params any) (json.RawMessage, error) {
	stopStreams := stopMCPStreamsOnContext(ctx, s.stdout, s.stdin, s.stderrPipe)
	defer stopStreams()
	request := rpcMessage{JSONRPC: "2.0", ID: id, Method: method}
	if params != nil {
		request.Params = params
	}
	if err := s.send(request); err != nil {
		if contextErr := contextError(ctx); contextErr != nil {
			return nil, contextErr
		}
		return nil, err
	}
	for {
		msg, err := readMCPStdioMessage(ctx, s.reader, false)
		if err != nil {
			if contextErr := contextError(ctx); contextErr != nil {
				return nil, contextErr
			}
			if errors.Is(err, io.EOF) {
				break
			}
			var syntax *json.SyntaxError
			if errors.As(err, &syntax) {
				continue
			} // Ignore non-protocol stdout lines.
			return nil, fmt.Errorf("read MCP response from %s: %w; stderr: %s", s.command, err, s.stderr.String())
		}
		if msg.Method != "" && msg.ID != nil {
			_ = s.respondToServerRequest(msg)
			continue
		}
		if !idMatches(msg.ID, id) {
			continue
		}
		if msg.Error != nil {
			return nil, rpcErrorFor(method, msg.Error, 0)
		}
		return msg.Result, nil
	}
	if contextErr := contextError(ctx); contextErr != nil {
		return nil, contextErr
	}
	return nil, fmt.Errorf("MCP server %s closed stdout before %s response (possible request timeout or upstream outage); retry the call after a short delay; stderr: %s", s.command, method, s.stderr.String())
}

func (s *session) notify(ctx context.Context, method string, params any) error {
	stopStreams := stopMCPStreamsOnContext(ctx, s.stdout, s.stdin, s.stderrPipe)
	defer stopStreams()
	if err := contextError(ctx); err != nil {
		return err
	}
	msg := rpcMessage{JSONRPC: "2.0", Method: method}
	if params != nil {
		msg.Params = params
	}
	if err := s.send(msg); err != nil {
		if contextErr := contextError(ctx); contextErr != nil {
			return contextErr
		}
		return err
	}
	return nil
}

func (s *session) respondToServerRequest(msg rpcMessage) error {
	switch msg.Method {
	case "roots/list":
		return s.send(rpcMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON(map[string]any{"roots": []any{}})})
	default:
		return s.send(rpcMessage{JSONRPC: "2.0", ID: msg.ID, Error: &rpcError{Code: -32601, Message: "method not supported by synon-go MCP client"}})
	}
}

func (s *session) send(msg rpcMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.encoder.Encode(msg)
}

func (s *session) close() {
	s.closeOnce.Do(func() {
		if s.stdout != nil {
			_ = s.stdout.Close()
		}
		if s.stderrPipe != nil {
			_ = s.stderrPipe.Close()
		}
		_ = s.stdin.Close()
		_ = s.process.kill(s.cmd)
		_ = s.cmd.Wait()
		_ = s.process.close()
	})
}

func (s *sdkSession) request(ctx context.Context, id int, method string, params any) (json.RawMessage, error) {
	stopStreams := stopMCPStreamsOnContext(ctx, s.stdout, s.stdin, s.stderrPipe)
	defer stopStreams()
	request := rpcMessage{JSONRPC: "2.0", ID: id, Method: method}
	if params != nil {
		request.Params = params
	}
	if err := s.send(request); err != nil {
		if contextErr := contextError(ctx); contextErr != nil {
			return nil, contextErr
		}
		return nil, err
	}
	for {
		msg, err := readMCPStdioMessage(ctx, s.reader, true)
		if err != nil {
			if contextErr := contextError(ctx); contextErr != nil {
				return nil, contextErr
			}
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if msg.Method != "" && msg.ID != nil {
			_ = s.respondToServerRequest(msg)
			continue
		}
		if !idMatches(msg.ID, id) {
			continue
		}
		if msg.Error != nil {
			return nil, rpcErrorFor(method, msg.Error, 0)
		}
		return msg.Result, nil
	}
	if contextErr := contextError(ctx); contextErr != nil {
		return nil, contextErr
	}
	return nil, fmt.Errorf("SDK MCP bridge %s closed stdout before %s response (possible request timeout or upstream outage); retry the call after a short delay; stderr: %s", s.command, method, s.stderr.String())
}

func (s *sdkSession) notify(ctx context.Context, method string, params any) error {
	stopStreams := stopMCPStreamsOnContext(ctx, s.stdout, s.stdin, s.stderrPipe)
	defer stopStreams()
	if err := contextError(ctx); err != nil {
		return err
	}
	msg := rpcMessage{JSONRPC: "2.0", Method: method}
	if params != nil {
		msg.Params = params
	}
	if err := s.send(msg); err != nil {
		if contextErr := contextError(ctx); contextErr != nil {
			return contextErr
		}
		return err
	}
	for {
		decoded, err := readMCPStdioMessage(ctx, s.reader, true)
		if err != nil {
			if contextErr := contextError(ctx); contextErr != nil {
				return contextErr
			}
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
		if decoded.Method != "" && decoded.ID != nil {
			_ = s.respondToServerRequest(decoded)
			continue
		}
		if decoded.Error != nil {
			return fmt.Errorf("SDK MCP %s failed: %s", method, decoded.Error.Message)
		}
		return nil
	}
	if contextErr := contextError(ctx); contextErr != nil {
		return contextErr
	}
	return fmt.Errorf("SDK MCP bridge %s closed stdout before %s notification response; stderr: %s", s.command, method, s.stderr.String())
}

func (s *sdkSession) respondToServerRequest(msg rpcMessage) error {
	switch msg.Method {
	case "roots/list":
		return s.send(rpcMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON(map[string]any{"roots": []any{}})})
	default:
		return s.send(rpcMessage{JSONRPC: "2.0", ID: msg.ID, Error: &rpcError{Code: -32601, Message: "method not supported by synon-go SDK MCP bridge"}})
	}
}

func (s *sdkSession) send(msg rpcMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.encoder.Encode(map[string]any{
		"subtype":     "mcp_message",
		"server_name": s.serverName,
		"message":     msg,
	})
}

func (s *sdkSession) close() {
	s.closeOnce.Do(func() {
		if s.stdout != nil {
			_ = s.stdout.Close()
		}
		if s.stderrPipe != nil {
			_ = s.stderrPipe.Close()
		}
		_ = s.stdin.Close()
		_ = s.process.kill(s.cmd)
		_ = s.cmd.Wait()
		_ = s.process.close()
	})
}

func decodeSDKBridgeLine(raw []byte) (rpcMessage, bool, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return rpcMessage{}, false, nil
	}
	var wrapped struct {
		MCPResponse json.RawMessage `json:"mcp_response"`
		Response    json.RawMessage `json:"response"`
		Error       string          `json:"error"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return rpcMessage{}, false, err
	}
	if strings.TrimSpace(wrapped.Error) != "" {
		return rpcMessage{}, false, errors.New(wrapped.Error)
	}
	payload := wrapped.MCPResponse
	if len(bytes.TrimSpace(payload)) == 0 {
		payload = wrapped.Response
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		payload = raw
	}
	if bytes.Equal(bytes.TrimSpace(payload), []byte("null")) {
		return rpcMessage{JSONRPC: "2.0", Result: json.RawMessage(`null`)}, true, nil
	}
	var msg rpcMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return rpcMessage{}, false, err
	}
	return msg, true, nil
}
