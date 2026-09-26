package server

import (
	"fmt"
	"log"
	"time"
	"unicode"
)

const sessionRunnerCommunicationAuditNamespace = "session-runner-communication-audit"

// These constant-space counters describe the response boundary, not correctness.
// Repetition can be intentional (data, sequences, quotations); it must never
// become a task verdict, retry trigger, or a reason to retain the underlying text.
func addSessionRunnerNarrationCounters(record map[string]any, prefix, text string) {
	units, longest, run, start := 0, 0, 0, -1
	previous := ""
	finish := func(end int) {
		if start < 0 {
			return
		}
		unit := text[start:end]
		units++
		if unit == previous {
			run++
		} else {
			run = 1
		}
		if run > longest {
			longest = run
		}
		previous, start = unit, -1
	}
	for offset, value := range text {
		if unicode.IsSpace(value) {
			finish(offset)
		} else if start < 0 {
			start = offset
		}
	}
	finish(len(text))
	record[prefix+"whitespace_units"] = units
	record[prefix+"longest_equal_unit_run"] = longest
}

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
