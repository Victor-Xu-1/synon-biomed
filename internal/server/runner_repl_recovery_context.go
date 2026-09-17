package server

import (
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

// sessionRunnerREPLRecoveryContext separates durable task evidence from
// process-local REPL memory whenever a turn resumes. A healthy same-process
// kernel may still retain variables, but the recovery contract cannot depend
// on that accident: source activation, crash recovery, and host migration are
// all allowed to replace the worker while preserving the transcript.
func sessionRunnerREPLRecoveryContext(source transcriptstore.ResumeSource, attempt int) string {
	if (source == transcriptstore.ResumeSourceFresh || strings.TrimSpace(string(source)) == "") && attempt <= 1 {
		return ""
	}
	return "REPL recovery contract: this turn resumed from a durable checkpoint. Transcript events, immutable tool results, saved files, and artifacts remain authoritative, but variables from an earlier REPL process are not durable and may no longer exist. In the first resumed REPL cell, define every variable used in that cell. Reissue at most one read-only source or MCP call when its result is needed again, or reconstruct from an exact durable file/result; do not reference a prior variable merely because its old stdout is visible, and do not replay a side-effecting call."
}
