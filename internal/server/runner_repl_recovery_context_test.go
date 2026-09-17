package server

import (
	"strings"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestSessionRunnerREPLRecoveryContextIsResumeOnlyAndSeparatesDurableState(t *testing.T) {
	if context := sessionRunnerREPLRecoveryContext(transcriptstore.ResumeSourceFresh, 1); context != "" {
		t.Fatalf("fresh turn received recovery context: %q", context)
	}
	if context := sessionRunnerREPLRecoveryContext(transcriptstore.ResumeSourceFresh, 2); context == "" {
		t.Fatal("same-input retry did not receive recovery context")
	}
	for _, source := range []transcriptstore.ResumeSource{
		transcriptstore.ResumeSourceCheckpoint,
		transcriptstore.ResumeSourceRetry,
		transcriptstore.ResumeSourceUserInput,
	} {
		context := sessionRunnerREPLRecoveryContext(source, 1)
		for _, required := range []string{"variables", "not durable", "define every variable", "read-only", "do not replay a side-effecting call"} {
			if !strings.Contains(context, required) {
				t.Fatalf("resume source %q context missing %q: %s", source, required, context)
			}
		}
	}
}
