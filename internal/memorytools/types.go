package memorytools

import (
	"context"
	"errors"
	"fmt"

	"synon-go/internal/memorypolicy"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

const (
	MaxOperationsPerKind = memorypolicy.OperationsPerKindMax
	MaxReadRows          = memorypolicy.ReadToolMax
	MaxSearchRows        = memorypolicy.SearchToolMax
)

var (
	ErrMemoryUnavailable           = errors.New("unavailable")
	ErrMemoryClassifierUnavailable = errors.New("Memory classifier unavailable — write skipped (transient; retry later).")
	ErrMemoryWriteRejected         = errors.New("Memory write rejected: content flagged as potential prompt injection")
)

type Scope struct {
	UserID        string
	ProjectID     string
	FrameID       string
	SourceFrameID string
}

type Classification struct {
	Flagged bool
	Reason  string
	Pattern string
}

type Classifier interface {
	ClassifyMemoryWrites(ctx context.Context, texts []string) ([]Classification, error)
}

type ReadInput struct {
	Entity string `json:"entity"`
}

type ReadResult struct {
	Output   string             `json:"output"`
	Memories []workspace.Memory `json:"-"`
}

type AppendInput struct {
	Text     string `json:"text"`
	Evidence string `json:"evidence,omitempty"`
}

type ReplaceInput struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Evidence string `json:"evidence,omitempty"`
}

type WriteInput struct {
	Entity   string         `json:"entity,omitempty"`
	Category string         `json:"category,omitempty"`
	Append   []AppendInput  `json:"append,omitempty"`
	Replace  []ReplaceInput `json:"replace,omitempty"`
	Remove   []string       `json:"remove,omitempty"`
}

// WriteAuthority is supplied only by the trusted Transcript runner context.
// User/tool input cannot provide or override any field.
type WriteAuthority struct {
	Claim             transcriptstore.RunnerClaim
	SourceEventID     int64
	InputSHA256       string
	FreshWriteAllowed func(context.Context) (bool, error)
}

type WriteResult struct {
	Output   string   `json:"output"`
	Appended []string `json:"appended"`
	Replaced []string `json:"replaced"`
	Removed  []string `json:"removed"`
}

type SearchInput struct {
	Query string `json:"query"`
}

type SearchResult struct {
	Output          string             `json:"output"`
	ResultsReturned int                `json:"results_returned"`
	Entities        []string           `json:"-"`
	Memories        []workspace.Memory `json:"-"`
}

type ClassifiedWriteError struct {
	Reason  string
	Pattern string
}

func (e *ClassifiedWriteError) Error() string {
	detail := e.Reason
	if e.Pattern != "" {
		detail += ": " + e.Pattern
	}
	if detail == "" {
		detail = "flagged"
	}
	return fmt.Sprintf("%s (%s). Nothing was saved.", ErrMemoryWriteRejected, detail)
}

func (e *ClassifiedWriteError) Unwrap() error { return ErrMemoryWriteRejected }
