package server

import (
	"fmt"
	"log"
	"strings"
	"time"

	kernelruntime "synon-go/internal/kernel"
)

// bindKernelStdoutRealtime owns the kernel-to-conversation streaming adapter.
// The composition root calls it only for the long-lived service: short-lived
// embedded Servers may share a Manager and must not leave it pointing at a
// closed session store after they exit.
func (s *Server) bindKernelStdoutRealtime(manager *kernelruntime.Manager) {
	if s == nil || manager == nil {
		return
	}
	manager.SetStdoutObserver(func(chunk kernelruntime.ExecStdoutChunk) {
		if strings.TrimSpace(chunk.FrameID) == "" || strings.TrimSpace(chunk.Chunk) == "" {
			return
		}
		message := map[string]any{
			"type": "tool_stdout", "exec_id": chunk.ExecID, "tool_use_id": chunk.ToolUseID,
			"tool_name": chunk.ToolName, "chunk": chunk.Chunk, "chunk_sequence": chunk.Sequence,
			"chunk_start_byte": chunk.StartByte, "chunk_end_byte": chunk.EndByte,
			"root_frame_id": chunk.RootFrameID, "background": chunk.Background,
		}
		if !chunk.StartedAt.IsZero() {
			message["started_at"] = chunk.StartedAt.UTC().Format(time.RFC3339Nano)
		}
		if strings.TrimSpace(chunk.Status) != "" {
			message["status"] = chunk.Status
		}
		_, err := s.appendSessionToolEvent(map[string]any{
			"sessionId": chunk.FrameID, "role": "assistant",
			"clientMessageId": fmt.Sprintf("kernel-stdout:%s:%d", chunk.ExecID, chunk.Sequence),
			"message":         message,
		})
		if err != nil && !strings.Contains(err.Error(), "session not found") {
			log.Printf("kernel stdout realtime publication failed: %T", err)
		}
	})
}
