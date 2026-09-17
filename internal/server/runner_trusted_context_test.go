package server

import (
	"strings"
	"testing"
	"time"

	sessionstore "synon-go/internal/persistence/sessions"
)

func TestTrustedRuntimeContextRequiresAuditableReportReferences(t *testing.T) {
	now := time.Date(2026, 8, 25, 4, 0, 0, 0, time.UTC)
	context := sessionRunnerTrustedRuntimeContext(now, now.Add(-time.Minute), sessionstore.Session{}, nil)
	for _, marker := range []string{
		"Any user-visible report based on external evidence",
		"References or Evidence section",
		"retrievable source URLs or stable identifiers",
		"precise factual claims",
	} {
		if !strings.Contains(context, marker) {
			t.Fatalf("trusted runtime context is missing %q", marker)
		}
	}
}
