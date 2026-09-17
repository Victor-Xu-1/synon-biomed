//go:build linux

package kernel

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestKernelEgressProxyPinsAllowedPublicDestinationAndTunnels(t *testing.T) {
	serverSide := make(chan net.Conn, 1)
	proxy, err := startKernelEgressProxyWithOptions(
		t.TempDir(), "kernel-test", []string{"*.example.com"}, nil,
		kernelEgressProxyOptions{
			proxy: func(*http.Request) (*url.URL, error) { return nil, nil },
			lookup: func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
			},
			dial: func(_ context.Context, address string) (net.Conn, error) {
				if address != "93.184.216.34:443" {
					return nil, fmt.Errorf("unexpected pinned address %q", address)
				}
				client, server := net.Pipe()
				serverSide <- server
				return client, nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	client := kernelEgressTestClient(t, proxy)
	defer client.Close()
	_, _ = io.WriteString(client, "CONNECT data.example.com:443 HTTP/1.1\r\nHost: data.example.com:443\r\n\r\n")
	response, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: http.MethodConnect})
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT response=%#v err=%v", response, err)
	}
	upstream := <-serverSide
	defer upstream.Close()
	go func() {
		buffer := make([]byte, 4)
		_, _ = io.ReadFull(upstream, buffer)
		_, _ = upstream.Write([]byte("pong"))
	}()
	_, _ = client.Write([]byte("ping"))
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(client, buffer); err != nil || string(buffer) != "pong" {
		t.Fatalf("tunnel response=%q err=%v", buffer, err)
	}
}

func TestKernelEgressProxyRejectsUnapprovedDeniedPrivateAndPlainHTTP(t *testing.T) {
	proxy, err := startKernelEgressProxyWithOptions(
		t.TempDir(), "kernel-policy", []string{"*.example.com", "hooks.slack.com"}, nil,
		kernelEgressProxyOptions{
			proxy: func(*http.Request) (*url.URL, error) { return nil, nil },
			lookup: func(_ context.Context, host string) ([]net.IPAddr, error) {
				if host == "private.example.com" {
					return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
				}
				return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
			},
			dial: func(context.Context, string) (net.Conn, error) {
				return nil, fmt.Errorf("rejected request reached the dialer")
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	for _, target := range []string{
		"other.example.org:443", "private.example.com:443", "api.example.com:80", "hooks.slack.com:443",
	} {
		t.Run(strings.ReplaceAll(target, ":", "_"), func(t *testing.T) {
			connection := kernelEgressTestClient(t, proxy)
			defer connection.Close()
			_, _ = io.WriteString(connection, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
			response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodConnect})
			if err != nil || response.StatusCode != http.StatusForbidden {
				t.Fatalf("target=%s response=%#v err=%v", target, response, err)
			}
		})
	}
}

func TestKernelEgressProxyUsesExplicitTrustedUpstreamProxy(t *testing.T) {
	proxy, err := startKernelEgressProxy(
		t.TempDir(), "kernel-upstream", []string{"example.com"}, nil, "http://Proxy.Example:8080/",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	request, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
	upstream, err := proxy.options.proxy(request)
	if err != nil || upstream == nil || upstream.String() != "http://proxy.example:8080" {
		t.Fatalf("kernel upstream proxy = %#v, %v", upstream, err)
	}
	if !proxy.options.trustProxyResolution {
		t.Fatal("explicit kernel upstream proxy did not own destination resolution")
	}
	if _, err := startKernelEgressProxy(
		t.TempDir(), "kernel-invalid-upstream", []string{"example.com"}, nil, "https://proxy.example/path",
	); err == nil {
		t.Fatal("unsupported kernel upstream proxy was accepted")
	}
}

func TestKernelEgressProxyLetsTrustedProxyResolvePublicHostname(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	targets := make(chan string, 1)
	upstreamErrors := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			upstreamErrors <- acceptErr
			return
		}
		defer connection.Close()
		request, readErr := http.ReadRequest(bufio.NewReader(connection))
		if readErr != nil {
			upstreamErrors <- readErr
			return
		}
		targets <- request.Host
		_, writeErr := io.WriteString(connection, "HTTP/1.1 200 Connection Established\r\n\r\n")
		upstreamErrors <- writeErr
		_, _ = io.Copy(io.Discard, connection)
	}()
	proxy, err := startKernelEgressProxy(
		t.TempDir(), "kernel-trusted-resolution", []string{"unresolved.example.com"}, nil,
		"http://"+listener.Addr().String(),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	client := kernelEgressTestClient(t, proxy)
	_, _ = io.WriteString(client, "CONNECT unresolved.example.com:443 HTTP/1.1\r\nHost: unresolved.example.com:443\r\n\r\n")
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	response, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: http.MethodConnect})
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT response=%#v err=%v", response, err)
	}
	if target := <-targets; target != "unresolved.example.com:443" {
		t.Fatalf("upstream target=%q", target)
	}
	if err := <-upstreamErrors; err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
}

