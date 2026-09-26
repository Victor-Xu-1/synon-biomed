package securefetch

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"synon-go/internal/httpreliability"
)

type ErrorCode string

const (
	CodeInvalidRequest   ErrorCode = "secure_fetch_invalid_request"
	CodeInvalidPolicy    ErrorCode = "secure_fetch_invalid_policy"
	CodeDestination      ErrorCode = "secure_fetch_destination_denied"
	CodeNonPublic        ErrorCode = "secure_fetch_destination_non_public"
	CodeRedirect         ErrorCode = "secure_fetch_redirect_denied"
	CodeTransport        ErrorCode = "secure_fetch_transport_failed"
	CodeStatus           ErrorCode = "secure_fetch_response_status"
	CodeContentType      ErrorCode = "secure_fetch_content_type_denied"
	CodeContentRange     ErrorCode = "secure_fetch_content_range_invalid"
	CodeResponseTooLarge ErrorCode = "secure_fetch_response_too_large"
)

type FetchError struct {
	Code       ErrorCode
	statusCode int
	retryAfter time.Duration
	cause      error
}

func (e *FetchError) Error() string { return string(e.Code) }

func (e *FetchError) Unwrap() error { return e.cause }

func IsCode(err error, code ErrorCode) bool {
	var target *FetchError
	return errors.As(err, &target) && target.Code == code
}

func HTTPStatus(err error) (int, bool) {
	var target *FetchError
	if !errors.As(err, &target) || target.Code != CodeStatus || target.statusCode == 0 {
		return 0, false
	}
	return target.statusCode, true
}

func HTTPRetryAfter(err error) time.Duration {
	var target *FetchError
	if errors.As(err, &target) {
		return target.retryAfter
	}
	return 0
}

type AddressResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type Options struct {
	HTTPClient                   *http.Client
	Resolver                     AddressResolver
	TestOnlyAllowCustomTransport bool
	// ProxyURL is an explicit operator-selected HTTP(S) proxy. Environment
	// variables are never read here; the product configuration layer remains
	// the single authority for selecting a network route.
	ProxyURL string
}

type Policy struct {
	AllowedHosts       []string
	AllowedPorts       []string
	AcceptedMediaTypes []string
	// AllowPublicRedirects permits an already-authorized HTTPS source to hand
	// off to another public HTTPS host. The initial URL must still match
	// AllowedHosts, every redirect remains bounded by MaxRedirects and
	// AllowedPorts, and the redirect host must resolve exclusively to public
	// addresses before a proxied or direct request is allowed.
	AllowPublicRedirects bool
	// AllowMissingContentType is intentionally opt-in. A caller enabling it
	// must validate streamed bytes before publishing the response.
	AllowMissingContentType bool
	MaxRedirects            int
	MaxBytes                int64
	Timeout                 time.Duration
	UserAgent               string
	IdentityEncoding        bool
	// LongLivedTransfer removes the http.Client wall-clock timeout from the
	// response body. TransferIdleTimeout still detects a connection that has
	// stopped producing bytes, while the caller context remains authoritative
	// for cancellation.
	LongLivedTransfer   bool
	TransferIdleTimeout time.Duration
	// PrefixBytes requests an exact prefix for byte-identity verification. It
	// is mutually exclusive with the continuation RangeStart/IfRange pair.
	PrefixBytes int64
	// RangeStart and IfRange are a deliberately narrow resume contract. They
	// cannot express arbitrary headers and are accepted only as a pair with a
	// strong entity tag or a valid HTTP date.
	RangeStart int64
	IfRange    string
}

type Response struct {
	Body        io.ReadCloser
	StatusCode  int
	ContentType string
	// ReportedContentType preserves validated MIME parameters such as charset.
	// ContentType remains the normalized media type for existing consumers.
	ReportedContentType string
	ContentLength       int64
	FinalURL            *url.URL
	ETag                string
	LastModified        string
	ContentRangeStart   int64
	ContentRangeEnd     int64
	ContentRangeTotal   int64
}

type Client struct {
	transport *http.Transport
	resolver  AddressResolver
	configErr error
}

