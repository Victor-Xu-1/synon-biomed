package mcpstdio

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"sync"
	"synon-go/internal/buildinfo"
)

func productClientInfo() map[string]any {
	identity := buildinfo.Release()
	return map[string]any{"name": identity.MachineSlug, "version": identity.Version}
}

// WithHTTPClient binds the supplied client to remote MCP/OAuth calls made with
// ctx. It keeps the default client behavior for ordinary runtime calls while
// allowing a caller to supply its own transport, proxy, or trust store.
func WithHTTPClient(ctx context.Context, client *http.Client) context.Context {
	if client == nil {
		return ctx
	}
	return context.WithValue(ctx, mcpHTTPClientContextKey{}, client)
}

type oauthResolverContextKey struct{}

type oauthIPResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

func withOAuthResolver(ctx context.Context, resolver oauthIPResolver) context.Context {
	if resolver == nil {
		return ctx
	}
	return context.WithValue(ctx, oauthResolverContextKey{}, resolver)
}

func oauthResolverForContext(ctx context.Context) oauthIPResolver {
	if resolver, ok := ctx.Value(oauthResolverContextKey{}).(oauthIPResolver); ok && resolver != nil {
		return resolver
	}
	return net.DefaultResolver
}

func httpClientForContext(ctx context.Context) *http.Client {
	if client, ok := ctx.Value(mcpHTTPClientContextKey{}).(*http.Client); ok && client != nil {
		return client
	}
	return http.DefaultClient
}

type ServerConfig struct {
	// ConnectorID is the stable authority key assigned by the trusted config
	// source. It is deliberately not accepted from project configuration data.
	ConnectorID string            `json:"-"`
	Type        string            `json:"type,omitempty"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	URL         string            `json:"url,omitempty"`
	Name        string            `json:"name,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	// QueryParams contains only runtime-injected owner credentials. It is never
	// decoded from or serialized into project MCP configuration because query
	// credentials can leak through URLs, logs, and referrers.
	QueryParams map[string]string `json:"-"`
	Disabled    bool              `json:"disabled,omitempty"`
	Scope       string            `json:"scope,omitempty"`

	PermissionPolicy  string         `json:"permissionPolicy,omitempty"`
	ApprovalMode      string         `json:"approvalMode,omitempty"`
	RequireApproval   bool           `json:"requireApproval,omitempty"`
	RequireReason     bool           `json:"requireReason,omitempty"`
	RememberDecisions bool           `json:"rememberDecisions,omitempty"`
	Permissions       map[string]any `json:"permissions,omitempty"`

	BridgeCommand string            `json:"bridgeCommand,omitempty"`
	BridgeArgs    []string          `json:"bridgeArgs,omitempty"`
	BridgeEnv     map[string]string `json:"bridgeEnv,omitempty"`

	// TrustedTLS is assigned only by the host runtime after configuration has
	// crossed its credential and network-policy boundary. It is never decoded
	// from connector JSON, so a connector cannot relax its own certificate
	// posture or replace the operator's CA bundle.
	TrustedTLS *TLSPosture `json:"-"`
}

type TLSPosture struct {
	Strict   bool
	CABundle string
	ProxyURL string
}

func ApplyTLSPosture(config ServerConfig, posture TLSPosture) ServerConfig {
	copy := posture
	config.TrustedTLS = &copy
	return config
}

type fileConfig struct {
	MCPServers map[string]ServerConfig `json:"mcpServers"`
}

type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	MimeType    string `json:"mimeType,omitempty"`
	Description string `json:"description,omitempty"`
	Server      string `json:"server"`
}

type ResourceReadResult struct {
	Contents []ResourceContent `json:"contents"`
}

type ResourceContent struct {
	URI         string `json:"uri"`
	MimeType    string `json:"mimeType,omitempty"`
	Text        string `json:"text,omitempty"`
	BlobSavedTo string `json:"blobSavedTo,omitempty"`
}

type ServerProjection struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Configured bool   `json:"configured"`
	Scope      string `json:"scope,omitempty"`
	ToolCount  int    `json:"toolCount"`
	Error      string `json:"error,omitempty"`
}

