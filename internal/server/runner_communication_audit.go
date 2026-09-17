package server

import (
	"fmt"
	"log"
	"time"
)

const sessionRunnerCommunicationAuditNamespace = "session-runner-communication-audit"

// Store counters and typed decisions only: no prompts, tool payloads, hidden
// reasoning, response bodies or credentials belong in communication diagnostics.
func (s *Server) recordSessionRunnerCommunicationAudit(run *sessionRunnerChatRun, record map[string]any) {
	if s == nil || s.runtimeStore == nil || run == nil {
		return
	}
	record["session_id"] = run.SessionID
	record["attempt"] = sessionRunnerAttempt(run)
	record["recorded_at"] = time.Now().UTC()
	key := fmt.Sprintf("%s-%d-%d", runtimeKeyFromSessionID(run.SessionID), sessionRunnerAttempt(run), time.Now().UTC().UnixNano())
	if _, err := s.runtimeStore.Set(sessionRunnerCommunicationAuditNamespace, key, record); err != nil {
		log.Printf("communication audit persistence failed session=%q", run.SessionID)
	}
}
