package kernel

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	providerProxyFrameOpen  byte = 1
	providerProxyFrameData  byte = 2
	providerProxyFrameClose byte = 3

	providerProxyMaxFrameBytes = 64 << 10
	providerProxyMaxStreams    = 32
	providerProxyQueueFrames   = 8
	providerProxyHeaderBytes   = 9
	providerProxyMaxHTTPHeader = 32 << 10
)

// ProviderEgressRule is one exact TLS control-plane destination or one
// provider-owned DNS suffix. Provider kernels never receive a generic network
// grant: every outbound connection is parsed and admitted by these rules.
type ProviderEgressRule struct {
	Host         string
	Suffix       string
	Port         int
	AllowPrivate bool
}

type ProviderProxyDialFunc func(context.Context, string, int, bool) (net.Conn, error)

type providerProxyMux struct {
	connection net.Conn
	rules      []ProviderEgressRule
	dial       ProviderProxyDialFunc

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	writeMu sync.Mutex
	mu      sync.Mutex
	streams map[uint32]*providerProxyStream
}

func startProviderProxyMux(connection net.Conn, rules []ProviderEgressRule, dial ProviderProxyDialFunc) (*providerProxyMux, error) {
	if connection == nil {
		return nil, errors.New("provider proxy transport is unavailable")
	}
	normalized, err := normalizeProviderEgressRules(rules)
	if err != nil {
		_ = connection.Close()
		return nil, err
	}
	if dial == nil {
		dial = dialPublicProviderDestination
	}
	ctx, cancel := context.WithCancel(context.Background())
	mux := &providerProxyMux{
		connection: connection, rules: normalized, dial: dial,
		ctx: ctx, cancel: cancel, done: make(chan struct{}), streams: map[uint32]*providerProxyStream{},
	}
	go mux.readLoop()
	return mux, nil
}

func (m *providerProxyMux) Close() error {
	if m == nil {
		return nil
	}
	m.cancel()
	err := m.connection.Close()
	<-m.done
	return err
}

func (m *providerProxyMux) Done() <-chan struct{} {
	if m == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return m.done
}

func (m *providerProxyMux) readLoop() {
	defer close(m.done)
	defer m.cancel()
	defer m.closeStreams()
	header := make([]byte, providerProxyHeaderBytes)
	for {
		if _, err := io.ReadFull(m.connection, header); err != nil {
			return
		}
		kind := header[0]
		streamID := binary.BigEndian.Uint32(header[1:5])
		length := binary.BigEndian.Uint32(header[5:9])
		if streamID == 0 || length > providerProxyMaxFrameBytes ||
			(kind == providerProxyFrameOpen || kind == providerProxyFrameClose) && length != 0 {
			return
		}
		payload := make([]byte, int(length))
		if len(payload) > 0 {
			if _, err := io.ReadFull(m.connection, payload); err != nil {
				return
			}
		}
		switch kind {
		case providerProxyFrameOpen:
			m.openStream(streamID)
		case providerProxyFrameData:
			m.deliver(streamID, payload)
		case providerProxyFrameClose:
			m.remoteClose(streamID)
		default:
			return
		}
	}
}

func (m *providerProxyMux) openStream(id uint32) {
	m.mu.Lock()
	if _, exists := m.streams[id]; exists || len(m.streams) >= providerProxyMaxStreams {
		m.mu.Unlock()
		_ = m.writeFrame(providerProxyFrameClose, id, nil)
		return
	}
	stream := &providerProxyStream{
		mux: m, id: id, inbound: make(chan []byte, providerProxyQueueFrames), closed: make(chan struct{}),
	}
	m.streams[id] = stream
	m.mu.Unlock()
	go func() {
		ctx, cancel := context.WithCancel(m.ctx)
		defer cancel()
		_ = serveProviderProxyStream(ctx, stream, m.rules, m.dial)
		_ = stream.Close()
	}()
}

func (m *providerProxyMux) deliver(id uint32, payload []byte) {
	m.mu.Lock()
	stream := m.streams[id]
	m.mu.Unlock()
	if stream == nil {
		_ = m.writeFrame(providerProxyFrameClose, id, nil)
		return
	}
	select {
	case stream.inbound <- payload:
	case <-stream.closed:
	case <-m.ctx.Done():
	default:
		_ = stream.Close()
	}
}

func (m *providerProxyMux) remoteClose(id uint32) {
	m.mu.Lock()
	stream := m.streams[id]
	delete(m.streams, id)
	m.mu.Unlock()
	if stream != nil {
		stream.closeRemote()
	}
}

func (m *providerProxyMux) remove(id uint32, stream *providerProxyStream) {
	m.mu.Lock()
	if m.streams[id] == stream {
		delete(m.streams, id)
	}
	m.mu.Unlock()
}

