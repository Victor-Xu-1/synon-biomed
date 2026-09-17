package server

import (
	"testing"

	runtimekv "synon-go/internal/persistence/runtimekv"
)

func TestAggregateFrameTaskModelUsageScopesCurrentTaskAttempts(t *testing.T) {
	entries := []runtimekv.Entry{
		{Value: map[string]any{
			"sessionId": "frame-live", "attempt": float64(12), "runtimeRole": "agent",
			"promptTokens": float64(86_096), "completionTokens": float64(142),
			"cacheReadTokens": float64(4_096), "cacheWriteTokens": float64(0), "totalTokens": float64(86_238),
		}},
		{Value: map[string]any{
			"sessionId": "frame-live", "attempt": float64(12), "runtimeRole": "agent",
			"promptTokens": float64(86_397), "completionTokens": float64(156),
			"cacheReadTokens": float64(86_016), "cacheWriteTokens": float64(0), "totalTokens": float64(86_553),
		}},
		{Value: map[string]any{
			"sessionId": "frame-live", "attempt": float64(11),
			"promptTokens": float64(9_999), "completionTokens": float64(999), "totalTokens": float64(10_998),
		}},
		{Value: map[string]any{
			"sessionId": "another-frame", "attempt": float64(12),
			"promptTokens": float64(8_888), "completionTokens": float64(888), "totalTokens": float64(9_776),
		}},
	}

	usage, err := aggregateFrameTaskModelUsage(entries, "frame-live", []int64{12})
	if err != nil {
		t.Fatal(err)
	}
	if usage.ModelCallCount != 2 || !usage.UsageAvailable || usage.InputTokens != 172_493 ||
		usage.OutputTokens != 298 || usage.CacheReadTokens != 90_112 || usage.CacheWriteTokens != 0 ||
		usage.TotalTokens != 172_791 {
		t.Fatalf("usage=%#v", usage)
	}

	total, err := aggregateFrameTaskModelUsage(entries, "frame-live", []int64{11, 12})
	if err != nil {
		t.Fatal(err)
	}
	if total.ModelCallCount != 3 || !total.UsageAvailable || total.InputTokens != 182_492 ||
		total.OutputTokens != 1_297 || total.CacheReadTokens != 90_112 || total.CacheWriteTokens != 0 ||
		total.TotalTokens != 183_789 {
		t.Fatalf("total usage=%#v", total)
	}
}

func TestAggregateFrameTaskModelUsageKeepsMissingUsageUnavailable(t *testing.T) {
	entries := []runtimekv.Entry{{Value: map[string]any{
		"sessionId": "frame-error", "attempt": float64(3), "runtimeRole": "agent", "error": "provider unavailable",
	}}}

	usage, err := aggregateFrameTaskModelUsage(entries, "frame-error", []int64{3})
	if err != nil {
		t.Fatal(err)
	}
	if usage.ModelCallCount != 1 || usage.UsageAvailable {
		t.Fatalf("usage=%#v", usage)
	}
}

func TestAggregateFrameTaskModelUsageRejectsCorruptMatchingUsage(t *testing.T) {
	entries := []runtimekv.Entry{{Value: map[string]any{
		"sessionId": "frame-corrupt", "attempt": float64(4), "promptTokens": float64(1.5),
	}}}

	if _, err := aggregateFrameTaskModelUsage(entries, "frame-corrupt", []int64{4}); err == nil {
		t.Fatal("expected corrupt matching token usage to fail")
	}
}
