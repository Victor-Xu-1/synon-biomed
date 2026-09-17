package kernel

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestProviderProxySOCKSAndHTTPConnectOnlyReachAdmittedDomain(t *testing.T) {
	rules := []ProviderEgressRule{{Host: "api.provider.test", Port: 443}}
	dial := func(_ context.Context, host string, port int, _ bool) (net.Conn, error) {
		if host != "api.provider.test" || port != 443 {
			t.Fatalf("dial target=%s:%d", host, port)
		}
		client, remote := net.Pipe()
		go func() {
			defer remote.Close()
			_, _ = io.Copy(remote, remote)
		}()
		return client, nil
	}
	for _, test := range []struct {
		name      string
		handshake []byte
		wantHead  int
	}{
		{
			name: "socks5",
			handshake: append(append([]byte{5, 1, 0, 5, 1, 0, 3, byte(len("api.provider.test"))},
				[]byte("api.provider.test")...), 0x01, 0xbb),
			wantHead: 12,
		},
		{
			name: "http-connect", handshake: []byte("CONNECT api.provider.test:443 HTTP/1.1\r\nHost: api.provider.test:443\r\n\r\n"),
			wantHead: len("HTTP/1.1 200 Connection Established\r\n\r\n"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { _ = serveProviderProxyStream(ctx, server, rules, dial) }()
			if _, err := client.Write(test.handshake); err != nil {
				t.Fatal(err)
			}
			head := make([]byte, test.wantHead)
			if _, err := io.ReadFull(client, head); err != nil {
				t.Fatal(err)
			}
			if _, err := client.Write([]byte("provider-echo")); err != nil {
				t.Fatal(err)
			}
			body := make([]byte, len("provider-echo"))
			if _, err := io.ReadFull(client, body); err != nil || string(body) != "provider-echo" {
				t.Fatalf("echo=%q err=%v", body, err)
			}
		})
	}
}

