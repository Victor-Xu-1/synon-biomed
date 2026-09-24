package kernel

import "testing"

func TestConfinementDiagnosticsAreStableAndClosed(t *testing.T) {
	for _, fixture := range []struct {
		evidence    ConfinementEvidence
		wantCode    string
		wantMessage string
	}{
		{ConfinementEvidence{Available: true, Mode: "ready"}, "", ""},
		{ConfinementEvidence{Reason: "bubblewrap is unavailable"}, "kernel_confinement_unavailable", "kernel process confinement is unavailable"},
		{ConfinementEvidence{Reason: darwinConfinementUnverifiedReason}, "kernel_confinement_unavailable", "kernel process confinement is unavailable"},
		{ConfinementEvidence{Reason: "Synon kernel confinement is unavailable on this platform"}, "kernel_confinement_unsupported", "kernel process confinement is unsupported on this platform"},
		{ConfinementEvidence{Reason: "private path /secret/bwrap failed"}, "kernel_confinement_probe_failed", "kernel process confinement verification failed"},
	} {
		diagnostic := DiagnoseConfinementEvidence(fixture.evidence)
		if diagnostic.Code != fixture.wantCode || diagnostic.Message != fixture.wantMessage {
			t.Fatalf("evidence=%#v diagnostic=%#v", fixture.evidence, diagnostic)
		}
	}
}
