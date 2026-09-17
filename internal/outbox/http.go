package outbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"synon-go/internal/persistence/workspace"
)

const (
	EventIDHeader        = "X-Synon-Event-ID"
	IdempotencyHeader    = "Idempotency-Key"
	defaultResponseLimit = 64 << 10
	maximumResponseLimit = 1 << 20
)

type HTTPDeliverer struct {
	Endpoint         string
	Client           *http.Client
	Headers          map[string]string
	MaxResponseBytes int64
}

func (h HTTPDeliverer) Deliver(ctx context.Context, event workspace.OutboxEvent) error {
	endpoint, err := url.Parse(strings.TrimSpace(h.Endpoint))
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return errors.New("outbox HTTP endpoint must be an absolute URL")
	}
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && isLoopbackHost(endpoint.Hostname())) {
		return errors.New("outbox HTTP endpoint must use HTTPS outside loopback")
	}
	if endpoint.User != nil {
		return errors.New("outbox HTTP endpoint must not contain user credentials")
	}
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal outbox event: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create outbox HTTP request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range h.Headers {
		if reservedDeliveryHeader(key) {
			continue
		}
		request.Header.Set(key, value)
	}
	for key, value := range event.Headers {
		if reservedDeliveryHeader(key) {
			continue
		}
		request.Header.Set(key, value)
	}
	request.Header.Set(EventIDHeader, event.ID)
	request.Header.Set(IdempotencyHeader, event.ID)
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	requestClient := *client
	requestClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := requestClient.Do(request)
	if err != nil {
		return fmt.Errorf("post outbox event: %w", err)
	}
	defer response.Body.Close()
	limit := h.MaxResponseBytes
	if limit <= 0 {
		limit = defaultResponseLimit
	}
	if limit > maximumResponseLimit {
		return fmt.Errorf("outbox HTTP response limit exceeds %d bytes", maximumResponseLimit)
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if readErr != nil {
		return fmt.Errorf("read outbox HTTP response: %w", readErr)
	}
	if int64(len(responseBody)) > limit {
		return errors.New("outbox HTTP response exceeds configured limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := strings.TrimSpace(string(responseBody))
		if len(message) > 512 {
			message = message[:512]
		}
		return fmt.Errorf("outbox HTTP response %d: %s", response.StatusCode, message)
	}
	return nil
}

func reservedDeliveryHeader(key string) bool {
	return strings.EqualFold(key, EventIDHeader) || strings.EqualFold(key, IdempotencyHeader) ||
		strings.EqualFold(key, "Content-Length") || strings.EqualFold(key, "Host")
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
