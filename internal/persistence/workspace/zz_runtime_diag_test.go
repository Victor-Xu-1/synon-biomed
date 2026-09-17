package workspace

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"synon-go/internal/software"
	"synon-go/internal/software/localconda"

	_ "modernc.org/sqlite"
)

func TestRuntimeDiagCanonicalDetachedSoftwareRequest(t *testing.T) {
	databasePath := os.Getenv("SYNON_RUNTIME_DIAG_DB")
	operationID := os.Getenv("SYNON_RUNTIME_DIAG_OPERATION")
	if databasePath == "" || operationID == "" {
		t.Skip("runtime diagnostic fixture is not configured")
	}
	db, err := sql.Open("sqlite", "file:"+databasePath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	operation, found, err := getKernelLocalOperationQuery(ctx, db, operationID)
	if err != nil || !found {
		t.Fatalf("operation found=%t err=%v", found, err)
	}
	var backendID string
	if err := db.QueryRowContext(ctx, `SELECT backend_id FROM kernel_execution_backends WHERE frame_id=? ORDER BY created_at DESC LIMIT 1`, operation.FrameID).Scan(&backendID); err != nil {
		t.Fatal(err)
	}
	backend, found, err := getKernelExecutionBackendQuery(ctx, db, backendID)
	if err != nil || !found {
		t.Fatalf("backend found=%t err=%v", found, err)
	}
	session, err := DecodeKernelExecutionSessionSpecV1(backend.SessionSpecJSON)
	if err != nil {
		t.Fatal(err)
	}
	request, err := software.DecodeRequestJSON(operation.InputJSON)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := software.RequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	harness, err := localconda.BuildPythonHarness(software.Plan{
		ProviderID: software.LocalProviderID, Request: request, RequestDigest: digest, Environment: operation.Environment,
	}, software.ProvisionReceipt{
		ProviderID: software.LocalProviderID, Environment: operation.Environment, Generation: session.RuntimeGeneration,
		Executable: request.Executable, Local: true, Verified: true, Preflight: true,
		Disposition: software.ProvisionDispositionInstalled,
	})
	if err != nil {
		t.Fatal(err)
	}
	operation.State = KernelLocalOperationStateStarted
	operation.ExecutionID = "execution-runtime-diag"
	operation.KernelID = backend.KernelID
	operation.KernelGeneration = backend.KernelGeneration
	detached := KernelDetachedExecutionRequestV1{
		Version: 1, OperationID: operation.OperationID, ExecutionID: operation.ExecutionID,
		OwnerUserID: operation.OwnerUserID, ProjectID: operation.ProjectID,
		RootFrameID: operation.RootFrameID, RootFrameIncarnationID: operation.RootFrameIncarnationID,
		FrameID: operation.FrameID, FrameIncarnationID: operation.FrameIncarnationID,
		KernelID: operation.KernelID, KernelGeneration: int64(operation.KernelGeneration),
		ToolCallID: operation.ToolCallID, ToolName: operation.Tool,
		Language: session.Language, KernelKind: session.KernelKind, Environment: operation.Environment,
		Code: harness, WorkingDir: session.WorkspaceDir, Background: request.Background,
		TimeoutMillis:    (time.Duration(request.TimeoutSeconds)*time.Second + 30*time.Second).Milliseconds(),
		OutputLimitBytes: 128 * 1024, Origin: "agent",
	}
	canonical, encoded, _, err := canonicalKernelDetachedExecutionRequest(operation, backend, detached)
	if err != nil {
		t.Fatal(err)
	}
	if canonical.ExecutionID != operation.ExecutionID || encoded == "" {
		t.Fatalf("canonical detached request is incomplete: %#v", canonical)
	}
	if _, err := DecodeKernelDetachedExecutionRequestV1(encoded); err != nil {
		t.Fatal(err)
	}
}
