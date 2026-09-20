package mcpstdio

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/coder/websocket"
	"os/exec"
	"strings"
	"time"
)

func (s *remoteWebSocketSession) notify(ctx context.Context, method string, params any) error {
	message := rpcMessage{JSONRPC: "2.0", Method: method}
	if params != nil {
		message.Params = params
	}
	return s.send(ctx, message)
}

func (s *remoteWebSocketSession) respondToServerRequest(ctx context.Context, msg rpcMessage) error {
	switch msg.Method {
	case "roots/list":
		return s.send(ctx, rpcMessage{JSONRPC: "2.0", ID: msg.ID, Result: mustRawJSON(map[string]any{"roots": []any{}})})
	default:
		return s.send(ctx, rpcMessage{JSONRPC: "2.0", ID: msg.ID, Error: &rpcError{Code: -32601, Message: "method not supported by synon-go MCP WebSocket client"}})
	}
}

func (s *remoteWebSocketSession) send(ctx context.Context, msg rpcMessage) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return s.conn.Write(ctx, websocket.MessageText, raw)
}

func startSession(ctx context.Context, root string, config ServerConfig) (*session, error) {
	command := strings.TrimSpace(config.Command)
	if err := validateMCPProcessSpec(command, config.Args); err != nil {
		return nil, err
	}
	environment, err := buildMCPEnvironment(
		config.Env,
		trustedMCPEnvironment(config),
	)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, command, config.Args...)
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
		return nil, fmt.Errorf("contain MCP subprocess %s: %w", command, err)
	}
	if err := ctx.Err(); err != nil {
		_ = process.kill(cmd)
		_ = cmd.Wait()
		_ = process.close()
		return nil, err
	}
	sess := &session{
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