func (m *providerProxyMux) closeStreams() {
	m.mu.Lock()
	streams := make([]*providerProxyStream, 0, len(m.streams))
	for _, stream := range m.streams {
		streams = append(streams, stream)
	}
	m.streams = map[uint32]*providerProxyStream{}
	m.mu.Unlock()
	for _, stream := range streams {
		stream.closeRemote()
	}
}

func (m *providerProxyMux) writeFrame(kind byte, id uint32, payload []byte) error {
	if len(payload) > providerProxyMaxFrameBytes {
		return errors.New("provider proxy frame exceeds the bounded protocol")
	}
	header := make([]byte, providerProxyHeaderBytes)
	header[0] = kind
	binary.BigEndian.PutUint32(header[1:5], id)
	binary.BigEndian.PutUint32(header[5:9], uint32(len(payload)))
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	if _, err := m.connection.Write(header); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err := m.connection.Write(payload)
		return err
	}
	return nil
}

type providerProxyStream struct {
	mux *providerProxyMux
	id  uint32

	inbound chan []byte
	closed  chan struct{}
	once    sync.Once
	readBuf []byte
}

func (s *providerProxyStream) Read(target []byte) (int, error) {
	for len(s.readBuf) == 0 {
		select {
		case payload, ok := <-s.inbound:
			if !ok {
				return 0, io.EOF
			}
			s.readBuf = payload
		case <-s.closed:
			return 0, io.EOF
		}
	}
	read := copy(target, s.readBuf)
	s.readBuf = s.readBuf[read:]
	return read, nil
}

func (s *providerProxyStream) Write(payload []byte) (int, error) {
	written := 0
	for len(payload) > 0 {
		chunk := payload
		if len(chunk) > providerProxyMaxFrameBytes {
			chunk = chunk[:providerProxyMaxFrameBytes]
		}
		select {
		case <-s.closed:
			return written, io.ErrClosedPipe
		default:
		}
		if err := s.mux.writeFrame(providerProxyFrameData, s.id, chunk); err != nil {
			return written, err
		}
		written += len(chunk)
		payload = payload[len(chunk):]
	}
	return written, nil
}

func (s *providerProxyStream) Close() error {
	s.once.Do(func() {
		close(s.closed)
		s.mux.remove(s.id, s)
		_ = s.mux.writeFrame(providerProxyFrameClose, s.id, nil)
	})
	return nil
}

func (s *providerProxyStream) closeRemote() {
	s.once.Do(func() {
		close(s.closed)
		close(s.inbound)
	})
}

func serveProviderProxyStream(
	ctx context.Context,
	stream io.ReadWriteCloser,
	rules []ProviderEgressRule,
	dial ProviderProxyDialFunc,
) error {
	if stream == nil {
		return errors.New("provider proxy stream is unavailable")
	}
	if dial == nil {
		dial = dialPublicProviderDestination
	}
	reader := bufio.NewReaderSize(stream, providerProxyMaxHTTPHeader)
	first, err := reader.ReadByte()
	if err != nil {
		return err
	}
	handshake := providerProxyHandshake{SOCKS: first == 5}
	if first == 5 {
		handshake.Host, handshake.Port, err = providerSOCKS5Handshake(reader, stream)
		handshake.Tunnel = true
	} else {
		handshake, err = providerHTTPProxyHandshake(first, reader)
	}
	if err != nil {
		return err
	}
	rule, allowed := providerEgressRuleFor(handshake.Host, handshake.Port, rules)
	if !allowed {
		return errors.New("provider proxy destination is not allowlisted")
	}
	dialContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	remote, err := dial(dialContext, handshake.Host, handshake.Port, rule.AllowPrivate)
	cancel()
	if err != nil {
		return err
	}
	defer remote.Close()
	if handshake.SOCKS {
		if _, err := stream.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
			return err
		}
	} else if handshake.Tunnel {
		if _, err := io.WriteString(stream, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return err
		}
	} else if len(handshake.Initial) > 0 {
		if _, err := remote.Write(handshake.Initial); err != nil {
			return err
		}
	}
	errCh := make(chan error, 2)
	go func() {
		_, copyErr := io.Copy(remote, reader)
		errCh <- copyErr
	}()
	go func() {
		_, copyErr := io.Copy(stream, remote)
		errCh <- copyErr
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case copyErr := <-errCh:
		return copyErr
	}
}