func New(options Options) *Client {
	var (
		transport *http.Transport
		configErr error
	)
	if options.HTTPClient != nil && !options.TestOnlyAllowCustomTransport {
		configErr = fetchError(CodeInvalidPolicy)
	}
	if configErr == nil && options.HTTPClient != nil {
		custom, ok := options.HTTPClient.Transport.(*http.Transport)
		if !ok || custom == nil {
			configErr = fetchError(CodeInvalidPolicy)
		} else {
			transport = custom.Clone()
			if transport.DialTLS != nil || transport.DialTLSContext != nil {
				configErr = fetchError(CodeInvalidPolicy)
			}
		}
	}
	if transport == nil {
		transport = hardenedTransport()
	}
	originalDial := transport.DialContext
	if !options.TestOnlyAllowCustomTransport {
		originalDial = nil
	}
	proxyURL, proxyErr := explicitProxyURL(options.ProxyURL)
	if proxyErr != nil {
		configErr = fetchError(CodeInvalidPolicy)
	}
	if proxyURL == nil {
		transport.Proxy = nil
		transport.DialContext = pinnedDialer(originalDial, options.Resolver)
	} else {
		transport.Proxy = http.ProxyURL(proxyURL)
		if originalDial == nil {
			originalDial = (&net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}).DialContext
		}
		transport.DialContext = originalDial
	}
	return &Client{transport: transport, resolver: options.Resolver, configErr: configErr}
}

func hardenedTransport() *http.Transport {
	return &http.Transport{
		Proxy:               nil,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		// The request attempt context owns the configurable header budget.
		ResponseHeaderTimeout: 0,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
}

func (c *Client) Fetch(ctx context.Context, rawURL string, policy Policy) (*Response, error) {
	if c.configErr != nil {
		return nil, c.configErr
	}
	compiled, err := compilePolicy(policy)
	if err != nil {
		return nil, err
	}
	target, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !compiled.allowsURL(target) {
		return nil, fetchError(CodeInvalidRequest)
	}
	clientTimeout := compiled.timeout
	attempt := httpreliability.Begin(ctx, compiled.timeout)
	handedOff := false
	defer func() {
		if !handedOff {
			attempt.Close()
		}
	}()
	if compiled.longLivedTransfer {
		clientTimeout = 0
	}
	client := http.Client{Transport: c.transport, Timeout: clientTimeout}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) > compiled.maxRedirects || request == nil || !compiled.allowsRedirectURL(request.URL) {
			return fetchError(CodeRedirect)
		}
		if !compiled.allowsHost(request.URL.Hostname()) {
			if _, err := resolvePublicAddresses(request.Context(), c.resolver, request.URL.Hostname()); err != nil {
				return fetchErrorWithCause(CodeRedirect, err)
			}
		}
		return nil
	}
	request, err := http.NewRequestWithContext(attempt.Context, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fetchError(CodeInvalidRequest)
	}
	request.Header.Set("Accept", strings.Join(compiled.mediaTypes, ", "))
	if compiled.userAgent != "" {
		request.Header.Set("User-Agent", compiled.userAgent)
	}
	if compiled.identityEncoding {
		request.Header.Set("Accept-Encoding", "identity")
	}
	if compiled.prefixBytes > 0 {
		request.Header.Set("Range", fmt.Sprintf("bytes=0-%d", compiled.prefixBytes-1))
	} else if compiled.rangeStart > 0 {
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-", compiled.rangeStart))
		request.Header.Set("If-Range", compiled.ifRange)
	}
	response, err := client.Do(request)
	if err != nil {
		err = attempt.Error(err)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		var fetchErr *FetchError
		if errors.As(err, &fetchErr) {
			return nil, fetchErr
		}
		return nil, fetchErrorWithCause(CodeTransport, err)
	}
	idleTimeout := compiled.timeout
	if compiled.longLivedTransfer {
		idleTimeout = compiled.transferIdleTimeout
	}
	response.Body, err = attempt.Body(response.Body, idleTimeout)
	if err != nil {
		return nil, fetchErrorWithCause(CodeTransport, err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		response.Body.Close()
		return nil, &FetchError{Code: CodeStatus, statusCode: response.StatusCode,
			retryAfter: httpreliability.RetryAfter(response.Header.Get("Retry-After"), time.Now())}
	}
	contentRangeStart, contentRangeEnd, contentRangeTotal := int64(-1), int64(-1), int64(-1)
	bodyLimit := compiled.maxBytes
	if response.StatusCode == http.StatusPartialContent {
		var ok bool
		contentRangeStart, contentRangeEnd, contentRangeTotal, ok = parseContentRange(response.Header.Get("Content-Range"))
		if !ok || contentRangeStart != compiled.rangeStart || contentRangeTotal > compiled.maxBytes ||
			(compiled.prefixBytes > 0 && contentRangeEnd != compiled.prefixBytes-1) {
			response.Body.Close()
			return nil, fetchError(CodeContentRange)
		}
		bodyLimit = contentRangeEnd - contentRangeStart + 1
		if response.ContentLength >= 0 && response.ContentLength != bodyLimit {
			response.Body.Close()
			return nil, fetchError(CodeContentRange)
		}
	} else if (compiled.rangeStart > 0 || compiled.prefixBytes > 0) && response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fetchError(CodeContentRange)
	}
	if compiled.prefixBytes > 0 && response.StatusCode == http.StatusOK {
		bodyLimit = compiled.prefixBytes
	}
	rawContentType := strings.TrimSpace(response.Header.Get("Content-Type"))
	mediaType, mediaParameters, err := mime.ParseMediaType(rawContentType)
	if rawContentType == "" && compiled.allowMissingContentType {
		mediaType, err = "", nil
	}
	if err != nil || (mediaType != "" && !compiled.mediaTypeAllowed(mediaType)) {
		response.Body.Close()
		return nil, fetchError(CodeContentType)
	}
	lengthLimit := bodyLimit
	if compiled.prefixBytes > 0 && response.StatusCode == http.StatusOK {
		lengthLimit = compiled.maxBytes
	}
	if response.ContentLength > lengthLimit {
		response.Body.Close()
		return nil, fetchError(CodeResponseTooLarge)
	}
	etag := validStrongETag(strings.TrimSpace(response.Header.Get("ETag")))
	lastModified := validHTTPDate(strings.TrimSpace(response.Header.Get("Last-Modified")))
	if response.StatusCode == http.StatusPartialContent && compiled.ifRange != "" {
		if strings.HasPrefix(compiled.ifRange, `"`) && etag != compiled.ifRange {
			response.Body.Close()
			return nil, fetchError(CodeContentRange)
		}
		if !strings.HasPrefix(compiled.ifRange, `"`) && lastModified != compiled.ifRange {
			response.Body.Close()
			return nil, fetchError(CodeContentRange)
		}
	}
	body := io.ReadCloser(response.Body)
	reader := io.Reader(body)
	if compiled.prefixBytes > 0 && response.StatusCode == http.StatusOK {
		// A server may ignore Range. Deliberately reading an exact prefix is
		// not a claim to have acquired its whole advertised representation.
		reader = io.LimitReader(body, compiled.prefixBytes)
	}
	handedOff = true
	return &Response{
		Body:                &boundedReadCloser{reader: reader, closer: body, remaining: bodyLimit},
		StatusCode:          response.StatusCode,
		ContentType:         mediaType,
		ReportedContentType: mime.FormatMediaType(mediaType, mediaParameters),
		ContentLength:       response.ContentLength,
		FinalURL:            response.Request.URL,
		ETag:                etag,
		LastModified:        lastModified,
		ContentRangeStart:   contentRangeStart,
		ContentRangeEnd:     contentRangeEnd,
		ContentRangeTotal:   contentRangeTotal,
	}, nil
}

