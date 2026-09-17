package webfetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"synon-go/internal/mcpdirectory"
)

// MaxResponseLimit is both the model-visible schema ceiling and the in-memory
// reader ceiling. Keeping one exported value prevents an admitted tool call
// from failing only after execution because the registry advertised a wider
// limit than the transport can honor.
const MaxResponseLimit int64 = 4 * 1024 * 1024
const MaxMetadataValueBytes = 4096
const maxRedirects = 5

type Options struct {
	ClientForURL   func(context.Context, string) (*http.Client, error)
	BaseHTTPClient *http.Client
	Now            func() time.Time
}

type RedirectHop struct {
	From       string `json:"from"`
	To         string `json:"to"`
	StatusCode int    `json:"statusCode"`
}

type Result struct {
	RequestedURL      string        `json:"requestedUrl,omitempty"`
	StatusCode        int           `json:"statusCode"`
	ContentType       string        `json:"contentType"`
	Body              string        `json:"body"`
	RawBodyBase64     string        `json:"rawBodyBase64,omitempty"`
	URL               string        `json:"url"`
	BytesRead         int           `json:"bytesRead"`
	ContentLength     int64         `json:"contentLength,omitempty"`
	RetrievedAt       string        `json:"retrievedAt,omitempty"`
	ResponseDate      string        `json:"responseDate,omitempty"`
	LastModified      string        `json:"lastModified,omitempty"`
	ETag              string        `json:"etag,omitempty"`
	ContentLanguage   string        `json:"contentLanguage,omitempty"`
	ContentLocation   string        `json:"contentLocation,omitempty"`
	BodySHA256        string        `json:"bodySha256,omitempty"`
	BodyHashScope     string        `json:"bodyHashScope,omitempty"`
	Complete          bool          `json:"complete"`
	Redirects         []RedirectHop `json:"redirects,omitempty"`
	MetadataTruncated []string      `json:"metadataTruncated,omitempty"`
	Truncated         bool          `json:"truncated,omitempty"`
	Binary            bool          `json:"binary,omitempty"`
	Partial           bool          `json:"partial,omitempty"`
	Warning           string        `json:"warning,omitempty"`
	Recovery          string        `json:"recovery,omitempty"`
	SourceUnavailable bool          `json:"sourceUnavailable,omitempty"`
	// Reused indicates that an identical successful read was served from the
	// current logical run's bounded read cache. It preserves the full typed
	// response while making duplicate-fetch suppression observable to the model.
	Reused bool   `json:"reused,omitempty"`
	Error  string `json:"error,omitempty"`
}

// ToolResultEnvelope exposes only the documented result-status fields to the
// agent runtime classifier. The response body remains available for diagnosis
// but cannot become trusted source evidence when SourceUnavailable is true.
func (result Result) ToolResultEnvelope() map[string]any {
	return map[string]any{
		"sourceUnavailable": result.SourceUnavailable,
		"partial":           result.Partial,
		"error":             result.Error,
	}
}

func Fetch(ctx context.Context, url string, limit int64) (Result, error) {
	return FetchWithOptions(ctx, url, limit, Options{})
}

