package webfetch

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// Opt-in elapsed-time regression uses the production secure client factory.
// Local TLS serves a public-name fixture; no external document is downloaded.
func TestFetchSlowNetworkBeyondFormerWallClockBudget(t *testing.T) {
	if os.Getenv("SYNON_TEST_SLOW_NETWORK") != "1" {
		t.Skip("opt-in 28-second real TLS regression")
	}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(10 * time.Second):
		}
		w.Header().Set("Content-Type", "text/plain")
		for index := 0; index < 18; index++ {
			_, _ = io.WriteString(w, "part\n")
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(time.Second):
			}
		}
	}))
	defer upstream.Close()
	transport := upstream.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.InsecureSkipVerify = true // Fixture-only public name.
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	defer transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	started := time.Now()
	result, err := FetchWithOptions(ctx, "https://example.com/document.txt", 4096, Options{BaseHTTPClient: &http.Client{Transport: transport}})
	if err != nil || !result.Complete || result.SourceUnavailable || result.Body != strings.Repeat("part\n", 18) {
		t.Fatalf("slow source incomplete: bytes=%d complete=%t unavailable=%t error=%v", result.BytesRead, result.Complete, result.SourceUnavailable, err)
	}
	t.Logf("secure_factory_elapsed=%s bytes=%d complete=%t attempts=%d", time.Since(started), result.BytesRead, result.Complete, len(result.Attempts))
}
