package networktls

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	ModeAuto    = "auto"
	ModeStrict  = "strict"
	ModeRelaxed = "relaxed"

	MCPX509StrictEnv = "SYNON_MCP_X509_STRICT"

	SourceOperatorEnv     = "operator-env"
	SourceConfig          = "config"
	SourceNetworkCABundle = "network-ca-bundle"
	SourceSystemKeychain  = "system-keychain"
	SourceAutoNone        = "auto-none"

	postureRefreshInterval = 6 * time.Minute
	maximumCABundleBytes   = 16 << 20
)

var relaxedValues = map[string]struct{}{
	"0": {}, "relaxed": {}, "relax": {}, "false": {}, "off": {}, "no": {},
}

var strictValues = map[string]struct{}{
	"1": {}, "strict": {}, "true": {}, "on": {}, "yes": {},
}

type Config struct {
	Mode     string
	CABundle string
}

type Diagnostics struct {
	OverrideUnrecognized bool   `json:"overrideUnrecognized,omitempty"`
	CABundleRefusal      string `json:"caBundleRefusal,omitempty"`
	DetectionErrored     bool   `json:"detectionErrored,omitempty"`
}

type Posture struct {
	Strict      bool        `json:"strict"`
	Source      string      `json:"source"`
	CABundle    string      `json:"caBundle,omitempty"`
	Diagnostics Diagnostics `json:"diagnostics"`
}

type corporateAnchorDetector func(context.Context) (bool, error)

type Resolver struct {
	config         Config
	platform       string
	detectAnchors  corporateAnchorDetector
	validateBundle func(string) (string, error)
	now            func() time.Time

	mu       sync.Mutex
	cached   Posture
	resolved time.Time
	hasCache bool
}

func NewResolver(config Config) *Resolver {
	return newResolverForTest(config, runtime.GOOS, detectCorporateAnchors)
}

func newResolverForTest(config Config, platform string, detector corporateAnchorDetector) *Resolver {
	mode := strings.ToLower(strings.TrimSpace(config.Mode))
	if mode == "" {
		mode = ModeAuto
	}
	return &Resolver{
		config:         Config{Mode: mode, CABundle: strings.TrimSpace(config.CABundle)},
		platform:       strings.ToLower(strings.TrimSpace(platform)),
		detectAnchors:  detector,
		validateBundle: validateCABundle,
		now:            time.Now,
	}
}

func ValidateMode(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ModeAuto, nil
	}
	switch value {
	case ModeAuto, ModeStrict, ModeRelaxed:
		return value, nil
	default:
		return "", fmt.Errorf("must be one of %q, %q, or %q", ModeAuto, ModeStrict, ModeRelaxed)
	}
}

func (r *Resolver) Current(ctx context.Context) Posture {
	if r == nil {
		return Posture{Strict: true, Source: SourceAutoNone}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if r.hasCache && now.Sub(r.resolved) < postureRefreshInterval {
		return r.cached
	}
	r.cached = r.resolve(ctx)
	r.resolved = now
	r.hasCache = true
	return r.cached
}

func (r *Resolver) Invalidate() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.hasCache = false
	r.mu.Unlock()
}

func (r *Resolver) resolve(ctx context.Context) Posture {
	bundle, refusal := r.resolveBundle()
	diagnostics := Diagnostics{CABundleRefusal: refusal}
	override, recognized := parseOverride(os.Getenv(MCPX509StrictEnv))
	if recognized {
		return Posture{Strict: override, Source: SourceOperatorEnv, CABundle: bundle, Diagnostics: diagnostics}
	}
	if value := strings.TrimSpace(os.Getenv(MCPX509StrictEnv)); value != "" && !strings.EqualFold(value, ModeAuto) {
		diagnostics.OverrideUnrecognized = true
	}

	switch r.config.Mode {
	case ModeStrict:
		return Posture{Strict: true, Source: SourceConfig, CABundle: bundle, Diagnostics: diagnostics}
	case ModeRelaxed:
		return Posture{Strict: false, Source: SourceConfig, CABundle: bundle, Diagnostics: diagnostics}
	}

	if bundle != "" {
		return Posture{Strict: false, Source: SourceNetworkCABundle, CABundle: bundle, Diagnostics: diagnostics}
	}
	if r.platform == "darwin" && r.detectAnchors != nil {
		detected, err := r.detectAnchors(ctx)
		if err != nil {
			diagnostics.DetectionErrored = true
		} else if detected {
			return Posture{Strict: false, Source: SourceSystemKeychain, Diagnostics: diagnostics}
		}
	}
	return Posture{Strict: true, Source: SourceAutoNone, Diagnostics: diagnostics}
}

func (r *Resolver) resolveBundle() (string, string) {
	if strings.TrimSpace(r.config.CABundle) == "" {
		return "", ""
	}
	path, err := r.validateBundle(r.config.CABundle)
	if err != nil {
		return "", err.Error()
	}
	return path, ""
}

func parseOverride(value string) (strict bool, recognized bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || value == ModeAuto {
		return false, false
	}
	if _, ok := strictValues[value]; ok {
		return true, true
	}
	if _, ok := relaxedValues[value]; ok {
		return false, true
	}
	return false, false
}

func validateCABundle(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("inspect file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("path is not a regular file")
	}
	if info.Size() <= 0 || info.Size() > maximumCABundleBytes {
		return "", fmt.Errorf("file size %d is outside 1..%d bytes", info.Size(), maximumCABundleBytes)
	}
	raw, err := os.ReadFile(canonical)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	if _, err := appendCABundle(x509.NewCertPool(), raw); err != nil {
		return "", err
	}
	return filepath.Clean(canonical), nil
}

func appendCABundle(pool *x509.CertPool, raw []byte) (*x509.CertPool, error) {
	if pool == nil {
		pool = x509.NewCertPool()
	}
	remaining := raw
	certificates := 0
	for {
		block, rest := pem.Decode(remaining)
		if block == nil {
			break
		}
		remaining = rest
		if block.Type != "CERTIFICATE" {
			continue
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse certificate %d: %w", certificates+1, err)
		}
		pool.AddCert(certificate)
		certificates++
	}
	if certificates == 0 {
		return nil, errors.New("file contains no parseable PEM certificates")
	}
	return pool, nil
}

func RootPool(bundlePath string) (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if strings.TrimSpace(bundlePath) == "" {
		return pool, nil
	}
	raw, err := os.ReadFile(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("read network CA bundle: %w", err)
	}
	return appendCABundle(pool, raw)
}

func HTTPClient(base *http.Client, bundlePath string, proxyURL ...string) (*http.Client, error) {
	if base == nil {
		base = http.DefaultClient
	}
	client := *base
	var transport *http.Transport
	switch value := base.Transport.(type) {
	case nil:
		transport = http.DefaultTransport.(*http.Transport).Clone()
	case *http.Transport:
		transport = value.Clone()
	default:
		return nil, fmt.Errorf("HTTP transport %T cannot accept the configured CA bundle", base.Transport)
	}
	pool, err := RootPool(bundlePath)
	if err != nil {
		return nil, err
	}
	if transport.TLSClientConfig != nil {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	} else {
		transport.TLSClientConfig = new(tls.Config)
	}
	transport.TLSClientConfig.RootCAs = pool
	if len(proxyURL) > 0 {
		if err := configureHTTPProxy(transport, proxyURL[0]); err != nil {
			return nil, err
		}
	}
	client.Transport = transport
	return &client, nil
}
