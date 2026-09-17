package detached

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

func TestLoadExecutionResultVerifiesInlineAndSpoolContent(t *testing.T) {
	now := time.Now().UTC().Round(0)
	inline := ExecutionResultV1{
		Version: 1, ExecutionID: "execution-inline", ToolCallID: "tool-inline",
		KernelID: "kernel-inline", KernelKind: "analysis", Language: "python", Environment: "python",
		Response:  kernelruntime.Response{ID: "execution-inline", Stdout: "ok\n"},
		StartedAt: now, FinishedAt: now.Add(time.Second), Generation: 1,
	}
	inlineJSON, err := json.Marshal(inline)
	if err != nil {
		t.Fatal(err)
	}
	inlineDigest := sha256.Sum256(inlineJSON)
	receipt := workspace.KernelExecutionResultReceipt{
		ExecutionID: inline.ExecutionID, ResultJSON: string(inlineJSON),
		ResultSHA256: hex.EncodeToString(inlineDigest[:]),
	}
	loaded, cleanup, err := LoadExecutionResult(t.TempDir(), receipt)
	if err != nil || cleanup != "" || loaded.Response.Stdout != "ok\n" {
		t.Fatalf("inline loaded=%#v cleanup=%q err=%v", loaded, cleanup, err)
	}

	spoolDir := t.TempDir()
	large := inline
	large.ExecutionID = "execution-spool"
	large.ToolCallID = "tool-spool"
	large.Response.ID = large.ExecutionID
	large.Response.Stdout = strings.Repeat("x", 1048577)
	encoded, err := json.Marshal(large)
	if err != nil {
		t.Fatal(err)
	}
	fullDigest := sha256.Sum256(encoded)
	fullSHA := hex.EncodeToString(fullDigest[:])
	spoolPath := filepath.Join(spoolDir, fullSHA+".json")
	if err := os.WriteFile(spoolPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	pointer, err := json.Marshal(spoolPointerV1{
		Version: 1, ExecutionID: large.ExecutionID, Kind: "kernel-spool-v1",
		SHA256: fullSHA, Bytes: len(encoded),
	})
	if err != nil {
		t.Fatal(err)
	}
	pointerDigest := sha256.Sum256(pointer)
	receipt = workspace.KernelExecutionResultReceipt{
		ExecutionID: large.ExecutionID, ResultJSON: string(pointer),
		ResultRef:    "kernel-spool-sha256:" + fullSHA,
		ResultSHA256: hex.EncodeToString(pointerDigest[:]),
	}
	loaded, cleanup, err = LoadExecutionResult(spoolDir, receipt)
	if err != nil || cleanup != spoolPath || len(loaded.Response.Stdout) != len(large.Response.Stdout) {
		t.Fatalf("spool cleanup=%q stdout=%d err=%v", cleanup, len(loaded.Response.Stdout), err)
	}
	if err := os.WriteFile(spoolPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadExecutionResult(spoolDir, receipt); err == nil {
		t.Fatal("tampered spool was accepted")
	}
}
