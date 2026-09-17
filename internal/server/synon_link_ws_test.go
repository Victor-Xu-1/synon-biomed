package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"synon-go/internal/synonlink"
)

func TestSynonLinkWebSocketHelloProgressAndResultLifecycle(t *testing.T) {
	link := synonlink.NewService()
	srv := New(Options{SynonLink: link})
	httpServer := httptest.NewServer(srv.Handler())
	defer func() {
		httpServer.CloseClientConnections()
		httpServer.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/synon-link/ws?userId=local"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	connected := readWSMessage(t, ctx, conn)
	if connected["type"] != "connected" || connected["userId"] != "local" {
		t.Fatalf("connected message = %#v", connected)
	}

	writeWSMessage(t, ctx, conn, map[string]any{
		"type":             "hello",
		"clientId":         "sl_ws_client",
		"clientKind":       "browser",
		"name":             "Victor Chrome",
		"browser":          "Chrome",
		"extensionVersion": "0.6.10",
		"capabilities":     []string{"browserSearch", "humanPageRead", "not-real"},
		"supportedActions": []string{"search_web", "read_page", "not_real_action"},
	})
	ack := readWSMessage(t, ctx, conn)
	if ack["type"] != "hello_ack" {
		t.Fatalf("hello ack = %#v", ack)
	}

	clientsResp, err := http.Get(httpServer.URL + "/api/synon-link?userId=local")
	if err != nil {
		t.Fatalf("GET clients error = %v", err)
	}
	defer clientsResp.Body.Close()
	var clientsBody struct {
		Clients []synonlink.Client `json:"clients"`
	}
	if err := json.NewDecoder(clientsResp.Body).Decode(&clientsBody); err != nil {
		t.Fatalf("decode clients: %v", err)
	}
	if len(clientsBody.Clients) != 1 || clientsBody.Clients[0].ID != "sl_ws_client" {
		t.Fatalf("clients = %#v", clientsBody)
	}
	if containsString(clientsBody.Clients[0].Capabilities, "not-real") {
		t.Fatalf("capabilities were not filtered: %#v", clientsBody.Clients[0].Capabilities)
	}

	commandResult := make(chan map[string]any, 1)
	commandError := make(chan error, 1)
	go func() {
		body := []byte(`{"userId":"local","clientId":"sl_ws_client","name":"search_web","payload":{"query":"Synon"}}`)
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, httpServer.URL+"/api/synon-link/commands", bytes.NewReader(body))
		if err != nil {
			commandError <- err
			return
		}
		request.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(request)
		if err != nil {
			commandError <- err
			return
		}
		defer resp.Body.Close()
		var decoded map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			commandError <- err
			return
		}
		commandResult <- decoded
	}()

	command := readWSMessage(t, ctx, conn)
	if command["type"] != "command" || command["name"] != "search_web" {
		t.Fatalf("command = %#v", command)
	}
	commandID, _ := command["commandId"].(string)
	if commandID == "" {
		t.Fatalf("command missing id: %#v", command)
	}

	writeWSMessage(t, ctx, conn, map[string]any{
		"type":      "command_progress",
		"commandId": commandID,
		"status":    "browser_search_read",
		"summary":   "reading search results",
		"progress": map[string]any{
			"percent": float64(50),
		},
	})
	var runningBody struct {
		Running []map[string]any `json:"running"`
	}
	progressDeadline := time.Now().Add(time.Second)
	for {
		runningRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/api/synon-link/running?userId=local", nil)
		if err != nil {
			t.Fatalf("build running request: %v", err)
		}
		runningResp, err := http.DefaultClient.Do(runningRequest)
		if err != nil {
			t.Fatalf("GET running error = %v", err)
		}
		runningBody.Running = nil
		decodeErr := json.NewDecoder(runningResp.Body).Decode(&runningBody)
		runningResp.Body.Close()
		if decodeErr != nil {
			t.Fatalf("decode running: %v", decodeErr)
		}
		if len(runningBody.Running) == 1 && runningBody.Running[0]["commandId"] == commandID && runningBody.Running[0]["status"] == "browser_search_read" {
			break
		}
		if time.Now().After(progressDeadline) {
			t.Fatalf("running progress did not converge = %#v", runningBody)
		}
		time.Sleep(10 * time.Millisecond)
	}

	writeWSMessage(t, ctx, conn, map[string]any{
		"type":      "command_result",
		"commandId": commandID,
		"ok":        true,
		"result": map[string]any{
			"results": []any{"one", "two"},
		},
	})

	select {
	case err := <-commandError:
		t.Fatalf("command API error = %v", err)
	case result := <-commandResult:
		if result["ok"] != true {
			t.Fatalf("command result = %#v", result)
		}
		if result["commandId"] != commandID {
			t.Fatalf("command result id = %#v want %s", result, commandID)
		}
	case <-ctx.Done():
		t.Fatal("command API did not finish")
	}
}

