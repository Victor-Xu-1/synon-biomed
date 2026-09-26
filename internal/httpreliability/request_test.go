package httpreliability

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAttemptProgressOutlivesHeaderBudgetAndDetectsRealIdle(t *testing.T) {
	for _, idle := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			for index := 0; index < 8; index++ {
				delay := 15 * time.Millisecond
				if idle {
					delay = 300 * time.Millisecond
				}
				select {
				case <-r.Context().Done():
					return
				case <-time.After(delay):
				}
				_, _ = io.WriteString(w, "x")
				w.(http.Flusher).Flush()
			}
		}))
		attempt := Begin(context.Background(), 60*time.Millisecond)
		request, _ := http.NewRequestWithContext(attempt.Context, http.MethodGet, server.URL, nil)
		response, err := server.Client().Do(request)
		if err != nil {
			attempt.Close()
			server.Close()
			t.Fatal(err)
		}
		body, err := attempt.Body(response.Body, 100*time.Millisecond)
		if err != nil {
			attempt.Close()
			server.Close()
			t.Fatal(err)
		}
		value, readErr := io.ReadAll(body)
		_ = body.Close()
		server.Close()
		if idle && !errors.Is(readErr, ErrBodyIdleTimeout) {
			t.Fatalf("idle error=%v", readErr)
		}
		if !idle && (readErr != nil || string(value) != "xxxxxxxx") {
			t.Fatalf("progressing response=%q error=%v", value, readErr)
		}
	}
}

func TestAttemptHeaderBudgetAndParentCancellation(t *testing.T) {
	for _, cancelParent := range []bool{false, true} {
		parent, cancel := context.WithCancel(context.Background())
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
		attempt := Begin(parent, 40*time.Millisecond)
		if cancelParent {
			cancel()
		}
		request, _ := http.NewRequestWithContext(attempt.Context, http.MethodGet, server.URL, nil)
		_, err := server.Client().Do(request)
		err = attempt.Error(err)
		attempt.Close()
		cancel()
		server.Close()
		want := error(ErrResponseHeadersTimeout)
		if cancelParent {
			want = context.Canceled
		}
		if !errors.Is(err, want) {
			t.Fatalf("header/cancel error=%v want=%v", err, want)
		}
	}
}

func TestRetryAdviceNeverRetriesEarlyAndRemainsCancelable(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	if RetryAfter("120", now) != 2*time.Minute || RetryAfter(now.Add(2*time.Minute).Format(http.TimeFormat), now) != 2*time.Minute {
		t.Fatal("server retry advice shortened")
	}
	for _, invalid := range []string{"-2", "invalid", "\r\n"} {
		if RetryAfter(invalid, now) != 0 {
			t.Fatal("invalid retry advice accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Wait(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("backoff ignored cancel: %v", err)
	}
	if !TransientStatus(429) || !TransientStatus(503) || TransientStatus(404) || TransientStatus(501) {
		t.Fatal("HTTP retry classification")
	}
}
