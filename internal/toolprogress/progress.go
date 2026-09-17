package toolprogress

import (
	"context"
	"math"
	"strings"
)

// Update is an observed progress fact from a running tool. Percent values are
// intentionally optional: callers must leave them unset when the underlying
// process does not expose a determinate total. CompletedItems/TotalItems refer
// to known workflow milestones, while PhasePercent refers only to the current
// phase (for example a package download reported by the installer).
type Update struct {
	Phase          string
	Message        string
	PhasePercent   *float64
	BytesPerSecond *float64
	BytesCompleted *int64
	BytesTotal     *int64
	CompletedItems *int64
	TotalItems     *int64
	Indeterminate  bool
}

type reporterKey struct{}

// Reporter receives best-effort progress observations. It must return
// promptly and must never control whether the underlying tool continues.
type Reporter func(Update)

func WithReporter(ctx context.Context, reporter Reporter) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if reporter == nil {
		return ctx
	}
	return context.WithValue(ctx, reporterKey{}, reporter)
}

func ReporterFrom(ctx context.Context) Reporter {
	if ctx == nil {
		return nil
	}
	reporter, _ := ctx.Value(reporterKey{}).(Reporter)
	return reporter
}

func Report(ctx context.Context, update Update) {
	reporter := ReporterFrom(ctx)
	if reporter == nil {
		return
	}
	update = Normalize(update)
	if update.Phase == "" && update.Message == "" && update.PhasePercent == nil &&
		update.BytesPerSecond == nil && update.BytesCompleted == nil && update.BytesTotal == nil &&
		update.CompletedItems == nil && update.TotalItems == nil {
		return
	}
	reporter(update)
}

func Normalize(update Update) Update {
	update.Phase = boundedText(update.Phase, 80)
	update.Message = boundedText(update.Message, 240)
	update.PhasePercent = normalizedPercent(update.PhasePercent)
	if update.BytesPerSecond != nil &&
		(math.IsNaN(*update.BytesPerSecond) || math.IsInf(*update.BytesPerSecond, 0) || *update.BytesPerSecond < 0) {
		update.BytesPerSecond = nil
	}
	if update.BytesCompleted != nil && *update.BytesCompleted < 0 {
		update.BytesCompleted = nil
	}
	if update.BytesTotal != nil && *update.BytesTotal <= 0 {
		update.BytesTotal = nil
	}
	if update.BytesCompleted != nil && update.BytesTotal != nil && *update.BytesCompleted > *update.BytesTotal {
		completed := *update.BytesTotal
		update.BytesCompleted = &completed
	}
	if update.CompletedItems != nil && *update.CompletedItems < 0 {
		update.CompletedItems = nil
	}
	if update.TotalItems != nil && *update.TotalItems <= 0 {
		update.TotalItems = nil
	}
	if update.CompletedItems != nil && update.TotalItems != nil && *update.CompletedItems > *update.TotalItems {
		completed := *update.TotalItems
		update.CompletedItems = &completed
	}
	return update
}

func Merge(current, incoming Update) Update {
	incoming = Normalize(incoming)
	merged := current
	if incoming.Phase != "" {
		if incoming.Phase != current.Phase && incoming.PhasePercent == nil {
			merged.PhasePercent = nil
		}
		if incoming.Phase != current.Phase && incoming.Message == "" {
			merged.Message = ""
		}
		if incoming.Phase != current.Phase && incoming.BytesPerSecond == nil {
			merged.BytesPerSecond = nil
		}
		if incoming.Phase != current.Phase && incoming.BytesCompleted == nil && incoming.BytesTotal == nil {
			merged.BytesCompleted = nil
			merged.BytesTotal = nil
		}
		merged.Phase = incoming.Phase
	}
	if incoming.Message != "" {
		merged.Message = incoming.Message
	}
	if incoming.PhasePercent != nil {
		value := *incoming.PhasePercent
		merged.PhasePercent = &value
	}
	if incoming.BytesPerSecond != nil {
		value := *incoming.BytesPerSecond
		merged.BytesPerSecond = &value
	}
	if incoming.BytesCompleted != nil {
		value := *incoming.BytesCompleted
		merged.BytesCompleted = &value
	}
	if incoming.BytesTotal != nil {
		value := *incoming.BytesTotal
		merged.BytesTotal = &value
	}
	if incoming.CompletedItems != nil {
		value := *incoming.CompletedItems
		merged.CompletedItems = &value
	}
	if incoming.TotalItems != nil {
		value := *incoming.TotalItems
		merged.TotalItems = &value
	}
	merged.Indeterminate = incoming.Indeterminate
	return Normalize(merged)
}

func Clone(update Update) Update {
	return Merge(Update{}, update)
}

func boundedText(value string, limit int) string {
	value = strings.TrimSpace(strings.Join(strings.Fields(value), " "))
	characters := []rune(value)
	if len(characters) > limit {
		value = string(characters[:limit])
	}
	return value
}

func normalizedPercent(value *float64) *float64 {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 || *value > 100 {
		return nil
	}
	normalized := *value
	return &normalized
}
