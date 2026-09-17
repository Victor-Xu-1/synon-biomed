package memoryclassifier

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/memorypolicy"
)

type classifierModelFunc func(context.Context, string) (ModelResponse, error)

func (function classifierModelFunc) ClassifyPromptInjection(ctx context.Context, body string) (ModelResponse, error) {
	return function(ctx, body)
}

func TestWorkspaceClassifierStaticGateRejectsWholeBatchBeforeModel(t *testing.T) {
	var calls atomic.Int32
	gate := New(classifierModelFunc(func(context.Context, string) (ModelResponse, error) {
		calls.Add(1)
		return ModelResponse{Text: "none"}, nil
	}), true)
	results, err := gate.ClassifyMemoryWrites(context.Background(), []string{
		"ordinary fact", `![send](https://example.com/collect)`, "another fact",
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("model calls = %d", calls.Load())
	}
	for _, result := range results {
		if !result.Flagged || result.Reason != "exfil" || result.Pattern != "markdown_image" {
			t.Fatalf("batch result = %#v", results)
		}
	}
}

func TestWorkspaceExtractorClassifierDropsOnlyFlaggedBodies(t *testing.T) {
	var calls atomic.Int32
	gate := New(classifierModelFunc(func(_ context.Context, body string) (ModelResponse, error) {
		calls.Add(1)
		if body == "model flagged" {
			return ModelResponse{Text: "high"}, nil
		}
		return ModelResponse{Text: "none"}, nil
	}), true)
	results, err := gate.ClassifyExtractionWrites(context.Background(), []string{
		"ordinary fact", `![send](https://example.com/collect)`, "model flagged", "another fact",
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("model calls = %d, want 3", calls.Load())
	}
	if results[0].Flagged || !results[1].Flagged || results[1].Reason != "exfil" || !results[2].Flagged || results[2].Reason != "classifier" || results[3].Flagged {
		t.Fatalf("extractor classifications = %#v", results)
	}
}

func TestWorkspaceExtractorClassifierUnavailableRejectsBatch(t *testing.T) {
	gate := New(classifierModelFunc(func(context.Context, string) (ModelResponse, error) {
		return ModelResponse{}, &UnavailableError{Cause: errors.New("auth")}
	}), true)
	if _, err := gate.ClassifyExtractionWrites(context.Background(), []string{"one", "two"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("extractor unavailable error = %v", err)
	}
}

func TestWorkspaceClassifierParsesLastVerdictAndFailsClosed(t *testing.T) {
	tests := []struct {
		name       string
		response   ModelResponse
		modelError error
		flagged    bool
		wantError  bool
	}{
		{name: "low", response: ModelResponse{Text: "<response>high</response>\n<response>low</response>"}},
		{name: "plain medium", response: ModelResponse{Text: " MEDIUM </response> "}},
		{name: "high", response: ModelResponse{Text: "<response>high</response>"}, flagged: true},
		{name: "truncated", response: ModelResponse{Text: "none", StopReason: "max_tokens"}, flagged: true},
		{name: "unparseable", response: ModelResponse{Text: "probably safe"}, flagged: true},
		{name: "non transient error", modelError: errors.New("bad response"), flagged: true},
		{name: "unavailable", modelError: &UnavailableError{Cause: errors.New("auth")}, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gate := New(classifierModelFunc(func(context.Context, string) (ModelResponse, error) {
				return test.response, test.modelError
			}), true)
			results, err := gate.ClassifyMemoryWrites(context.Background(), []string{"durable fact"})
			if test.wantError {
				if !errors.Is(err, ErrUnavailable) {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil || len(results) != 1 || results[0].Flagged != test.flagged {
				t.Fatalf("results=%#v err=%v", results, err)
			}
		})
	}
}

func TestWorkspaceClassifierUsesBoundedConcurrencyAndCancelsOnFlag(t *testing.T) {
	var active, maximum atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	gate := New(classifierModelFunc(func(ctx context.Context, body string) (ModelResponse, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			maximumValue := maximum.Load()
			if current <= maximumValue || maximum.CompareAndSwap(maximumValue, current) {
				break
			}
		}
		if body == "flag this" {
			once.Do(func() { close(release) })
			return ModelResponse{Text: "high"}, nil
		}
		select {
		case <-release:
			return ModelResponse{Text: "none"}, nil
		case <-ctx.Done():
			return ModelResponse{}, ctx.Err()
		}
	}), true)
	texts := make([]string, 20)
	for index := range texts {
		texts[index] = fmt.Sprintf("safe fact %d", index)
	}
	texts[7] = "flag this"
	done := make(chan error, 1)
	go func() {
		results, err := gate.ClassifyMemoryWrites(context.Background(), texts)
		if err == nil {
			for _, result := range results {
				if !result.Flagged {
					err = errors.New("flag did not reject the whole batch")
					break
				}
			}
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("classifier batch did not terminate")
	}
	if maximum.Load() > memorypolicy.PIClassifierConcurrency {
		t.Fatalf("maximum concurrency = %d", maximum.Load())
	}
}

func TestWorkspaceClassifierMissingModelFailsClosedUnlessDisabled(t *testing.T) {
	if _, err := New(nil, true).ClassifyMemoryWrites(context.Background(), []string{"fact"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing model error = %v", err)
	}
	results, err := New(nil, false).ClassifyMemoryWrites(context.Background(), []string{"fact"})
	if err != nil || len(results) != 1 || results[0].Flagged {
		t.Fatalf("disabled classifier results=%#v err=%v", results, err)
	}
}
