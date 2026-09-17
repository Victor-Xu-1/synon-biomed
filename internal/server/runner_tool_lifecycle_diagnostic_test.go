package server

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
)

func TestToolLifecycleInterruptionDiagnosticContainsTypesNotValues(t *testing.T) {
	secret := errors.New("private tool input must not appear")
	detail := (sessionRunnerToolLifecyclePersistenceInterruption{
		eventType: agentruntime.EventToolPaused,
		cause:     fmt.Errorf("persist waiting checkpoint: %w", secret),
	}).diagnosticDetail()
	if strings.Contains(detail, "private tool input") {
		t.Fatalf("diagnostic leaked cause text: %q", detail)
	}
	for _, marker := range []string{"event_type=tool_paused", "cause_types=*fmt.wrapError>*errors.errorString"} {
		if !strings.Contains(detail, marker) {
			t.Fatalf("diagnostic %q missing %q", detail, marker)
		}
	}
}