type compiledPolicy struct {
	hosts                   map[string]struct{}
	ports                   map[string]struct{}
	mediaTypes              []string
	mediaTypeSet            map[string]struct{}
	allowPublicRedirects    bool
	allowMissingContentType bool
	maxRedirects            int
	maxBytes                int64
	timeout                 time.Duration
	userAgent               string
	identityEncoding        bool
	longLivedTransfer       bool
	transferIdleTimeout     time.Duration
	prefixBytes             int64
	rangeStart              int64
	ifRange                 string
}

func compilePolicy(policy Policy) (compiledPolicy, error) {
	if policy.MaxBytes <= 0 || policy.Timeout < 0 || (!policy.LongLivedTransfer && policy.Timeout == 0) || policy.MaxRedirects < 0 || len(policy.AllowedHosts) == 0 || len(policy.AcceptedMediaTypes) == 0 {
		return compiledPolicy{}, fetchError(CodeInvalidPolicy)
	}
	if policy.LongLivedTransfer {
		if policy.TransferIdleTimeout <= 0 {
			return compiledPolicy{}, fetchError(CodeInvalidPolicy)
		}
	} else if policy.TransferIdleTimeout != 0 {
		return compiledPolicy{}, fetchError(CodeInvalidPolicy)
	}
	if policy.PrefixBytes < 0 || policy.PrefixBytes > policy.MaxBytes ||
		(policy.PrefixBytes > 0 && (policy.RangeStart != 0 || strings.TrimSpace(policy.IfRange) != "")) {
		return compiledPolicy{}, fetchError(CodeInvalidPolicy)
	}
	if policy.RangeStart < 0 || policy.RangeStart >= policy.MaxBytes {
		return compiledPolicy{}, fetchError(CodeInvalidPolicy)
	}
	ifRange := strings.TrimSpace(policy.IfRange)
	if policy.RangeStart == 0 && ifRange != "" {
		return compiledPolicy{}, fetchError(CodeInvalidPolicy)
	}
	if policy.RangeStart > 0 && validStrongETag(ifRange) == "" && validHTTPDate(ifRange) == "" {
		return compiledPolicy{}, fetchError(CodeInvalidPolicy)
	}
	hosts := make(map[string]struct{}, len(policy.AllowedHosts))
	for _, raw := range policy.AllowedHosts {
		host := canonicalHost(raw)
		if host == "" || strings.ContainsAny(host, "/:@") {
			return compiledPolicy{}, fetchError(CodeInvalidPolicy)
		}
		for _, character := range host {
			if character > 0x7f {
				return compiledPolicy{}, fetchError(CodeInvalidPolicy)
			}
		}
		hosts[host] = struct{}{}
	}
	ports := make(map[string]struct{}, len(policy.AllowedPorts))
	for _, raw := range policy.AllowedPorts {
		port := strings.TrimSpace(raw)
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 || strconv.Itoa(value) != port {
			return compiledPolicy{}, fetchError(CodeInvalidPolicy)
		}
		ports[port] = struct{}{}
	}
	if len(ports) == 0 {
		ports["443"] = struct{}{}
	}
	mediaTypes := make([]string, 0, len(policy.AcceptedMediaTypes))
	mediaTypeSet := make(map[string]struct{}, len(policy.AcceptedMediaTypes))
	for _, raw := range policy.AcceptedMediaTypes {
		mediaType, _, err := mime.ParseMediaType(raw)
		mediaType = strings.ToLower(strings.TrimSpace(mediaType))
		if err != nil || mediaType == "" || strings.Contains(mediaType, "*") {
			return compiledPolicy{}, fetchError(CodeInvalidPolicy)
		}
		if _, duplicate := mediaTypeSet[mediaType]; !duplicate {
			mediaTypeSet[mediaType] = struct{}{}
			mediaTypes = append(mediaTypes, mediaType)
		}
	}
	sort.Strings(mediaTypes)
	return compiledPolicy{
		hosts: hosts, ports: ports, mediaTypes: mediaTypes, mediaTypeSet: mediaTypeSet,
		allowPublicRedirects:    policy.AllowPublicRedirects,
		allowMissingContentType: policy.AllowMissingContentType,
		maxRedirects:            policy.MaxRedirects, maxBytes: policy.MaxBytes, timeout: policy.Timeout,
		userAgent: strings.TrimSpace(policy.UserAgent), identityEncoding: policy.IdentityEncoding,
		longLivedTransfer: policy.LongLivedTransfer, transferIdleTimeout: policy.TransferIdleTimeout,
		prefixBytes: policy.PrefixBytes, rangeStart: policy.RangeStart, ifRange: ifRange,
	}, nil
}

func explicitProxyURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("invalid proxy URL")
	}
	return parsed, nil
}

// ResumeValidator returns the only validator value that Fetch will permit in
// an If-Range request. Strong entity tags are preferred over HTTP dates.
func ResumeValidator(response *Response) string {
	if response == nil {
		return ""
	}
	if value := validStrongETag(strings.TrimSpace(response.ETag)); value != "" {
		return value
	}
	return validHTTPDate(strings.TrimSpace(response.LastModified))
}

func parseContentRange(raw string) (start, end, total int64, ok bool) {
	value := strings.TrimSpace(raw)
	if !strings.HasPrefix(value, "bytes ") {
		return 0, 0, 0, false
	}
	value = strings.TrimSpace(strings.TrimPrefix(value, "bytes "))
	slash := strings.IndexByte(value, '/')
	if slash <= 0 || slash == len(value)-1 || strings.Contains(value[slash+1:], "/") {
		return 0, 0, 0, false
	}
	span, totalRaw := value[:slash], value[slash+1:]
	dash := strings.IndexByte(span, '-')
	if dash <= 0 || dash == len(span)-1 || strings.Contains(span[dash+1:], "-") || totalRaw == "*" {
		return 0, 0, 0, false
	}
	start, startErr := strconv.ParseInt(span[:dash], 10, 64)
	end, endErr := strconv.ParseInt(span[dash+1:], 10, 64)
	total, totalErr := strconv.ParseInt(totalRaw, 10, 64)
	if startErr != nil || endErr != nil || totalErr != nil || start < 0 || end < start || total <= end {
		return 0, 0, 0, false
	}
	return start, end, total, true
}

