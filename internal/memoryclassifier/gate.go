package memoryclassifier

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"synon-go/internal/memorypolicy"
	"synon-go/internal/memorytools"
	workspace "synon-go/internal/persistence/workspace"
)

var (
	ErrUnavailable           = errors.New("memory prompt-injection classifier unavailable")
	responseTagPattern       = regexp.MustCompile(`(?is)<response>\s*(none|low|medium|high)\b`)
	responsePlainPattern     = regexp.MustCompile(`(?is)^\s*(none|low|medium|high)\s*(?:</response>)?\s*$`)
	classifierWrapperPattern = regexp.MustCompile(`(?i)</?(?:memory[_a-z]*|response)\b[^>]*>`)
	classifierClosePattern   = regexp.MustCompile(`(?i)</transcript\b[^>]*>`)
	classifierRolePattern    = regexp.MustCompile(`(?im)^[ \t]*(User|AI|Human|Assistant)\s*:`)
)

type ModelResponse struct {
	Text       string
	StopReason string
}

type Model interface {
	ClassifyPromptInjection(ctx context.Context, body string) (ModelResponse, error)
}

// UnavailableError identifies timeout, cancellation, authentication,
// model-not-found, and transient provider failures. The memory service rejects the
// entire write batch when any one of these prevents classification.
type UnavailableError struct {
	Cause error
}

func (e *UnavailableError) Error() string {
	if e == nil || e.Cause == nil {
		return ErrUnavailable.Error()
	}
	return fmt.Sprintf("%s: %v", ErrUnavailable, e.Cause)
}

func (e *UnavailableError) Unwrap() error { return ErrUnavailable }

type Gate struct {
	model   Model
	enabled bool
}

func New(model Model, enabled bool) *Gate {
	return &Gate{model: model, enabled: enabled}
}

func (g *Gate) ClassifyMemoryWrites(ctx context.Context, texts []string) ([]memorytools.Classification, error) {
	return g.classifyBatch(ctx, texts, true)
}

// ClassifyExtractionWrites evaluates candidates independently: a flagged
// body is dropped individually, while classifier unavailability rejects the
// whole extraction batch so its durable cursor can be held for a retry.
func (g *Gate) ClassifyExtractionWrites(ctx context.Context, texts []string) ([]memorytools.Classification, error) {
	return g.classifyBatch(ctx, texts, false)
}

func (g *Gate) classifyBatch(ctx context.Context, texts []string, abortOnFlag bool) ([]memorytools.Classification, error) {
	results := make([]memorytools.Classification, len(texts))
	if len(texts) == 0 {
		return results, nil
	}
	pending := make([]int, 0, len(texts))
	for index, text := range texts {
		if pattern := workspace.FindMemoryExfilPattern(text); pattern != "" {
			flagged := memorytools.Classification{Flagged: true, Reason: "exfil", Pattern: pattern}
			results[index] = flagged
			if abortOnFlag {
				fillClassifications(results, flagged)
				return results, nil
			}
			continue
		}
		pending = append(pending, index)
	}
	if len(pending) == 0 {
		return results, nil
	}
	if g == nil || !g.enabled {
		return results, nil
	}
	if g.model == nil {
		return nil, &UnavailableError{Cause: errors.New("no classifier model is configured")}
	}
	if ctx == nil {
		return nil, &UnavailableError{Cause: errors.New("classifier context is required")}
	}

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	workerCount := minInt(memorypolicy.PIClassifierConcurrency, len(pending))
	var (
		workers       sync.WaitGroup
		mu            sync.Mutex
		flaggedResult *memorytools.Classification
		unavailable   error
	)
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for {
				select {
				case <-workerCtx.Done():
					return
				case index, ok := <-jobs:
					if !ok {
						return
					}
					classification, err := g.classifyOne(workerCtx, texts[index])
					mu.Lock()
					if err != nil && unavailable == nil {
						unavailable = err
					}
					if err == nil {
						results[index] = classification
						if abortOnFlag && classification.Flagged && flaggedResult == nil {
							copy := classification
							flaggedResult = &copy
						}
					}
					shouldStop := err != nil || abortOnFlag && classification.Flagged
					mu.Unlock()
					if shouldStop {
						cancel()
						return
					}
				}
			}
		}()
	}
	feedDone := make(chan struct{})
	go func() {
		defer close(feedDone)
		defer close(jobs)
		for _, index := range pending {
			select {
			case <-workerCtx.Done():
				return
			case jobs <- index:
			}
		}
	}()
	workers.Wait()
	<-feedDone

	mu.Lock()
	defer mu.Unlock()
	if abortOnFlag && flaggedResult != nil {
		fillClassifications(results, *flaggedResult)
		return results, nil
	}
	if unavailable != nil {
		return nil, unavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, &UnavailableError{Cause: err}
	}
	return results, nil
}

func (g *Gate) classifyOne(ctx context.Context, text string) (memorytools.Classification, error) {
	callCtx, cancel := context.WithTimeout(ctx, memorypolicy.PIClassifierDeadline)
	defer cancel()
	response, err := g.model.ClassifyPromptInjection(callCtx, neutralizeClassifierBody(text))
	if err != nil {
		if errors.Is(err, ErrUnavailable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return memorytools.Classification{}, &UnavailableError{Cause: err}
		}
		return memorytools.Classification{Flagged: true, Reason: "classifier"}, nil
	}
	if strings.EqualFold(strings.TrimSpace(response.StopReason), "max_tokens") {
		return memorytools.Classification{Flagged: true, Reason: "classifier"}, nil
	}
	verdict, ok := parseVerdict(response.Text)
	if !ok || verdict == "high" {
		return memorytools.Classification{Flagged: true, Reason: "classifier"}, nil
	}
	return memorytools.Classification{}, nil
}

func parseVerdict(value string) (string, bool) {
	value = strings.ToLower(value)
	matches := responseTagPattern.FindAllStringSubmatch(value, -1)
	if len(matches) > 0 {
		return matches[len(matches)-1][1], true
	}
	match := responsePlainPattern.FindStringSubmatch(value)
	if len(match) == 2 {
		return match[1], true
	}
	return "", false
}

func neutralizeClassifierBody(value string) string {
	for {
		next := classifierWrapperPattern.ReplaceAllString(value, "")
		if next == value {
			break
		}
		value = next
	}
	value = classifierClosePattern.ReplaceAllString(value, "<\\/transcript>")
	return classifierRolePattern.ReplaceAllString(value, "[content] $1:")
}

func fillClassifications(values []memorytools.Classification, value memorytools.Classification) {
	for index := range values {
		values[index] = value
	}
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
