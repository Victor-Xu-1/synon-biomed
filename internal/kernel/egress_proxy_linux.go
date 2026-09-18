//go:build linux

package kernel

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"synon-go/internal/networkpolicy"
	"synon-go/internal/networktls"
)

const (
	kernelEgressHeaderLimit      = 32 << 10
	kernelEgressHandshakeTimeout = 30 * time.Second
	kernelEgressPortBase         = 20000
	kernelEgressPortSpan         = 30000
)

var kernelEgressReservedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("100::/64"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"), netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fec0::/10"),
}

type kernelEgressProxyOptions struct {
	lookup               func(context.Context, string) ([]net.IPAddr, error)
	dial                 func(context.Context, string) (net.Conn, error)
	proxy                func(*http.Request) (*url.URL, error)
	trustProxyResolution bool
}

type kernelEgressProxy struct {
	control      *net.UnixConn
	child        *os.File
	port         int
	allowed      []string
	denied       []string
	options      kernelEgressProxyOptions
	done         chan struct{}
	closeOnce    sync.Once
	childMu      sync.Mutex
	connectionMu sync.Mutex
	connections  map[net.Conn]struct{}
	closed       bool
}

func startKernelEgressProxy(workspaceDir, kernelID string, allowed, denied []string, upstreamProxy ...string) (*kernelEgressProxy, error) {
	options := kernelEgressProxyOptions{}
	if len(upstreamProxy) > 0 && strings.TrimSpace(upstreamProxy[0]) != "" {
		normalized, err := networktls.NormalizeProxyURL(upstreamProxy[0])
		if err != nil {
			return nil, err
		}
		parsed, err := url.Parse(normalized)
		if err != nil {
			return nil, errors.New("parse normalized kernel upstream proxy")
		}
		options.proxy = http.ProxyURL(parsed)
		options.trustProxyResolution = true
	}
	return startKernelEgressProxyWithOptions(workspaceDir, kernelID, allowed, denied, options)
}

func startKernelEgressProxyWithOptions(
	workspaceDir, kernelID string,
	allowed, denied []string,
	options kernelEgressProxyOptions,
) (*kernelEgressProxy, error) {
	if len(allowed) == 0 {
		return nil, nil
	}
	if options.lookup == nil {
		options.lookup = func(ctx context.Context, host string) ([]net.IPAddr, error) {
			return net.DefaultResolver.LookupIPAddr(ctx, host)
		}
	}
	if options.dial == nil {
		dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
		options.dial = func(ctx context.Context, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp", address)
		}
	}
	if options.proxy == nil {
		options.proxy = http.ProxyFromEnvironment
	}
	workspaceDir = filepath.Clean(strings.TrimSpace(workspaceDir))
	if !filepath.IsAbs(workspaceDir) || strings.TrimSpace(kernelID) == "" {
		return nil, errors.New("kernel egress workspace and kernel id are required")
	}
	descriptors, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("create kernel egress control socket: %w", err)
	}
	controlFile := os.NewFile(uintptr(descriptors[0]), "kernel-egress-control")
	childFile := os.NewFile(uintptr(descriptors[1]), "kernel-egress-child")
	connection, err := net.FileConn(controlFile)
	_ = controlFile.Close() // FileConn owns a duplicate, integrated with Go's poller.
	if err != nil {
		_ = childFile.Close()
		return nil, fmt.Errorf("open kernel egress control connection: %w", err)
	}
	controlConnection, ok := connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		_ = childFile.Close()
		return nil, errors.New("kernel egress control connection is not a Unix socket")
	}
	proxy := &kernelEgressProxy{
		control: controlConnection,
		child:   childFile,
		port:    kernelEgressLoopbackPort(workspaceDir, kernelID),
		allowed: append([]string(nil), allowed...), denied: append([]string(nil), denied...),
		options: options, done: make(chan struct{}),
	}
	go proxy.serve()
	return proxy, nil
}

func kernelEgressLoopbackPort(workspaceDir, kernelID string) int {
	digest := sha256.Sum256([]byte(filepath.Clean(workspaceDir) + "\x00" + strings.TrimSpace(kernelID)))
	return kernelEgressPortBase + int(binary.BigEndian.Uint16(digest[:2]))%kernelEgressPortSpan
}

