package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: smoke-ws-client <ws-url>")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, os.Args[1], nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dial: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	connected := read(ctx, conn)
	if connected["type"] != "connected" {
		fmt.Fprintf(os.Stderr, "unexpected connected message: %#v\n", connected)
		os.Exit(1)
	}

	write(ctx, conn, map[string]any{
		"type":             "hello",
		"clientId":         "sl_smoke_client",
		"clientKind":       "browser",
		"name":             "Smoke Chrome",
		"extensionVersion": "0.6.10",
		"capabilities":     []string{"browserSearch", "humanPageRead", "scrollPage", "visualClick", "captureVisibleTab", "downloads", "downloadDiscovery", "tabs"},
		"supportedActions": []string{"search_web", "read_page", "scroll_page", "click_at", "screenshot", "download_file"},
	})

	ack := read(ctx, conn)
	if ack["type"] != "hello_ack" {
		fmt.Fprintf(os.Stderr, "unexpected hello ack: %#v\n", ack)
		os.Exit(1)
	}

	exerciseAccessLifecycle(ctx, os.Args[1])

	resultCh := make(chan map[string]any, 1)
	errorCh := make(chan error, 1)
	go executeSynonLinkTool(ctx, os.Args[1], "search_web", map[string]any{"query": "Synon smoke"}, resultCh, errorCh)

	command := readCommand(ctx, conn, "search_web")
	commandID, _ := command["commandId"].(string)
	if commandID == "" {
		fmt.Fprintf(os.Stderr, "command missing id: %#v\n", command)
		os.Exit(1)
	}
	write(ctx, conn, map[string]any{
		"type":      "command_result",
		"commandId": commandID,
		"ok":        true,
		"result": map[string]any{
			"smoke": true,
		},
	})

	select {
	case err := <-errorCh:
		fmt.Fprintf(os.Stderr, "synon_link tool execution: %v\n", err)
		os.Exit(1)
	case result := <-resultCh:
		if result["ok"] != true || result["commandId"] != commandID {
			fmt.Fprintf(os.Stderr, "unexpected synon_link tool result: %#v\n", result)
			os.Exit(1)
		}
	case <-ctx.Done():
		fmt.Fprintf(os.Stderr, "synon_link tool execution timed out: %v\n", ctx.Err())
		os.Exit(1)
	}

	resultCh = make(chan map[string]any, 1)
	errorCh = make(chan error, 1)
	go executeSynonLinkTool(ctx, os.Args[1], "scroll_page", map[string]any{"direction": "down"}, resultCh, errorCh)
	command = readCommand(ctx, conn, "scroll_page")
	commandID, _ = command["commandId"].(string)
	if commandID == "" {
		fmt.Fprintf(os.Stderr, "browser protocol command missing id: %#v\n", command)
		os.Exit(1)
	}
	write(ctx, conn, map[string]any{
		"type":      "command_result",
		"commandId": commandID,
		"ok":        true,
		"result": map[string]any{
			"scrolled": true,
		},
	})
	select {
	case err := <-errorCh:
		fmt.Fprintf(os.Stderr, "synon_link browser protocol execution: %v\n", err)
		os.Exit(1)
	case result := <-resultCh:
		if result["ok"] != true || result["commandId"] != commandID {
			fmt.Fprintf(os.Stderr, "unexpected browser protocol tool result: %#v\n", result)
			os.Exit(1)
		}
	case <-ctx.Done():
		fmt.Fprintf(os.Stderr, "synon_link browser protocol execution timed out: %v\n", ctx.Err())
		os.Exit(1)
	}

	fmt.Println("SYNON_LINK_WEBSOCKET=yes")
	fmt.Println("SYNON_LINK_ACCESS_LIFECYCLE=yes")
	fmt.Println("SYNON_LINK_TOOL_EXECUTE=yes")
	fmt.Println("SYNON_LINK_BROWSER_PROTOCOL=yes")
}

