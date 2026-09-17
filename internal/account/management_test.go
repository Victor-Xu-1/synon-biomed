package account

import "testing"

func TestManagementSnapshotValidateRequiresStableContractAndCollections(t *testing.T) {
	snapshot := ManagementSnapshot{
		SchemaVersion:     ManagementSchemaVersion,
		ControlPlane:      ControlPlane{Mode: "local-runtime", PolicySource: "local-runtime", DataResidency: "local"},
		ControlBoundaries: []ControlBoundary{{ID: "identity", Status: "active", Source: "local-runtime"}},
		RuntimePolicy:     RuntimePolicy{Source: "local-runtime", ApprovalMode: "confirm", NetworkMode: "default", MCPBoundary: "unavailable", PermissionProfile: "local-workspace"},
		Account:           Account{ID: "account-1", Username: "scientist"},
		LoginMethods:      []LoginMethod{}, Workspaces: []Workspace{}, Entitlements: []Entitlement{},
		Sessions: []Session{}, SecurityEvents: []SecurityEvent{},
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}

	invalid := snapshot
	invalid.SchemaVersion++
	if err := invalid.Validate(); err == nil {
		t.Fatal("unknown schema version was accepted")
	}
	invalid = snapshot
	invalid.Sessions = nil
	if err := invalid.Validate(); err == nil {
		t.Fatal("nil collection was accepted")
	}
}
