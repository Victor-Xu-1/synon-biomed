package securefetch

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"
)

type transferZeroReader struct{}

func (transferZeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

// Opt-in real TLS transfer: constant memory and no multi-GB fixture on disk.
// Small default CI tests separately cover timeouts, resume and cancellation.
func TestClientLargeTransferRealTLS(t *testing.T) {
	size, err := strconv.ParseInt(os.Getenv("SYNON_TEST_TRANSFER_BYTES"), 10, 64)
	if os.Getenv("SYNON_TEST_TRANSFER_BYTES") == "" {
		t.Skip("opt-in multi-GB transfer")
	}
	if err != nil || size <= 0 {
		t.Fatal("positive transfer byte count required")
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "chemical/x-pdb")
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		_, _ = io.CopyN(w, transferZeroReader{}, size)
	}))
	defer server.Close()
	client, target, policy := testClient(t, server)
	policy.MaxBytes = size
	policy.LongLivedTransfer, policy.TransferIdleTimeout = true, 10*time.Second
	policy.Timeout = 0
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	response, err := client.Fetch(ctx, target+"/large", policy)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	n, err := io.Copy(io.Discard, response.Body)
	if err != nil || n != size {
		t.Fatalf("streamed %d/%d: %v", n, size, err)
	}
	t.Logf("real TLS streamed %d bytes without a total transfer deadline", n)
}