func exerciseAccessLifecycle(ctx context.Context, wsURL string) {
	createBody := map[string]any{
		"userId":   "local",
		"clientId": "sl_smoke_client",
		"scope":    "read_page",
		"reason":   "smoke access lifecycle",
	}
	created, err := requestJSON(ctx, wsURL, http.MethodPost, "/api/synon-link/access-requests", createBody)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create access request: %v\n", err)
		os.Exit(1)
	}
	request := mapValue(created["request"])
	requestID := stringValue(request["id"])
	if requestID == "" || request["status"] != "pending" {
		fmt.Fprintf(os.Stderr, "unexpected access create result: %#v\n", created)
		os.Exit(1)
	}

	listed, err := requestJSON(ctx, wsURL, http.MethodGet, "/api/synon-link/access-requests?userId=local&status=pending", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list access requests: %v\n", err)
		os.Exit(1)
	}
	requests, _ := listed["requests"].([]any)
	if len(requests) != 1 {
		fmt.Fprintf(os.Stderr, "unexpected access list result: %#v\n", listed)
		os.Exit(1)
	}

	decided, err := requestJSON(ctx, wsURL, http.MethodPost, "/api/synon-link/access-requests/"+url.PathEscape(requestID)+"/decision", map[string]any{
		"userId":   "local",
		"approved": true,
		"reason":   "smoke approved",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "decide access request: %v\n", err)
		os.Exit(1)
	}
	if mapValue(decided["request"])["status"] != "granted" {
		fmt.Fprintf(os.Stderr, "unexpected access decision result: %#v\n", decided)
		os.Exit(1)
	}

	revoked, err := requestJSON(ctx, wsURL, http.MethodDelete, "/api/synon-link/access-requests/"+url.PathEscape(requestID)+"?userId=local&reason=smoke", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "revoke access request: %v\n", err)
		os.Exit(1)
	}
	if mapValue(revoked["request"])["status"] != "revoked" {
		fmt.Fprintf(os.Stderr, "unexpected access revoke result: %#v\n", revoked)
		os.Exit(1)
	}
}

func executeSynonLinkTool(ctx context.Context, wsURL string, action string, payload map[string]any, resultCh chan<- map[string]any, errorCh chan<- error) {
	endpoint, err := toolExecuteURL(wsURL)
	if err != nil {
		errorCh <- err
		return
	}
	body := map[string]any{
		"input": map[string]any{
			"userId":   "local",
			"clientId": "sl_smoke_client",
			"action":   action,
			"payload":  payload,
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		errorCh <- err
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		errorCh <- err
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		errorCh <- err
		return
	}
	defer resp.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		errorCh <- err
		return
	}
	if resp.StatusCode != http.StatusOK {
		errorCh <- fmt.Errorf("status %d body %#v", resp.StatusCode, decoded)
		return
	}
	resultCh <- decoded
}

func toolExecuteURL(wsURL string) (string, error) {
	return httpURL(wsURL, "/api/tools/synon_link/execute")
}

func requestJSON(ctx context.Context, wsURL string, method string, path string, body map[string]any) (map[string]any, error) {
	endpoint, err := httpURL(wsURL, path)
	if err != nil {
		return nil, err
	}
	var payload *bytes.Reader
	if body == nil {
		payload = bytes.NewReader(nil)
	} else {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d body %#v", resp.StatusCode, decoded)
	}
	return decoded, nil
}

func httpURL(wsURL string, path string) (string, error) {
	parsed, err := url.Parse(wsURL)
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "wss" {
		parsed.Scheme = "https"
	} else {
		parsed.Scheme = "http"
	}
	if queryIndex := strings.Index(path, "?"); queryIndex >= 0 {
		parsed.Path = path[:queryIndex]
		parsed.RawQuery = path[queryIndex+1:]
	} else {
		parsed.Path = path
		parsed.RawQuery = ""
	}
	return parsed.String(), nil
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func mapValue(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func read(ctx context.Context, conn *websocket.Conn) map[string]any {
	_, data, err := conn.Read(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read: %v\n", err)
		os.Exit(1)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		fmt.Fprintf(os.Stderr, "decode: %v\n", err)
		os.Exit(1)
	}
	return decoded
}

func readCommand(ctx context.Context, conn *websocket.Conn, expectedName string) map[string]any {
	for {
		message := read(ctx, conn)
		switch message["type"] {
		case "access_event":
			continue
		case "command":
			if message["name"] == expectedName {
				return message
			}
		}
		fmt.Fprintf(os.Stderr, "unexpected command while waiting for %s: %#v\n", expectedName, message)
		os.Exit(1)
	}
}

func write(ctx context.Context, conn *websocket.Conn, value map[string]any) {
	data, err := json.Marshal(value)
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal: %v\n", err)
		os.Exit(1)
	}
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		os.Exit(1)
	}
}
