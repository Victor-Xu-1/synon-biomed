package memoryextract

import (
	"context"
	"strings"
	"testing"

	"synon-go/internal/memorypolicy"
)

type repairModelFunc func(context.Context, RepairRequest) (string, error)

func (function repairModelFunc) RepairMemoryText(ctx context.Context, request RepairRequest) (string, error) {
	return function(ctx, request)
}

func TestWorkspaceLiteralRepairerRetriesWithExactValueFeedback(t *testing.T) {
	requests := make([]RepairRequest, 0, 2)
	repairer := NewModelLiteralRepairer(repairModelFunc(func(_ context.Context, request RepairRequest) (string, error) {
		requests = append(requests, request)
		if len(requests) == 1 {
			return "Use v4.0.3.", nil
		}
		return "Use v4.0.3 (was v4.0.2) at /opt/synon/config.json.", nil
	}))
	result, err := repairer.RepairMemoryReplacement(context.Background(), "Use v4.0.2 at /opt/synon/config.json.", "Use v4.0.3.")
	if err != nil {
		t.Fatal(err)
	}
	if result != "Use v4.0.3 (was v4.0.2) at /opt/synon/config.json." || len(requests) != 2 {
		t.Fatalf("repair result=%q requests=%d", result, len(requests))
	}
	if requests[0].Model != "" || requests[0].MaxTokens != memorypolicy.LiteralRepairMaxTokens || requests[0].Temperature != 0 {
		t.Fatalf("repair request = %#v", requests[0])
	}
	if !strings.Contains(requests[1].Prompt, "Your previous attempt dropped these values") || !strings.Contains(requests[1].Prompt, "/opt/synon/config.json") {
		t.Fatalf("retry prompt = %q", requests[1].Prompt)
	}
}

func TestWorkspaceLiteralRepairerSkipsModelWhenNoLiteralIsLost(t *testing.T) {
	calls := 0
	repairer := NewModelLiteralRepairer(repairModelFunc(func(context.Context, RepairRequest) (string, error) {
		calls++
		return "", nil
	}))
	result, err := repairer.RepairMemoryReplacement(context.Background(), "Stable preference", "Updated stable preference")
	if err != nil || result != "Updated stable preference" || calls != 0 {
		t.Fatalf("repair result=%q calls=%d err=%v", result, calls, err)
	}
}

func TestWorkspaceLiteralRepairerRejectsInventedValuesAfterTwoAttempts(t *testing.T) {
	repairer := NewModelLiteralRepairer(repairModelFunc(func(context.Context, RepairRequest) (string, error) {
		return "Use v4.0.3 (was v4.0.2) at /opt/synon/config.json build 987654.", nil
	}))
	if _, err := repairer.RepairMemoryReplacement(context.Background(), "Use v4.0.2 at /opt/synon/config.json.", "Use v4.0.3."); err == nil || !strings.Contains(err.Error(), ErrLiteralRepairFailed.Error()) {
		t.Fatalf("invented literal repair error = %v", err)
	}
}

func TestWorkspaceLiteralRepairerPreservesValuesIntroducedByTheUpdate(t *testing.T) {
	requests := make([]RepairRequest, 0, 2)
	repairer := NewModelLiteralRepairer(repairModelFunc(func(_ context.Context, request RepairRequest) (string, error) {
		requests = append(requests, request)
		if len(requests) == 1 {
			return "Use build 654321 (was 123456).", nil
		}
		return "Use build 654321 (was 123456) with /new/path/2.", nil
	}))
	result, err := repairer.RepairMemoryReplacement(
		context.Background(),
		"Use build 123456.",
		"Use build 654321 with /new/path/2.",
	)
	if err != nil {
		t.Fatal(err)
	}
	if result != "Use build 654321 (was 123456) with /new/path/2." || len(requests) != 2 {
		t.Fatalf("repair result=%q requests=%d", result, len(requests))
	}
	if !strings.Contains(requests[1].Prompt, "Your previous attempt dropped these values") || !strings.Contains(requests[1].Prompt, "/new/path/2") {
		t.Fatalf("updated-literal retry prompt = %q", requests[1].Prompt)
	}
}
