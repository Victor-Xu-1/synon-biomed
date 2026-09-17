package server

import (
	"context"
	"testing"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

// Supervision fixtures exercise real child execution against a controlled local
// provider. Optional public discovery is covered by connector tests and must not
// consume this fixture's deadline before the provider receives its first call.
func newKernelHostSupervisionTestRuntime(t *testing.T, databasePath string, seed bool) (*workspace.Store, *kernelruntime.Manager, *Server, *agentKernelContext) {
	t.Helper()
	store, manager, app, identity := newKernelHostTestRuntime(t, databasePath, seed)
	connectors, err := app.mcpDirectory.ListUnifiedConnectors(context.Background(), identity.access.UserID)
	if err != nil {
		t.Error(err)
		closeKernelHostTestRuntime(t, app, manager, store)
		t.FailNow()
	}
	for _, connector := range connectors {
		if connector.Source != "bundled" {
			continue
		}
		if _, err := app.mcpDirectory.SetUnifiedEnabled(context.Background(), identity.access.UserID, connector.ID, false); err != nil {
			t.Error(err)
			closeKernelHostTestRuntime(t, app, manager, store)
			t.FailNow()
		}
	}
	return store, manager, app, identity
}
