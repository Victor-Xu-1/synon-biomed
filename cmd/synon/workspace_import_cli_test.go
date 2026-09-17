package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/workspaceimport"
)

func TestWorkspaceImportCLIInspectEmitsRedactedDeterministicPlan(t *testing.T) {
	database := filepath.Join(t.TempDir(), "workspace.sqlite")
	store, err := workspace.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runWorkspaceImportCLI(context.Background(), []string{"inspect", "--source", database}, &output); err != nil {
		t.Fatal(err)
	}
	var plan workspaceimport.Plan
	if err := json.Unmarshal(output.Bytes(), &plan); err != nil {
		t.Fatalf("decode plan: %v output=%s", err, output.String())
	}
	if len(plan.Sources) != 1 || plan.Sources[0].DatabasePath != "" || plan.PlanSHA256 == "" {
		t.Fatalf("plan=%#v", plan)
	}
	if strings.Contains(output.String(), "local") || strings.Contains(output.String(), "@") {
		t.Fatalf("default plan leaked owner identity: %s", output.String())
	}
}

func TestWorkspaceImportCLIPlanPrintsEvidenceBeforeFailingClosed(t *testing.T) {
	database := filepath.Join(t.TempDir(), "workspace.sqlite")
	store, err := workspace.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = runWorkspaceImportCLI(context.Background(), []string{
		"plan", "--source", database, "--target-home", t.TempDir(), "--include-paths",
	}, &output)
	if err == nil || !strings.Contains(err.Error(), "not ready for staged apply") {
		t.Fatalf("error=%v output=%s", err, output.String())
	}
	if !strings.Contains(output.String(), database) || !strings.Contains(output.String(), "target_collision_analysis_pending") {
		t.Fatalf("output=%s", output.String())
	}
}

func TestWorkspaceImportCLIRejectsImplicitWritesAndMissingSource(t *testing.T) {
	for _, args := range [][]string{{}, {"apply"}, {"inspect"}, {"plan", "--source", "missing"}} {
		var output bytes.Buffer
		if err := runWorkspaceImportCLI(context.Background(), args, &output); err == nil {
			t.Fatalf("args=%#v unexpectedly succeeded", args)
		}
		if output.Len() != 0 {
			t.Fatalf("args=%#v wrote partial output %q", args, output.String())
		}
	}
}
