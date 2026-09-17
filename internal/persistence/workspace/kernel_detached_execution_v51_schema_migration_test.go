package workspace

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestKernelDetachedExecutionV51PublishedIdentity(t *testing.T) {
	checksum, err := kernelDetachedExecutionV51Migration.validatedChecksum()
	if err != nil {
		t.Fatal(err)
	}
	const published = "b626d54783fcba9a41904b5a1a11e6067d7bf7d5d38c1d8c37cb181cd470b7a4"
	if checksum != published {
		t.Fatalf("published v51 checksum changed: got %s want %s", checksum, published)
	}
}

func TestKernelDetachedExecutionV51AllowsInPlaceBackendRecreation(t *testing.T) {
	ctx := context.Background()
	store, backend, _, _ := newAcceptedDetachedKernelExecutionFixture(t)
	if _, err := store.FinishKernelExecutionBackend(ctx, FinishKernelExecutionBackendInput{
		BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		ExecutorInstanceID: backend.ExecutorInstanceID, EvidenceLost: true,
	}); err != nil {
		t.Fatal(err)
	}
	var spec KernelExecutionSessionSpecV1
	if err := json.Unmarshal([]byte(backend.SessionSpecJSON), &spec); err != nil {
		t.Fatal(err)
	}
	recreated, err := store.RecreateKernelExecutionBackend(ctx, RecreateKernelExecutionBackendInput{
		BackendID: backend.BackendID, ExecutorInstanceID: "executor-v51",
		MachineBootID: "machine-boot-v51", SocketPath: "/run/user/1000/synon-biomed/backend-v51.sock",
		KernelID: backend.KernelID, KernelGeneration: backend.KernelGeneration, SessionSpec: spec,
	})
	if err != nil || recreated.BackendGeneration != backend.BackendGeneration+1 ||
		recreated.State != KernelExecutionBackendStateStarting {
		t.Fatalf("v51 recreate=%#v err=%v", recreated, err)
	}
	// The recreation path is only valid for one terminal row at a time.
	if _, err := store.RecreateKernelExecutionBackend(ctx, RecreateKernelExecutionBackendInput{
		BackendID: backend.BackendID, ExecutorInstanceID: "executor-v51",
		MachineBootID: "machine-boot-v51", SocketPath: "/run/user/1000/synon-biomed/backend-v51.sock",
		KernelID: backend.KernelID, KernelGeneration: backend.KernelGeneration, SessionSpec: spec,
	}); err == nil {
		t.Fatal("live backend was recreated a second time")
	}
	// A raw UPDATE that mutates identity fields without the recreation
	// transition must still be rejected by the immutable trigger.
	if _, err := store.db.Exec(`UPDATE kernel_execution_backends SET state='ready',state_version=2,
		executor_instance_id=?,updated_at=?
		WHERE backend_id=?`, "executor-sneaky", time.Now().UTC().Format(time.RFC3339Nano), backend.BackendID); err == nil ||
		!strings.Contains(err.Error(), "identity is immutable") {
		t.Fatalf("identity mutation error=%v", err)
	}
	// A raw UPDATE with a transition that is not allowed from 'starting'
	// must still be rejected by the transition trigger.
	if _, err := store.db.Exec(`UPDATE kernel_execution_backends SET state='draining',state_version=2,updated_at=?
		WHERE backend_id=?`, time.Now().UTC().Format(time.RFC3339Nano), backend.BackendID); err == nil ||
		!strings.Contains(err.Error(), "transition is invalid") {
		t.Fatalf("invalid transition error=%v", err)
	}
}