func TestSynonLinkWebSocketDeliversAccessEvents(t *testing.T) {
	link := synonlink.NewService()
	srv := New(Options{SynonLink: link})
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/synon-link/ws?userId=local"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	_ = readWSMessage(t, ctx, conn)
	writeWSMessage(t, ctx, conn, map[string]any{
		"type":             "hello",
		"clientId":         "sl_access_client",
		"clientKind":       "browser",
		"name":             "Synon Link Extension",
		"extensionVersion": "0.6.10",
		"capabilities":     []string{"localFiles"},
		"supportedActions": []string{"local_files_status"},
	})
	if ack := readWSMessage(t, ctx, conn); ack["type"] != "hello_ack" {
		t.Fatalf("hello ack = %#v", ack)
	}

	createResp, err := http.Post(httpServer.URL+"/api/synon-link/access-requests", "application/json", bytes.NewReader([]byte(`{
		"userId":"local",
		"clientId":"sl_access_client",
		"scope":"local_files",
		"reason":"extension should show permission UI"
	}`)))
	if err != nil {
		t.Fatalf("POST access request error = %v", err)
	}
	defer createResp.Body.Close()
	var created struct {
		Request synonlink.AccessRequest `json:"request"`
	}
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatalf("decode created access request: %v", err)
	}
	createdEvent := readWSMessage(t, ctx, conn)
	if createdEvent["type"] != "access_event" || createdEvent["event"] != "access_request_created" {
		t.Fatalf("created access event = %#v", createdEvent)
	}
	createdRequest, _ := createdEvent["request"].(map[string]any)
	if createdRequest["id"] != created.Request.ID || createdRequest["status"] != "pending" {
		t.Fatalf("created access event request = %#v", createdRequest)
	}

	decisionResp, err := http.Post(httpServer.URL+"/api/synon-link/access-requests/"+created.Request.ID+"/decision", "application/json", bytes.NewReader([]byte(`{
		"userId":"local",
		"approved":true,
		"reason":"approved in extension UI"
	}`)))
	if err != nil {
		t.Fatalf("POST access decision error = %v", err)
	}
	defer decisionResp.Body.Close()
	decisionEvent := readWSMessage(t, ctx, conn)
	if decisionEvent["type"] != "access_event" || decisionEvent["event"] != "access_request_decided" {
		t.Fatalf("decision access event = %#v", decisionEvent)
	}
	decisionRequest, _ := decisionEvent["request"].(map[string]any)
	if decisionRequest["id"] != created.Request.ID || decisionRequest["status"] != "granted" || decisionRequest["decisionReason"] != "approved in extension UI" {
		t.Fatalf("decision access event request = %#v", decisionRequest)
	}
}

func readWSMessage(t *testing.T, ctx context.Context, conn *websocket.Conn) map[string]any {
	t.Helper()
	messageType, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("websocket read error = %v", err)
	}
	if messageType != websocket.MessageText {
		t.Fatalf("message type = %v", messageType)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode websocket message %q: %v", data, err)
	}
	return decoded
}

func writeWSMessage(t *testing.T, ctx context.Context, conn *websocket.Conn, value map[string]any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal websocket message: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("websocket write error = %v", err)
	}
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