func TestKernelEgressRuntimeInjectsOneProxyAndCABundle(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	assetRoot := filepath.Join(filepath.Dir(file), "..", "..", "assets", "optional")
	manager := &Manager{config: Config{AssetRoot: assetRoot}}
	_, _, environment, err := manager.wrapKernelEgressRuntime(
		"/usr/bin/python3", []string{"worker.py"}, []string{"PATH=/usr/bin", "SSL_CERT_FILE=/old.pem"},
		23456, "/etc/synon/company.pem",
	)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(environment, "\n")
	for _, required := range []string{
		"HTTPS_PROXY=http://127.0.0.1:23456",
		"SSL_CERT_FILE=/etc/synon/company.pem",
		"REQUESTS_CA_BUNDLE=/etc/synon/company.pem",
		"CURL_CA_BUNDLE=/etc/synon/company.pem",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("kernel egress environment missing %q: %s", required, joined)
		}
	}
	if strings.Contains(joined, "/old.pem") {
		t.Fatalf("stale CA bundle remained in kernel environment: %s", joined)
	}
}

func TestKernelEgressProxyLiveUpstreamChain(t *testing.T) {
	if os.Getenv("SYNON_RUN_KERNEL_EGRESS_LIVE") != "1" {
		t.Skip("set SYNON_RUN_KERNEL_EGRESS_LIVE=1 for the live upstream-proxy check")
	}
	proxy, err := startKernelEgressProxy(t.TempDir(), "kernel-live-chain", []string{"example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	connection := kernelEgressTestClient(t, proxy)
	defer connection.Close()
	_, _ = io.WriteString(connection, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	_ = connection.SetReadDeadline(time.Now().Add(20 * time.Second))
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodConnect})
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("live upstream CONNECT response=%#v err=%v", response, err)
	}
}

func TestKernelEgressForwarderPassesAcceptedSocketsToPolicy(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	forwarder := filepath.Join(filepath.Dir(file), "..", "..", "assets", "optional", "kernels", "kernel_egress_forwarder.py")
	serverSide := make(chan net.Conn, 1)
	proxy, err := startKernelEgressProxyWithOptions(
		t.TempDir(), "kernel-forwarder", []string{"example.com"}, nil,
		kernelEgressProxyOptions{
			proxy: func(*http.Request) (*url.URL, error) { return nil, nil },
			lookup: func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
			},
			dial: func(context.Context, string) (net.Conn, error) {
				client, server := net.Pipe()
				serverSide <- server
				return client, nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	child := proxy.takeChild()
	if child == nil {
		t.Fatal("kernel egress child descriptor is unavailable")
	}
	command := exec.Command(python, forwarder)
	command.ExtraFiles = []*os.File{child}
	command.Env = append(os.Environ(),
		"SYNON_KERNEL_EGRESS_CONTROL_FD=3",
		"SYNON_KERNEL_EGRESS_PORT="+strconv.Itoa(proxy.port),
		"PYTHONDONTWRITEBYTECODE=1",
	)
	if err := command.Start(); err != nil {
		_ = child.Close()
		t.Fatal(err)
	}
	_ = child.Close()
	defer func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	}()
	var connection net.Conn
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		connection, err = net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(proxy.port)), 100*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if connection == nil {
		t.Fatalf("forwarder did not start: %v", err)
	}
	defer connection.Close()
	_, _ = io.WriteString(connection, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodConnect})
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("forwarded CONNECT response=%#v err=%v", response, err)
	}
	_ = (<-serverSide).Close()
}

func kernelEgressTestClient(t *testing.T, proxy *kernelEgressProxy) net.Conn {
	t.Helper()
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.DialTCP("tcp", nil, listener.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	server, err := listener.AcceptTCP()
	if err != nil {
		_ = client.Close()
		t.Fatal(err)
	}
	file, err := server.File()
	_ = server.Close()
	if err != nil {
		_ = client.Close()
		t.Fatal(err)
	}
	rights := unix.UnixRights(int(file.Fd()))
	err = unix.Sendmsg(int(proxy.child.Fd()), []byte{'C'}, rights, nil, 0)
	_ = file.Close()
	if err != nil {
		_ = client.Close()
		t.Fatal(err)
	}
	return client
}
