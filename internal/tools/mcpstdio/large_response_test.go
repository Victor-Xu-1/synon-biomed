package mcpstdio

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestRemoteMCPFullResponseBeyondOldTotalLimit(t *testing.T) {
	for _, media := range []string{"application/json", "text/event-stream"} {
		t.Run(media, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			value := strings.Repeat("x", 11<<20) + "remote-tail-λ"
			ctx, config := remoteMetadataFixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", media)
				if media == "text/event-stream" {
					_, _ = io.WriteString(w, "data: ")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"body": value}})
				if media == "text/event-stream" {
					_, _ = io.WriteString(w, "\n")
				}
			})
			raw, err := remoteHTTPRPC(ctx, t.TempDir(), config, nil, 1, "tools/call", map[string]any{"name": "large"})
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Body string `json:"body"`
			}
			if err := json.Unmarshal(raw, &result); err != nil || result.Body != value {
				t.Fatalf("remote result lost bytes: %v", err)
			}
			assertMCPResponseSpoolsRemoved(t)
		})
	}
}

func TestRemoteMCPCancelledAndShortLargeResponsesNeverSucceed(t *testing.T) {
	for _, cancelTransfer := range []bool{false, true} {
		t.Run(map[bool]string{false: "short", true: "cancelled"}[cancelTransfer], func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			started := make(chan struct{})
			ctx, config := remoteMetadataFixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"body":"`+strings.Repeat("x", 1<<20))
				w.(http.Flusher).Flush()
				close(started)
				if cancelTransfer {
					<-r.Context().Done()
				}
			})
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := remoteHTTPRPC(ctx, t.TempDir(), config, nil, 1, "tools/call", nil); done <- err }()
			<-started
			if cancelTransfer {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil || cancelTransfer && !errors.Is(err, context.Canceled) {
					t.Fatalf("incomplete result disposition: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("incomplete result retained live transport")
			}
			assertMCPResponseSpoolsRemoved(t)
		})
	}
}

func TestWebSocketMCPFullResponseBeyondOldTotalLimit(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	value := strings.Repeat("x", 11<<20) + "websocket-tail"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		if _, _, err := conn.Read(r.Context()); err != nil {
			t.Error(err)
			return
		}
		writer, err := conn.Writer(r.Context(), websocket.MessageText)
		if err != nil {
			t.Error(err)
			return
		}
		if err := json.NewEncoder(writer).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"body": value}}); err != nil {
			t.Error(err)
		}
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
		_, _, _ = conn.Read(r.Context())
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http://", "ws://", 1), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(-1)
	session := remoteWebSocketSession{conn: conn}
	raw, err := session.request(ctx, 1, "tools/call", nil)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || result.Body != value {
		t.Fatalf("websocket result lost bytes: %v", err)
	}
	assertMCPResponseSpoolsRemoved(t)
}

func TestStdioMCPFullResponseBeyondOldTotalLimit(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	session, err := startSession(context.Background(), t.TempDir(), ServerConfig{
		Command: "python3", Args: []string{"-c", `import sys,json
for line in sys.stdin:
 r=json.loads(line)
 print(json.dumps({'jsonrpc':'2.0','id':r['id'],'result':{'body':'x'*(33*1024*1024)+'stdio-tail'}}),flush=True)
`},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	raw, err := session.request(context.Background(), 1, "tools/call", nil)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || len(result.Body) != (33<<20)+10 || !strings.HasSuffix(result.Body, "stdio-tail") {
		t.Fatalf("stdio result lost bytes: %v", err)
	}
	assertMCPResponseSpoolsRemoved(t)
}

func assertMCPResponseSpoolsRemoved(t *testing.T) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(os.TempDir(), "synon-mcp-response-*"))
	if err != nil || len(paths) != 0 {
		t.Fatalf("MCP transport spool leaked: %v %v", paths, err)
	}
}