func (p *kernelEgressProxy) serve() {
	defer close(p.done)
	for {
		payload := make([]byte, 1)
		control := make([]byte, unix.CmsgSpace(4))
		// UnixConn synchronizes descriptor lifetime with Close and wakes a
		// blocked receive even when the worker still owns the child endpoint.
		_, controlBytes, _, _, err := p.control.ReadMsgUnix(payload, control)
		if err != nil {
			return
		}
		messages, err := unix.ParseSocketControlMessage(control[:controlBytes])
		if err != nil || len(messages) != 1 {
			continue
		}
		rights, err := unix.ParseUnixRights(&messages[0])
		if err != nil || len(rights) != 1 {
			for _, descriptor := range rights {
				_ = unix.Close(descriptor)
			}
			continue
		}
		file := os.NewFile(uintptr(rights[0]), "kernel-egress-client")
		connection, err := net.FileConn(file)
		_ = file.Close()
		if err != nil {
			continue
		}
		go p.serveConnection(connection)
	}
}

func (p *kernelEgressProxy) serveConnection(client net.Conn) {
	defer client.Close()
	if !p.trackConnection(client) {
		return
	}
	defer p.untrackConnection(client)
	_ = client.SetDeadline(time.Now().Add(kernelEgressHandshakeTimeout))
	reader := bufio.NewReader(io.LimitReader(client, kernelEgressHeaderLimit+1))
	request, err := http.ReadRequest(reader)
	if err != nil {
		writeKernelEgressResponse(client, http.StatusBadRequest)
		return
	}
	if request.Body != nil {
		_ = request.Body.Close()
	}
	if request.Method != http.MethodConnect {
		writeKernelEgressResponse(client, http.StatusForbidden)
		return
	}
	host, port, err := net.SplitHostPort(request.Host)
	if err != nil || port != "443" {
		writeKernelEgressResponse(client, http.StatusForbidden)
		return
	}
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if !networkpolicy.AllowsHost(host, p.allowed, p.denied) {
		writeKernelEgressResponse(client, http.StatusForbidden)
		return
	}
	addresses := []net.IPAddr(nil)
	if !p.options.trustProxyResolution {
		lookupContext, cancelLookup := context.WithTimeout(context.Background(), 10*time.Second)
		addresses, err = p.options.lookup(lookupContext, host)
		cancelLookup()
		if err != nil || len(addresses) == 0 {
			writeKernelEgressResponse(client, http.StatusBadGateway)
			return
		}
		for _, candidate := range addresses {
			if kernelEgressPublicIP(candidate.IP) {
				continue
			}
			writeKernelEgressResponse(client, http.StatusForbidden)
			return
		}
	}
	upstream, err := p.dialTarget(host, port, addresses)
	if err != nil {
		writeKernelEgressResponse(client, http.StatusBadGateway)
		return
	}
	defer upstream.Close()
	if !p.trackConnection(upstream) {
		return
	}
	defer p.untrackConnection(upstream)
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	_ = client.SetDeadline(time.Time{})
	if buffered := reader.Buffered(); buffered > 0 {
		if _, err := io.CopyN(upstream, reader, int64(buffered)); err != nil {
			return
		}
	}
	done := make(chan struct{}, 1)
	go func() {
		_, _ = io.Copy(upstream, client)
		if tcp, ok := upstream.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		done <- struct{}{}
	}()
	_, _ = io.Copy(client, upstream)
	_ = client.Close()
	_ = upstream.Close()
	<-done
}

func (p *kernelEgressProxy) dialTarget(host, port string, addresses []net.IPAddr) (net.Conn, error) {
	proxyRequest := &http.Request{URL: &url.URL{Scheme: "https", Host: net.JoinHostPort(host, port)}}
	upstreamProxy, err := p.options.proxy(proxyRequest)
	if err != nil {
		return nil, err
	}
	if upstreamProxy != nil {
		return dialKernelUpstreamProxy(upstreamProxy, host, port)
	}
	if p.options.trustProxyResolution {
		return nil, errors.New("configured trusted upstream proxy is unavailable")
	}
	sorted := append([]net.IPAddr(nil), addresses...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].IP.To4() != nil && sorted[j].IP.To4() == nil
	})
	var dialErrors []error
	for _, candidate := range sorted {
		dialContext, cancelDial := context.WithTimeout(context.Background(), 5*time.Second)
		connection, dialErr := p.options.dial(dialContext, net.JoinHostPort(candidate.IP.String(), port))
		cancelDial()
		if dialErr == nil {
			return connection, nil
		}
		dialErrors = append(dialErrors, dialErr)
	}
	return nil, errors.Join(dialErrors...)
}