func FetchWithOptions(ctx context.Context, rawURL string, limit int64, options Options) (Result, error) {
	if limit <= 0 {
		limit = MaxResponseLimit
	}
	if limit > MaxResponseLimit {
		return Result{}, fmt.Errorf("web response limit %d exceeds the configured in-memory maximum %d", limit, MaxResponseLimit)
	}
	clientForURL := options.ClientForURL
	if clientForURL == nil {
		clientForURL = func(requestCtx context.Context, target string) (*http.Client, error) {
			base := options.BaseHTTPClient
			if base == nil {
				base = http.DefaultClient
			}
			clone := *base
			if clone.Timeout <= 0 || clone.Timeout > 15*time.Second {
				clone.Timeout = 15 * time.Second
			}
			return mcpdirectory.SecureHTTPClient(requestCtx, target, &clone)
		}
	}
	requestedURL := strings.TrimSpace(rawURL)
	current := requestedURL
	redirects := []RedirectHop{}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	finish := func(result Result, header http.Header, returnedBytes []byte, complete bool) Result {
		result.RequestedURL = requestedURL
		result.Redirects = append([]RedirectHop(nil), redirects...)
		result.RetrievedAt = now().UTC().Format(time.RFC3339Nano)
		result.Complete = complete
		if header != nil {
			assign := func(name, value string) string {
				bounded, truncated := boundedWebMetadata(value)
				if truncated {
					result.MetadataTruncated = append(result.MetadataTruncated, name)
				}
				return bounded
			}
			result.ResponseDate = assign("response_date", header.Get("Date"))
			result.LastModified = assign("last_modified", header.Get("Last-Modified"))
			result.ETag = assign("etag", header.Get("ETag"))
			result.ContentLanguage = assign("content_language", header.Get("Content-Language"))
			result.ContentLocation = assign("content_location", header.Get("Content-Location"))
		}
		if len(returnedBytes) > 0 {
			digest := sha256.Sum256(returnedBytes)
			result.BodySHA256 = hex.EncodeToString(digest[:])
			result.BodyHashScope = "returned_response_bytes"
		}
		return result
	}
	for redirect := 0; redirect <= maxRedirects; redirect++ {
		client, err := clientForURL(ctx, current)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return Result{}, ctxErr
			}
			if mcpdirectory.IsPublicDestinationUnavailable(err) {
				return finish(recoverableSourceUnavailable(
					current,
					"Public source address is unavailable or blocked by the local network.",
				), nil, nil, false), nil
			}
			return Result{}, fmt.Errorf("web destination is not an approved public HTTPS origin: %w", err)
		}
		client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, current, nil)
		if err != nil {
			return Result{}, fmt.Errorf("build request: %w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return Result{}, ctxErr
			}
			message := "Public source is temporarily unavailable."
			var networkError net.Error
			if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkError) && networkError.Timeout() {
				message = "Public source request timed out."
			}
			return finish(recoverableSourceUnavailable(current, message), nil, nil, false), nil
		}
		if resp.StatusCode >= 300 && resp.StatusCode <= 399 {
			location := strings.TrimSpace(resp.Header.Get("Location"))
			_ = resp.Body.Close()
			if location == "" {
				return Result{}, errors.New("web redirect is missing Location")
			}
			base, parseErr := url.Parse(current)
			if parseErr != nil {
				return Result{}, fmt.Errorf("parse redirect base: %w", parseErr)
			}
			next, parseErr := base.Parse(location)
			if parseErr != nil {
				return Result{}, fmt.Errorf("parse redirect destination: %w", parseErr)
			}
			redirects = append(redirects, RedirectHop{From: current, To: next.String(), StatusCode: resp.StatusCode})
			current = next.String()
			continue
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		closeErr := resp.Body.Close()
		if readErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return Result{}, ctxErr
			}
			message := "Public source response could not be read completely."
			var networkError net.Error
			if errors.Is(readErr, context.DeadlineExceeded) || errors.As(readErr, &networkError) && networkError.Timeout() {
				message = "Public source response timed out while reading."
			}
			result := recoverableSourceUnavailable(current, message)
			result.StatusCode = resp.StatusCode
			result.ContentType = resp.Header.Get("Content-Type")
			result.ContentLength = resp.ContentLength
			if result.ContentLength < 0 {
				result.ContentLength = 0
			}
			if int64(len(body)) > limit {
				body = body[:limit]
				result.Truncated = true
			}
			result.BytesRead = len(body)
			result.Binary = webResponseIsBinary(current, result.ContentType, body, true)
			result.Partial = true
			if result.Binary && resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
				// WebFetch is intentionally a bounded page reader, not a binary
				// downloader. Once the public server has returned a successful binary
				// response and recognizable bytes, an interrupted body read is a safe
				// handoff authority for the governed streaming downloader. It is not
				// evidence that the source itself is unavailable.
				result.SourceUnavailable = false
				result.Error = ""
				result.Recovery = "use_dedicated_download_or_fulltext_tool"
				result.Warning = "Binary or scientific-file content was not read completely by the bounded page reader. Use a dedicated governed download tool with this exact URL."
			} else {
				result.Warning = "The public response ended before the bounded read completed; any returned prefix is diagnostic only and must not be treated as complete source evidence."
				assignWebResponseText(&result, body, true)
			}
			return finish(result, resp.Header, body, false), nil
		}
		truncated := int64(len(body)) > limit
		if truncated {
			body = body[:limit]
		}
		binary := webResponseIsBinary(current, resp.Header.Get("Content-Type"), body, truncated)
		contentLength := resp.ContentLength
		if contentLength < 0 {
			contentLength = 0
		}
		result := Result{
			StatusCode:    resp.StatusCode,
			ContentType:   resp.Header.Get("Content-Type"),
			URL:           current,
			BytesRead:     len(body),
			ContentLength: contentLength,
			Truncated:     truncated,
			Binary:        binary,
			Partial:       truncated || binary,
		}
		if result.Partial {
			result.Recovery = "use_dedicated_download_or_fulltext_tool"
			if binary {
				result.Warning = "Binary or scientific-file content was omitted from the model context. Use a dedicated governed download or full-text tool with this exact URL."
			} else {
				result.Warning = fmt.Sprintf("Response exceeded the bounded reader limit of %d bytes; only the returned prefix is available and must not be treated as the complete source.", limit)
			}
		} else if closeErr != nil {
			// The bounded response bytes are already complete. A transport-level
			// cleanup error is diagnostic, but it must not invalidate valid data.
			result.Warning = "The public response was read successfully, but closing the network body reported an error."
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			result.SourceUnavailable = true
			result.Error = fmt.Sprintf("HTTP source returned %s.", resp.Status)
		}
		assignWebResponseText(&result, body, truncated)
		return finish(result, resp.Header, body, !truncated), nil
	}
	return Result{}, fmt.Errorf("web redirect chain exceeds %d hops", maxRedirects)
}

