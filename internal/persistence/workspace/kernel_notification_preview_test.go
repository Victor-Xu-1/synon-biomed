package workspace

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestKernelNotificationPreviewBoundsEncodedTransport(t *testing.T) {
	for _, text := range []string{
		strings.Repeat("x", 2<<20),
		strings.Repeat("研究🧬", 100000),
		strings.Repeat("\x00\n\t\\\"<>&", 100000),
	} {
		raw, err := json.Marshal(map[string]any{"exit_status": "ok", "stdout": text})
		if err != nil {
			t.Fatal(err)
		}
		original := bytes.Clone(raw)
		payload, err := BuildKernelSettlementNotificationPayload("execution", "tool", raw, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(payload)
		if err != nil || len(encoded) >= 256<<10 {
			t.Fatalf("encoded notification bytes=%d err=%v", len(encoded), err)
		}
		output := payload["output"].(string)
		if !utf8.ValidString(output) || !strings.HasPrefix(string(raw), output) ||
			payload["output_truncated_from_chars"] != utf8.RuneCount(raw) {
			t.Fatal("preview must retain a valid prefix and report original character count")
		}
		if !bytes.Equal(raw, original) {
			t.Fatal("notification projection changed the authoritative result")
		}
	}
}

func TestKernelNotificationPreviewPreservesSmallResultsAndRequestedLimit(t *testing.T) {
	raw := []byte(`{"exit_status":"ok","stdout":"研究"}`)
	payload, err := BuildKernelSettlementNotificationPayload("execution", "tool", raw, 4096)
	if err != nil || payload["output"] != string(raw) || payload["output_truncated_from_chars"] != nil {
		t.Fatalf("small result changed: err=%v", err)
	}
	payload, err = BuildKernelSettlementNotificationPayload("execution", "tool", raw, 12)
	if err != nil || len(payload["output"].(string)) > 12 || payload["output_truncated_from_chars"] == nil {
		t.Fatalf("caller output limit not honored: err=%v", err)
	}
}
