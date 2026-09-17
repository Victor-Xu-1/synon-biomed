package common

import (
	"strings"
	"testing"
)

func TestBuildOutboundChunksNormalizesServerMessages(t *testing.T) {
	tests := []struct {
		name       string
		message    ServerMessage
		wantKind   string
		wantText   string
		wantDone   bool
		wantChunks int
	}{
		{
			name:       "content delta",
			message:    ServerMessage{"type": "content_delta", "text": "hello from runner"},
			wantKind:   "text",
			wantText:   "hello from runner",
			wantChunks: 1,
		},
		{
			name:       "tool use",
			message:    ServerMessage{"type": "tool_use", "toolName": "Bash", "input": map[string]any{"command": "go test ./internal/server"}},
			wantKind:   "tool",
			wantText:   "go test ./internal/server",
			wantChunks: 1,
		},
		{
			name:       "permission request",
			message:    ServerMessage{"type": "permission_request", "toolName": "Bash", "requestId": "req-1", "input": map[string]any{"command": "rm -rf /tmp/example"}},
			wantKind:   "permission",
			wantText:   "req-1",
			wantChunks: 1,
		},
		{
			name:       "error",
			message:    ServerMessage{"type": "error", "message": "model failed"},
			wantKind:   "error",
			wantText:   "model failed",
			wantChunks: 1,
		},
		{
			name:       "message complete",
			message:    ServerMessage{"type": "message_complete"},
			wantKind:   "complete",
			wantDone:   true,
			wantChunks: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := BuildOutboundChunks(tt.message)
			if len(chunks) != tt.wantChunks {
				t.Fatalf("chunks = %#v", chunks)
			}
			chunk := chunks[0]
			if chunk.Kind != tt.wantKind || chunk.Complete != tt.wantDone {
				t.Fatalf("chunk = %#v", chunk)
			}
			if tt.wantText != "" && !strings.Contains(chunk.Text, tt.wantText) {
				t.Fatalf("chunk text %q missing %q", chunk.Text, tt.wantText)
			}
		})
	}
}

func TestBuildOutboundChunksSkipsEmptyAndUnknownMessages(t *testing.T) {
	for _, message := range []ServerMessage{
		{"type": "content_delta", "text": "  "},
		{"type": "content_start", "blockType": "text"},
		{"type": "unknown", "text": "ignored"},
		nil,
	} {
		if chunks := BuildOutboundChunks(message); len(chunks) != 0 {
			t.Fatalf("BuildOutboundChunks(%#v) = %#v", message, chunks)
		}
	}
}
