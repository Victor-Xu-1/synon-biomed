package common

import (
	"strings"
	"testing"
)

func TestSplitMessageUsesNaturalBoundaries(t *testing.T) {
	text := "First paragraph.\n\nSecond paragraph."
	chunks := SplitMessage(text, 20)
	if len(chunks) < 2 {
		t.Fatalf("len(chunks) = %d, want multiple chunks", len(chunks))
	}
	joined := strings.Join(chunks, " ")
	if !strings.Contains(joined, "First paragraph") || !strings.Contains(joined, "Second paragraph") {
		t.Fatalf("joined chunks lost content: %q", joined)
	}
}

func TestSplitMessageHardSplitsWithoutNaturalBreak(t *testing.T) {
	chunks := SplitMessage(strings.Repeat("a", 50), 20)
	if len(chunks) != 3 {
		t.Fatalf("len(chunks) = %d, want 3", len(chunks))
	}
	for _, chunk := range chunks {
		if len(chunk) > 20 {
			t.Fatalf("chunk %q exceeds limit", chunk)
		}
	}
}

func TestEscapeMarkdownV2(t *testing.T) {
	got := EscapeMarkdownV2("hello_world test.md a*b*c")
	want := "hello\\_world test\\.md a\\*b\\*c"
	if got != want {
		t.Fatalf("EscapeMarkdownV2() = %q, want %q", got, want)
	}
}

func TestFormatToolUseAndPermissionRequest(t *testing.T) {
	tool := FormatToolUse("Bash", map[string]any{"command": "go test ./..."})
	if !strings.Contains(tool, "Bash") || !strings.Contains(tool, "go test ./...") {
		t.Fatalf("FormatToolUse() = %q", tool)
	}

	permission := FormatPermissionRequest("Bash", map[string]any{"command": "rm -rf /tmp/synon"}, "req-1")
	for _, part := range []string{"Bash", "req-1", "rm -rf"} {
		if !strings.Contains(permission, part) {
			t.Fatalf("FormatPermissionRequest() missing %q in %q", part, permission)
		}
	}
}

func TestTruncateInput(t *testing.T) {
	if got := TruncateInput("hello", 100); got != "hello" {
		t.Fatalf("TruncateInput(short) = %q", got)
	}

	got := TruncateInput(strings.Repeat("x", 300), 100)
	if len(got) != 103 || !strings.HasSuffix(got, "...") {
		t.Fatalf("TruncateInput(long) = %q length %d", got, len(got))
	}

	object := TruncateInput(map[string]any{"key": "value"}, 100)
	if !strings.Contains(object, "key") || !strings.Contains(object, "value") {
		t.Fatalf("TruncateInput(object) = %q", object)
	}

	if got := TruncateInput(make(chan int), 100); got != "(unserializable)" {
		t.Fatalf("TruncateInput(unserializable) = %q", got)
	}
}