func providerSOCKS5Handshake(reader *bufio.Reader, writer io.Writer) (string, int, error) {
	methods, err := reader.ReadByte()
	if err != nil || methods == 0 || methods > 16 {
		return "", 0, errors.New("provider SOCKS5 greeting is invalid")
	}
	methodList := make([]byte, int(methods))
	if _, err := io.ReadFull(reader, methodList); err != nil {
		return "", 0, err
	}
	noAuth := false
	for _, method := range methodList {
		noAuth = noAuth || method == 0
	}
	if !noAuth {
		_, _ = writer.Write([]byte{5, 0xff})
		return "", 0, errors.New("provider SOCKS5 requires unsupported authentication")
	}
	if _, err := writer.Write([]byte{5, 0}); err != nil {
		return "", 0, err
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil {
		return "", 0, err
	}
	if header[0] != 5 || header[1] != 1 || header[2] != 0 || header[3] != 3 {
		return "", 0, errors.New("provider SOCKS5 request must be a domain CONNECT")
	}
	length, err := reader.ReadByte()
	if err != nil || length == 0 {
		return "", 0, errors.New("provider SOCKS5 domain is invalid")
	}
	domain := make([]byte, int(length))
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(reader, domain); err != nil {
		return "", 0, err
	}
	if _, err := io.ReadFull(reader, portBytes); err != nil {
		return "", 0, err
	}
	host, err := normalizeProviderDomain(string(domain))
	if err != nil {
		return "", 0, err
	}
	return host, int(binary.BigEndian.Uint16(portBytes)), nil
}

type providerProxyHandshake struct {
	Host    string
	Port    int
	Tunnel  bool
	SOCKS   bool
	Initial []byte
}

func providerHTTPProxyHandshake(first byte, reader *bufio.Reader) (providerProxyHandshake, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return providerProxyHandshake{}, err
	}
	line = string([]byte{first}) + line
	if len(line) > 4096 {
		return providerProxyHandshake{}, errors.New("provider HTTP proxy request line is too large")
	}
	parts := strings.Fields(strings.TrimSpace(line))
	if len(parts) != 3 || !strings.HasPrefix(parts[2], "HTTP/1.") {
		return providerProxyHandshake{}, errors.New("provider HTTP proxy request line is invalid")
	}
	headerBytes := len(line)
	headers := make([]string, 0, 16)
	for {
		header, readErr := reader.ReadString('\n')
		headerBytes += len(header)
		if headerBytes > providerProxyMaxHTTPHeader {
			return providerProxyHandshake{}, errors.New("provider HTTP proxy headers are too large")
		}
		if readErr != nil {
			return providerProxyHandshake{}, readErr
		}
		if header == "\r\n" || header == "\n" {
			break
		}
		if strings.HasPrefix(header, " ") || strings.HasPrefix(header, "\t") || strings.ContainsAny(header, "\x00") {
			return providerProxyHandshake{}, errors.New("provider HTTP proxy header is invalid")
		}
		headers = append(headers, strings.TrimRight(header, "\r\n"))
	}
	if parts[0] == "CONNECT" {
		host, rawPort, splitErr := net.SplitHostPort(parts[1])
		if splitErr != nil {
			return providerProxyHandshake{}, errors.New("provider HTTP CONNECT authority is invalid")
		}
		host, err = normalizeProviderTargetHost(host)
		if err != nil {
			return providerProxyHandshake{}, err
		}
		port, convertErr := strconv.Atoi(rawPort)
		if convertErr != nil || port < 1 || port > 65535 {
			return providerProxyHandshake{}, errors.New("provider HTTP CONNECT port is invalid")
		}
		return providerProxyHandshake{Host: host, Port: port, Tunnel: true}, nil
	}
	method := parts[0]
	switch method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
	default:
		return providerProxyHandshake{}, errors.New("provider HTTP proxy method is not allowed")
	}
	target, parseErr := url.Parse(parts[1])
	if parseErr != nil || target.Scheme != "http" || target.User != nil || target.Host == "" {
		return providerProxyHandshake{}, errors.New("provider HTTP proxy accepts absolute HTTP URLs only")
	}
	host, err := normalizeProviderTargetHost(target.Hostname())
	if err != nil {
		return providerProxyHandshake{}, err
	}
	port := 80
	if target.Port() != "" {
		port, err = strconv.Atoi(target.Port())
		if err != nil || port < 1 || port > 65535 {
			return providerProxyHandshake{}, errors.New("provider HTTP proxy port is invalid")
		}
	}
	path := target.EscapedPath()
	if path == "" {
		path = "/"
	}
	if target.RawQuery != "" {
		path += "?" + target.RawQuery
	}
	var rewritten strings.Builder
	_, _ = fmt.Fprintf(&rewritten, "%s %s %s\r\n", method, path, parts[2])
	hasHost := false
	for _, header := range headers {
		name, _, found := strings.Cut(header, ":")
		if !found {
			return providerProxyHandshake{}, errors.New("provider HTTP proxy header is invalid")
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "proxy-authorization", "proxy-connection", "connection":
			continue
		case "host":
			hasHost = true
		}
		rewritten.WriteString(header)
		rewritten.WriteString("\r\n")
	}
	if !hasHost {
		rewritten.WriteString("Host: ")
		rewritten.WriteString(target.Host)
		rewritten.WriteString("\r\n")
	}
	rewritten.WriteString("Connection: close\r\n\r\n")
	return providerProxyHandshake{Host: host, Port: port, Initial: []byte(rewritten.String())}, nil
}