func validStrongETag(value string) string {
	if len(value) < 2 || len(value) > 512 || value[0] != '"' || value[len(value)-1] != '"' || strings.HasPrefix(value, "W/") {
		return ""
	}
	for _, character := range value[1 : len(value)-1] {
		if character == '"' || character < 0x21 || character > 0x7e {
			return ""
		}
	}
	return value
}

func validHTTPDate(value string) string {
	if value == "" || len(value) > 128 || strings.ContainsAny(value, "\r\n") {
		return ""
	}
	if _, err := http.ParseTime(value); err != nil {
		return ""
	}
	return value
}

func (p compiledPolicy) allowsURL(target *url.URL) bool {
	if !p.allowsURLShape(target) || !p.allowsHost(target.Hostname()) {
		return false
	}
	return true
}

func (p compiledPolicy) allowsRedirectURL(target *url.URL) bool {
	return p.allowsURL(target) || (p.allowPublicRedirects && p.allowsURLShape(target))
}

func (p compiledPolicy) allowsHost(host string) bool {
	_, allowed := p.hosts[canonicalHost(host)]
	return allowed
}

func (p compiledPolicy) allowsURLShape(target *url.URL) bool {
	if target == nil || target.Scheme != "https" || target.Hostname() == "" || target.User != nil || target.Fragment != "" {
		return false
	}
	port := target.Port()
	if port == "" {
		port = "443"
	}
	_, allowed := p.ports[port]
	return allowed
}

func (p compiledPolicy) mediaTypeAllowed(mediaType string) bool {
	_, allowed := p.mediaTypeSet[strings.ToLower(strings.TrimSpace(mediaType))]
	return allowed
}

func canonicalHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func pinnedDialer(original func(context.Context, string, string) (net.Conn, error), resolver AddressResolver) func(context.Context, string, string) (net.Conn, error) {
	if original == nil {
		original = (&net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	return func(ctx context.Context, network string, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fetchError(CodeDestination)
		}
		addresses, err := resolvePublicAddresses(ctx, resolver, host)
		if err != nil {
			return nil, err
		}
		for _, pinned := range addresses {
			connection, dialErr := original(ctx, network, net.JoinHostPort(pinned.String(), port))
			if dialErr == nil {
				return connection, nil
			}
		}
		return nil, fetchError(CodeTransport)
	}
}

func resolvePublicAddresses(ctx context.Context, resolver AddressResolver, host string) ([]netip.Addr, error) {
	host = canonicalHost(host)
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return nil, fetchError(CodeNonPublic)
	}
	if literal, err := netip.ParseAddr(host); err == nil {
		literal = literal.Unmap()
		if !isPublicAddress(literal) {
			return nil, fetchError(CodeNonPublic)
		}
		return []netip.Addr{literal}, nil
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addresses, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, fetchError(CodeTransport)
	}
	result := make([]netip.Addr, 0, len(addresses))
	seen := map[netip.Addr]struct{}{}
	for _, address := range addresses {
		address = address.Unmap()
		if !isPublicAddress(address) {
			return nil, fetchError(CodeNonPublic)
		}
		if _, duplicate := seen[address]; !duplicate {
			seen[address] = struct{}{}
			result = append(result, address)
		}
	}
	// Prefer IPv4 for sequentially pinned dials. Some scientific archives
	// advertise IPv6 endpoints that accept TCP while their TLS path stalls;
	// returning such a socket prevents net/http from reaching a healthy IPv4
	// address. Retain every validated address for fallback.
	sort.SliceStable(result, func(left, right int) bool {
		return result[left].Is4() && !result[right].Is4()
	})
	return result, nil
}

var reservedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"), netip.MustParsePrefix("fec0::/10"),
}

func isPublicAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	for _, network := range reservedNetworks {
		if network.Contains(address) {
			return false
		}
	}
	return true
}

type boundedReadCloser struct {
	reader    io.Reader
	closer    io.Closer
	remaining int64
	exceeded  bool
}

func (r *boundedReadCloser) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if r.exceeded {
		return 0, fetchError(CodeResponseTooLarge)
	}
	limit := int64(len(buffer))
	if limit > r.remaining+1 {
		limit = r.remaining + 1
	}
	n, err := r.reader.Read(buffer[:limit])
	if int64(n) > r.remaining {
		allowed := int(r.remaining)
		r.remaining = 0
		r.exceeded = true
		return allowed, fetchError(CodeResponseTooLarge)
	}
	r.remaining -= int64(n)
	return n, err
}

func (r *boundedReadCloser) Close() error { return r.closer.Close() }

func fetchError(code ErrorCode) error { return &FetchError{Code: code} }

func fetchErrorWithCause(code ErrorCode, cause error) error {
	return &FetchError{Code: code, cause: cause}
}