func TestProviderProxyRejectsIPLiteralSuffixConfusionAndPrivateResolution(t *testing.T) {
	rules, err := normalizeProviderEgressRules([]ProviderEgressRule{
		{Host: "api.provider.test", Port: 443}, {Suffix: ".w.provider.test", Port: 443},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"127.0.0.1", "api.provider.test.evil", "w.provider.test", "evilw.provider.test"} {
		if _, allowed := providerEgressRuleFor(target, 443, rules); allowed {
			t.Fatalf("target %q bypassed provider egress policy", target)
		}
	}
	if _, allowed := providerEgressRuleFor("node.w.provider.test", 443, rules); !allowed {
		t.Fatal("provider egress suffix semantics drifted")
	}
	if _, allowed := providerEgressRuleFor("api.provider.test", 80, rules); allowed {
		t.Fatal("provider egress exact/suffix or port semantics drifted")
	}
	for _, value := range []string{"127.0.0.1", "10.1.2.3", "169.254.1.2", "::1", "fc00::1", "0.0.0.0"} {
		address, err := net.ResolveIPAddr("ip", value)
		if err != nil {
			t.Fatal(err)
		}
		parsed, ok := netipAddrFromIP(address.IP)
		if !ok || publicProviderAddress(parsed) {
			t.Fatalf("address %q was not rejected", value)
		}
	}
}

func TestProviderProxyMuxTransportsIndependentBoundedStreams(t *testing.T) {
	host, child := net.Pipe()
	mux, err := startProviderProxyMux(host, []ProviderEgressRule{{Host: "api.provider.test", Port: 443}}, func(_ context.Context, _ string, _ int, _ bool) (net.Conn, error) {
		client, remote := net.Pipe()
		go func() {
			defer remote.Close()
			_, _ = io.Copy(remote, remote)
		}()
		return client, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer mux.Close()
	defer child.Close()

	writeProviderProxyTestFrame(t, child, providerProxyFrameOpen, 7, nil)
	handshake := append(append([]byte{5, 1, 0, 5, 1, 0, 3, byte(len("api.provider.test"))}, []byte("api.provider.test")...), 0x01, 0xbb)
	writeProviderProxyTestFrame(t, child, providerProxyFrameData, 7, handshake)
	kind, id, payload := readProviderProxyTestFrame(t, child)
	if kind != providerProxyFrameData || id != 7 || len(payload) != 2 || payload[0] != 5 || payload[1] != 0 {
		t.Fatalf("SOCKS method response kind=%d id=%d payload=%v", kind, id, payload)
	}
	_, _, payload = readProviderProxyTestFrame(t, child)
	if len(payload) != 10 || payload[1] != 0 {
		t.Fatalf("SOCKS connect response=%v", payload)
	}
	writeProviderProxyTestFrame(t, child, providerProxyFrameData, 7, []byte("mux-echo"))
	_, _, payload = readProviderProxyTestFrame(t, child)
	if string(payload) != "mux-echo" {
		t.Fatalf("mux echo=%q", payload)
	}
	writeProviderProxyTestFrame(t, child, providerProxyFrameClose, 7, nil)
	select {
	case <-mux.Done():
		t.Fatal("closing one stream closed the provider proxy mux")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestProviderProxyAllowsOnlyExplicitLoopbackHTTPManagedEndpoint(t *testing.T) {
	if _, err := normalizeProviderEgressRules([]ProviderEgressRule{{Host: "127.0.0.1", Port: 25001}}); err == nil {
		t.Fatal("loopback provider rule was accepted without explicit private authority")
	}
	rules, err := normalizeProviderEgressRules([]ProviderEgressRule{{Host: "127.0.0.1", Port: 25001, AllowPrivate: true}})
	if err != nil {
		t.Fatal(err)
	}
	dial := func(_ context.Context, host string, port int, allowPrivate bool) (net.Conn, error) {
		if host != "127.0.0.1" || port != 25001 || !allowPrivate {
			t.Fatalf("local endpoint dial=%s:%d allowPrivate=%t", host, port, allowPrivate)
		}
		client, remote := net.Pipe()
		go func() {
			defer remote.Close()
			reader := bufio.NewReader(remote)
			var request strings.Builder
			for {
				line, readErr := reader.ReadString('\n')
				request.WriteString(line)
				if readErr != nil || line == "\r\n" || line == "\n" {
					break
				}
			}
			if !strings.HasPrefix(request.String(), "GET /health?probe=1 HTTP/1.1\r\n") ||
				strings.Contains(request.String(), "Proxy-Connection") || !strings.Contains(request.String(), "Connection: close\r\n") {
				return
			}
			_, _ = io.WriteString(remote, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
		}()
		return client, nil
	}
	client, server := net.Pipe()
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		defer server.Close()
		_ = serveProviderProxyStream(ctx, server, rules, dial)
	}()
	request := "GET http://127.0.0.1:25001/health?probe=1 HTTP/1.1\r\nHost: 127.0.0.1:25001\r\nProxy-Connection: keep-alive\r\n\r\n"
	if _, err := io.WriteString(client, request); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(client)
	if err != nil || !strings.HasSuffix(string(response), "\r\n\r\nok") {
		t.Fatalf("local endpoint response=%q err=%v", response, err)
	}
}

func writeProviderProxyTestFrame(t *testing.T, connection net.Conn, kind byte, id uint32, payload []byte) {
	t.Helper()
	header := make([]byte, providerProxyHeaderBytes)
	header[0] = kind
	binary.BigEndian.PutUint32(header[1:5], id)
	binary.BigEndian.PutUint32(header[5:9], uint32(len(payload)))
	if _, err := connection.Write(append(header, payload...)); err != nil {
		t.Fatal(err)
	}
}

func readProviderProxyTestFrame(t *testing.T, connection net.Conn) (byte, uint32, []byte) {
	t.Helper()
	header := make([]byte, providerProxyHeaderBytes)
	if _, err := io.ReadFull(connection, header); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, binary.BigEndian.Uint32(header[5:9]))
	if _, err := io.ReadFull(connection, payload); err != nil {
		t.Fatal(err)
	}
	return header[0], binary.BigEndian.Uint32(header[1:5]), payload
}

func netipAddrFromIP(ip net.IP) (address netip.Addr, ok bool) {
	address, ok = netip.AddrFromSlice(ip)
	return address.Unmap(), ok
}