func dialKernelUpstreamProxy(proxyURL *url.URL, host, port string) (net.Conn, error) {
	if proxyURL == nil || proxyURL.Scheme != "http" || proxyURL.Hostname() == "" ||
		(proxyURL.Path != "" && proxyURL.Path != "/") || proxyURL.RawQuery != "" || proxyURL.Fragment != "" {
		return nil, errors.New("configured upstream proxy is not a supported HTTP CONNECT proxy")
	}
	proxyPort := proxyURL.Port()
	if proxyPort == "" {
		proxyPort = "80"
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	connection, err := dialer.Dial("tcp", net.JoinHostPort(proxyURL.Hostname(), proxyPort))
	if err != nil {
		return nil, errors.New("connect to configured upstream proxy failed")
	}
	target := net.JoinHostPort(host, port)
	headers := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n"
	if proxyURL.User != nil {
		password, _ := proxyURL.User.Password()
		credentials := base64.StdEncoding.EncodeToString([]byte(proxyURL.User.Username() + ":" + password))
		headers += "Proxy-Authorization: Basic " + credentials + "\r\n"
	}
	if _, err := io.WriteString(connection, headers+"\r\n"); err != nil {
		_ = connection.Close()
		return nil, errors.New("write configured upstream proxy request failed")
	}
	_ = connection.SetDeadline(time.Now().Add(15 * time.Second))
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodConnect})
	if err != nil || response.StatusCode != http.StatusOK {
		_ = connection.Close()
		return nil, errors.New("configured upstream proxy refused CONNECT")
	}
	if response.Body != nil {
		_ = response.Body.Close()
	}
	_ = connection.SetDeadline(time.Time{})
	return connection, nil
}

func writeKernelEgressResponse(writer io.Writer, status int) {
	text := http.StatusText(status)
	_, _ = fmt.Fprintf(writer, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", status, text)
}

func kernelEgressPublicIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	if address.IsPrivate() || address.IsLoopback() || address.IsUnspecified() ||
		address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() {
		return false
	}
	for _, prefix := range kernelEgressReservedNetworks {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func (p *kernelEgressProxy) Close() {
	if p == nil {
		return
	}
	p.closeOnce.Do(func() {
		p.connectionMu.Lock()
		p.closed = true
		connections := make([]net.Conn, 0, len(p.connections))
		for connection := range p.connections {
			connections = append(connections, connection)
		}
		p.connectionMu.Unlock()
		for _, connection := range connections {
			_ = connection.Close()
		}
		p.childMu.Lock()
		if p.child != nil {
			_ = p.child.Close()
			p.child = nil
		}
		p.childMu.Unlock()
		_ = p.control.Close()
		<-p.done
	})
}

// Both sides belong to the same proxy lifetime. Closing the session must also
// release stalled transfers; closing only the control socket leaks tunnels.
func (p *kernelEgressProxy) trackConnection(connection net.Conn) bool {
	p.connectionMu.Lock()
	defer p.connectionMu.Unlock()
	if p.closed {
		return false
	}
	if p.connections == nil {
		p.connections = make(map[net.Conn]struct{})
	}
	p.connections[connection] = struct{}{}
	return true
}

func (p *kernelEgressProxy) untrackConnection(connection net.Conn) {
	p.connectionMu.Lock()
	delete(p.connections, connection)
	p.connectionMu.Unlock()
}

func (p *kernelEgressProxy) takeChild() *os.File {
	if p == nil {
		return nil
	}
	p.childMu.Lock()
	defer p.childMu.Unlock()
	child := p.child
	p.child = nil
	return child
}
