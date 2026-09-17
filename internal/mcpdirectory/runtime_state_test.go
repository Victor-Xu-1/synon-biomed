package mcpdirectory

import (
	"testing"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

func TestBundledRuntimeStateFreshnessRequiresCurrentServiceProbe(t *testing.T) {
	startedAt := time.Date(2026, time.August, 12, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		state     workspace.MCPConnectorRuntimeState
		wantFresh bool
		wantProbe bool
	}{
		{
			name:      "missing timestamp",
			state:     workspace.MCPConnectorRuntimeState{Enabled: true},
			wantProbe: true,
		},
		{
			name:      "previous service snapshot",
			state:     workspace.MCPConnectorRuntimeState{Enabled: true, UpdatedAt: startedAt.Add(-time.Second)},
			wantProbe: true,
		},
		{
			name:      "current service snapshot",
			state:     workspace.MCPConnectorRuntimeState{Enabled: true, UpdatedAt: startedAt},
			wantFresh: true,
		},
		{
			name:  "disabled snapshot stays explicit",
			state: workspace.MCPConnectorRuntimeState{Enabled: false, UpdatedAt: startedAt.Add(-time.Hour)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bundledRuntimeStateIsFresh(tt.state, startedAt); got != tt.wantFresh {
				t.Fatalf("fresh=%t, want %t", got, tt.wantFresh)
			}
			if got := bundledRuntimeStateNeedsProbe(tt.state, startedAt); got != tt.wantProbe {
				t.Fatalf("needsProbe=%t, want %t", got, tt.wantProbe)
			}
		})
	}
}