func normalizeProviderEgressRules(rules []ProviderEgressRule) ([]ProviderEgressRule, error) {
	if len(rules) == 0 || len(rules) > 64 {
		return nil, errors.New("provider proxy requires a bounded non-empty egress policy")
	}
	result := make([]ProviderEgressRule, 0, len(rules))
	seen := map[string]bool{}
	for _, rule := range rules {
		if rule.Port < 1 || rule.Port > 65535 || (rule.Host == "") == (rule.Suffix == "") {
			return nil, errors.New("provider proxy egress rule is invalid")
		}
		if rule.Host != "" {
			host, err := normalizeProviderTargetHost(rule.Host)
			if err != nil {
				return nil, err
			}
			if address, addressErr := netip.ParseAddr(host); addressErr == nil && (!rule.AllowPrivate || !address.IsLoopback()) {
				return nil, errors.New("provider proxy IP rules are allowed only for explicit loopback endpoints")
			}
			rule.Host = host
		} else {
			suffix := strings.ToLower(strings.TrimSpace(rule.Suffix))
			if !strings.HasPrefix(suffix, ".") {
				return nil, errors.New("provider proxy suffix must begin with a dot")
			}
			if _, err := normalizeProviderDomain("probe" + suffix); err != nil {
				return nil, err
			}
			rule.Suffix = suffix
		}
		key := fmt.Sprintf("%s\x00%s\x00%d", rule.Host, rule.Suffix, rule.Port)
		if !seen[key] {
			seen[key] = true
			result = append(result, rule)
		}
	}
	return result, nil
}

func normalizeProviderDomain(value string) (string, error) {
	value = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
	if value == "" || len(value) > 253 || net.ParseIP(value) != nil || strings.ContainsAny(value, "\x00\r\n:/@[]") {
		return "", errors.New("provider proxy requires an ASCII DNS hostname")
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("provider proxy hostname is invalid")
		}
		for index := 0; index < len(label); index++ {
			character := label[index]
			if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' {
				continue
			}
			return "", errors.New("provider proxy hostname is invalid")
		}
	}
	return value, nil
}

func normalizeProviderTargetHost(value string) (string, error) {
	value = strings.Trim(strings.TrimSpace(value), "[]")
	if address, err := netip.ParseAddr(value); err == nil {
		return address.Unmap().String(), nil
	}
	return normalizeProviderDomain(value)
}

func providerEgressRuleFor(host string, port int, rules []ProviderEgressRule) (ProviderEgressRule, bool) {
	host, err := normalizeProviderTargetHost(host)
	if err != nil {
		return ProviderEgressRule{}, false
	}
	for _, rule := range rules {
		if rule.Port != port {
			continue
		}
		if rule.Host == host || rule.Suffix != "" && len(host) > len(rule.Suffix) && strings.HasSuffix(host, rule.Suffix) {
			return rule, true
		}
	}
	return ProviderEgressRule{}, false
}

func dialPublicProviderDestination(ctx context.Context, host string, port int, allowPrivate bool) (net.Conn, error) {
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("provider proxy destination could not be resolved")
	}
	dialer := net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	var joined error
	for _, address := range addresses {
		if !publicProviderAddress(address) && !(allowPrivate && address.IsLoopback()) {
			joined = errors.Join(joined, errors.New("provider proxy rejected a non-public resolved address"))
			continue
		}
		connection, dialErr := dialer.DialContext(ctx, "tcp", net.JoinHostPort(address.String(), strconv.Itoa(port)))
		if dialErr == nil {
			return connection, nil
		}
		joined = errors.Join(joined, dialErr)
	}
	if joined == nil {
		joined = errors.New("provider proxy found no admissible address")
	}
	return nil, joined
}

func publicProviderAddress(address netip.Addr) bool {
	return address.IsValid() && address.IsGlobalUnicast() && !address.IsPrivate() &&
		!address.IsLoopback() && !address.IsLinkLocalUnicast() && !address.IsLinkLocalMulticast() &&
		!address.IsMulticast() && !address.IsUnspecified()
}