func boundedWebMetadata(value string) (string, bool) {
	value = strings.TrimSpace(strings.ToValidUTF8(value, "�"))
	if len(value) <= MaxMetadataValueBytes {
		return value, false
	}
	value = value[:MaxMetadataValueBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value, true
}

func recoverableSourceUnavailable(rawURL, message string) Result {
	return Result{
		URL:               rawURL,
		SourceUnavailable: true,
		Error:             message,
		Recovery:          "retry_later_or_use_source_specific_tool",
	}
}

func webResponseIsBinary(rawURL, contentType string, body []byte, incomplete bool) bool {
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	if strings.HasPrefix(mediaType, "text/") || strings.Contains(mediaType, "json") ||
		strings.Contains(mediaType, "xml") || strings.Contains(mediaType, "javascript") ||
		mediaType == "application/x-www-form-urlencoded" {
		return false
	}
	for _, marker := range []string{
		"application/octet-stream", "application/pdf", "application/zip", "application/gzip",
		"application/x-gzip", "application/x-tar", "application/x-hdf5", "application/x-bzip2",
		"application/x-xz", "application/vnd.apache.parquet",
	} {
		if mediaType == marker {
			return true
		}
	}
	path := strings.ToLower(rawURL)
	if parsed, err := url.Parse(rawURL); err == nil {
		path = strings.ToLower(parsed.Path)
	}
	for _, suffix := range []string{
		".gz", ".zip", ".tar", ".bz2", ".xz", ".h5", ".hdf5", ".h5ad", ".parquet", ".pdf",
	} {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	// Decode whole code points from the original bytes even when they cross
	// the sniff window. A partial transport may also end mid-code-point; it is
	// decoded separately with its explicit partial status, not as binary data.
	for offset := 0; offset < min(len(body), 4096); {
		char, width := utf8.DecodeRune(body[offset:])
		if char == 0 {
			return true
		}
		if char == utf8.RuneError && width == 1 {
			return !incomplete || utf8.FullRune(body[offset:])
		}
		offset += width
	}
	return false
}
