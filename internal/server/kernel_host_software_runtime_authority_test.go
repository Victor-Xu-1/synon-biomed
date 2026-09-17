package server

import (
	"context"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestTaskKernelExposesRemoteComputeSeparatelyFromManagedSoftware(t *testing.T) {
	server := &Server{}
	policy := server.agentKernelHostCallPolicy(
		context.Background(),
		workspace.KernelFrameAccess{},
		"",
		[]string{"repl"},
		false,
	)
	if policy == nil {
		t.Fatal("expected a kernel host-call policy")
	}
	want := map[string]bool{"host.compute.create": false, "host.compute.status": false, "host.compute.ledger": false}
	for _, method := range policy.AllowedMethods {
		if _, tracked := want[method]; tracked {
			want[method] = true
		}
	}
	for method, found := range want {
		if !found {
			t.Fatalf("remote compute authority %q is missing", method)
		}
	}
}
