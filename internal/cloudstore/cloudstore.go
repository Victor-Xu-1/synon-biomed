package cloudstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultObjectLimit = 200
	DefaultFolderLimit = 5000
	MaxPageSize        = 1000
)

type Credentials struct {
	Values map[string]string
	Region string
}

type Object struct {
	Key          string `json:"key"`
	Size         int64  `json:"size"`
	LastModified string `json:"last_modified"`
}

type ObjectInfo struct {
	Size        int64
	ContentType string
}

type Page struct {
	Objects   []Object
	Prefixes  []string
	NextToken string
}

type Client interface {
	ListBuckets(context.Context) ([]string, error)
	ListPage(context.Context, string, string, string, int, string) (Page, error)
	HeadObject(context.Context, string, string) (ObjectInfo, error)
	OpenObject(context.Context, string, string) (io.ReadCloser, ObjectInfo, error)
	PutObject(context.Context, string, string, io.Reader, int64, string) (int64, error)
}

type ClientFactory interface {
	New(provider string, credentials Credentials) (Client, error)
}

type FactoryOptions struct {
	HTTPClient                 *http.Client
	S3Endpoint                 string
	GCSAPIEndpoint             string
	GCSTokenEndpoint           string
	AzureBlobEndpoint          string
	AzureTokenEndpoint         string
	AllowInsecureTestEndpoints bool
}

type Factory struct {
	options FactoryOptions
}

func NewFactory(client *http.Client) *Factory {
	return NewFactoryWithOptions(FactoryOptions{HTTPClient: client})
}

func NewFactoryWithOptions(options FactoryOptions) *Factory {
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	copyClient := *options.HTTPClient
	if copyClient.Timeout == 0 {
		copyClient.Timeout = 60 * time.Second
	}
	copyClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return errors.New("cloud storage redirects are disabled")
	}
	options.HTTPClient = &copyClient
	return &Factory{options: options}
}

func (f *Factory) New(provider string, credentials Credentials) (Client, error) {
	if f == nil {
		return nil, errors.New("cloud client factory is nil")
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	values := cloneValues(credentials.Values)
	if strings.TrimSpace(credentials.Region) != "" && strings.TrimSpace(values["region"]) == "" {
		values["region"] = strings.TrimSpace(credentials.Region)
	}
	switch provider {
	case "aws", "s3":
		return newS3Client(f.options, values, false)
	case "gcp", "gcs":
		if strings.TrimSpace(values["access_key_id"]) != "" {
			return newS3Client(f.options, values, true)
		}
		return newGCSClient(f.options, values)
	case "azure":
		return newAzureClient(f.options, values)
	default:
		return nil, fmt.Errorf("unsupported cloud provider %q", provider)
	}
}

func cloneValues(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	return result
}

func normalizedLimit(value, fallback, maximum int) int {
	if value <= 0 {
		value = fallback
	}
	if value > maximum {
		value = maximum
	}
	return value
}

func boolValue(value string, fallback bool) bool {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func endpointURL(raw, fallback string, allowInsecure bool) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		raw = fallback
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("cloud endpoint URL is invalid")
	}
	if parsed.Scheme != "https" && !(allowInsecure && parsed.Scheme == "http") {
		return nil, errors.New("cloud endpoint must use HTTPS")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "" {
		return nil, errors.New("cloud endpoint host is required")
	}
	if !allowInsecure {
		if net.ParseIP(host) != nil || host == "localhost" || !strings.Contains(host, ".") ||
			strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
			return nil, errors.New("cloud endpoint must use a public DNS host")
		}
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	return parsed, nil
}

func setEscapedPath(target *url.URL, escaped string) {
	decoded, err := url.PathUnescape(escaped)
	if err != nil {
		target.Path, target.RawPath = escaped, ""
		return
	}
	target.Path, target.RawPath = decoded, escaped
}

func responseError(response *http.Response) error {
	defer response.Body.Close()
	limited, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	message := strings.TrimSpace(string(limited))
	if message == "" {
		message = response.Status
	}
	return &HTTPError{StatusCode: response.StatusCode, Message: message}
}

type HTTPError struct {
	StatusCode int
	Message    string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("cloud storage returned HTTP %d: %s", e.StatusCode, e.Message)
}

func isSuccess(status int) bool {
	return status >= 200 && status < 300
}