type ToolProjection struct {
	Name            string         `json:"name"`
	Server          string         `json:"server"`
	ToolName        string         `json:"toolName"`
	Description     string         `json:"description,omitempty"`
	ServerStatus    string         `json:"serverStatus"`
	HasInputSchema  bool           `json:"hasInputSchema"`
	InputProperties []string       `json:"inputProperties"`
	InputSchema     map[string]any `json:"inputSchema,omitempty"`
	HasOutputSchema bool           `json:"hasOutputSchema,omitempty"`
	OutputSchema    map[string]any `json:"outputSchema,omitempty"`
	ReadOnlyHint    bool           `json:"readOnlyHint,omitempty"`
}

type ToolListResult struct {
	Servers []ServerProjection `json:"servers"`
	Tools   []ToolProjection   `json:"tools"`
}

type OAuthStartResult struct {
	Status      string `json:"status"`
	Message     string `json:"message"`
	AuthURL     string `json:"authUrl,omitempty"`
	RedirectURI string `json:"redirectUri,omitempty"`
}

type oauthTokenCache struct {
	AccessToken string `json:"accessToken"`
	TokenType   string `json:"tokenType"`
	ExpiresAt   int64  `json:"expiresAt"`
	Origin      string `json:"origin,omitempty"`
}

type oauthMetadata struct {
	Issuer                                     string   `json:"issuer"`
	AuthorizationEndpoint                      string   `json:"authorization_endpoint"`
	TokenEndpoint                              string   `json:"token_endpoint"`
	RegistrationEndpoint                       string   `json:"registration_endpoint"`
	CodeChallengeMethodsSupported              []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethodsSupported          []string `json:"token_endpoint_auth_methods_supported"`
	ScopesSupported                            []string `json:"scopes_supported"`
	ClientIDMetadataDocumentSupported          bool     `json:"client_id_metadata_document_supported"`
	AuthorizationResponseISSParameterSupported bool     `json:"authorization_response_iss_parameter_supported"`
}

type oauthDiscovery struct {
	MetadataURLs []string
	Resource     string
	Scope        string
}

type oauthClientCredentials struct {
	ClientID                string
	ClientSecret            string
	TokenEndpointAuthMethod string
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type session struct {
	command    string
	cmd        *exec.Cmd
	process    *mcpProcessControl
	stdin      io.WriteCloser
	stdout     io.ReadCloser
	stderrPipe io.ReadCloser
	scanner    *bufio.Scanner
	encoder    *json.Encoder
	mu         sync.Mutex
	closeOnce  sync.Once
	stderr     *limitedBuffer
}

type sdkSession struct {
	serverName string
	command    string
	cmd        *exec.Cmd
	process    *mcpProcessControl
	stdin      io.WriteCloser
	stdout     io.ReadCloser
	stderrPipe io.ReadCloser
	scanner    *bufio.Scanner
	encoder    *json.Encoder
	mu         sync.Mutex
	closeOnce  sync.Once
	stderr     *limitedBuffer
}

// stopMCPStreamsOnContext closes the parent-side MCP pipes when a bounded
// connector call is cancelled. Killing the direct process is not sufficient
// when a connector leaves a child holding an inherited pipe open; Scanner.Scan
// or an Encode into stdin would otherwise wait past the caller's deadline and
// stall the whole tool turn.
func stopMCPStreamsOnContext(ctx context.Context, streams ...io.Closer) func() {
	if ctx == nil {
		return func() {}
	}
	active := make([]io.Closer, 0, len(streams))
	for _, stream := range streams {
		if stream != nil {
			active = append(active, stream)
		}
	}
	if len(active) == 0 {
		return func() {}
	}
	done := make(chan struct{})
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { close(done) }) }
	go func() {
		select {
		case <-ctx.Done():
			for _, stream := range active {
				_ = stream.Close()
			}
		case <-done:
		}
	}()
	return stop
}

// stopMCPOutputOnContext is kept as a narrow helper for scanner-focused
// tests and callers that only own the response stream.
func stopMCPOutputOnContext(ctx context.Context, stdout io.Closer) func() {
	return stopMCPStreamsOnContext(ctx, stdout)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

type limitedBuffer struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
}

var nameUnsafePattern = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
