//go:build linux

package kernel

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"
)

// Exercise TLS and redirects through the actual execution proxy, not a host
// curl route. The URL is external test input, never a task-specific exception.
func TestKernelEgressPublicDownloadLive(t *testing.T) {
	target := os.Getenv("SYNON_KERNEL_EGRESS_PROBE_URL")
	if target == "" {
		t.Skip("set SYNON_KERNEL_EGRESS_PROBE_URL for live download verification")
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "https" || u.User != nil {
		t.Fatal("probe requires credential-free HTTPS")
	}
	proxy, err := startKernelEgressProxy(t.TempDir(), "public-download", []string{"*"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	transport := &http.Transport{
		Proxy:       http.ProxyURL(&url.URL{Scheme: "http", Host: "execution-proxy.invalid:80"}),
		DialContext: func(context.Context, string, string) (net.Conn, error) { return kernelEgressTestClient(t, proxy), nil },
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 25 * time.Second}
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Range", "bytes=0-1023")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("public download transport: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1024))
	if err != nil || (response.StatusCode != 200 && response.StatusCode != 206) || len(body) == 0 {
		t.Fatalf("public download status=%d bytes=%d err=%v", response.StatusCode, len(body), err)
	}
	t.Logf("TLS and redirected download verified: status=%d bytes=%d host=%s", response.StatusCode, len(body), response.Request.URL.Hostname())
}
