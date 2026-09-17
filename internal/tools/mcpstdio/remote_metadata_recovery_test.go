package mcpstdio

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

func remoteMetadataFixture(t *testing.T, handler http.HandlerFunc) (context.Context, ServerConfig) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	u, _ := url.Parse(server.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} // controlled TLS fixture
	dial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != net.JoinHostPort("8.8.8.8", port) {
			return nil, fmt.Errorf("unexpected test dial target")
		}
		return dial(ctx, network, u.Host)
	}
	t.Cleanup(transport.CloseIdleConnections)
	return withRemoteSecurityFixtureClient(context.Background(), &http.Client{Transport: transport}), ServerConfig{URL: "https://8.8.8.8:" + port}
}

func TestRemoteMetadataTruncatedResponseAndCancellation(t *testing.T) {
	var calls atomic.Int32
	ctx, config := remoteMetadataFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Length", "2000")
			_, _ = w.Write([]byte(`{"jsonrpc":`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`))
	})
	if _, err := remoteHTTPRPC(ctx, t.TempDir(), config, nil, 1, "tools/list", nil); err != nil || calls.Load() != 2 {
		t.Fatalf("partial metadata body failed to recover: %v", err)
	}
	cause := errors.New("user cancelled metadata read")
	ctx, cancel := context.WithCancelCause(ctx)
	cancel(cause)
	if _, err := remoteHTTPRPC(ctx, t.TempDir(), config, nil, 1, "tools/list", nil); !errors.Is(err, cause) || calls.Load() != 2 {
		t.Fatalf("cancelled request ran or lost cause: %v", err)
	}
}

func TestRemoteMetadataEOFRecoversWithoutRepeatingToolExecution(t *testing.T) {
	for _, method := range []string{"tools/list", "resources/list", "prompts/list", "tools/call"} {
		t.Run(method, func(t *testing.T) {
			var calls atomic.Int32
			ctx, config := remoteMetadataFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						_ = conn.Close()
					}
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"tools": []any{}}})
			})
			_, err := remoteHTTPRPC(ctx, t.TempDir(), config, nil, 1, method, nil)
			if method == "tools/call" {
				if err == nil || calls.Load() != 1 {
					t.Fatal("potential mutation was replayed")
				}
				return
			}
			if err != nil || calls.Load() != 2 {
				t.Fatalf("read-only metadata did not recover: calls=%d err=%v", calls.Load(), err)
			}
		})
	}
}

func TestRemoteMetadataPermanentEOFAndHTTPFailureAreBounded(t *testing.T) {
	for _, status := range []int{0, 401, 403, 400} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			ctx, config := remoteMetadataFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if status == 0 {
					conn, _, _ := w.(http.Hijacker).Hijack()
					_ = conn.Close()
					return
				}
				w.WriteHeader(status)
			})
			_, err := remoteHTTPRPC(ctx, t.TempDir(), config, nil, 1, "tools/list", nil)
			want := int32(1)
			if status == 0 {
				want = 2
			}
			if err == nil || calls.Load() != want {
				t.Fatalf("failure retry policy: %d %v", calls.Load(), err)
			}
		})
	}
}
