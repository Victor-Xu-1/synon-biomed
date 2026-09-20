package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"synon-go/internal/runtimecontrol"
)

func TestAgentSaveArtifactsLargeOrdinaryBytesPersistWithoutCheckpointFlag(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	const size = int64(51 << 20)
	const relativePath = "measured-output.bin"
	file, err := os.Create(filepath.Join(fixture.projectPath, relativePath))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("canonical-tail"), size-14); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"files": []any{relativePath}, "language": "text", "human_description": "Saving measured output"}
	result, err := fixture.server.executeAgentSaveArtifacts(fixture.toolContext(t, "save-large-ordinary", input), fixture.identity, "save-large-ordinary", input)
	if err != nil {
		t.Fatalf("legitimate bytes rejected by artificial cap: result=%v err=%v", result, err)
	}
	artifacts := agentSaveArtifactResults(t, result)
	if len(artifacts) != 1 || artifacts[0]["size_bytes"] != size || artifacts[0]["is_checkpoint"] == true {
		t.Fatalf("large ordinary result=%v", result)
	}
	_, version, reader, found, err := fixture.store.OpenArtifactVersionContent(stringValue(artifacts[0]["version_id"]))
	if err != nil || !found {
		t.Fatalf("persisted artifact missing: %v", err)
	}
	defer reader.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, reader)
	if err != nil || n != size || version.ContentSHA256 != hex.EncodeToString(hash.Sum(nil)) {
		t.Fatalf("persisted bytes/digest mismatch size=%d err=%v", n, err)
	}
	original, err := os.Open(filepath.Join(fixture.projectPath, relativePath))
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	originalHash := sha256.New()
	if _, err := io.Copy(originalHash, original); err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(originalHash.Sum(nil)) != version.ContentSHA256 {
		t.Fatal("persisted artifact differs from full source")
	}
}

type artifactCapacityWriterFunc func([]byte) (int, error)

func (write artifactCapacityWriterFunc) Write(data []byte) (int, error) { return write(data) }

func TestAgentSaveArtifactSourceMutationCleansIncompleteSnapshot(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	const relative = "changing.bin"
	writeAgentSaveArtifactsFile(t, fixture.projectPath, relative, strings.Repeat("x", 64<<10))
	source, err := fixture.server.resolveAgentSavedArtifactSource(fixture.identity.access, fixture.projectPath, relative, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer source.close()
	temporary := t.TempDir()
	t.Setenv("TMPDIR", temporary)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		_, _ = runtimecontrol.GuardDiskWrites(context.Background(), artifactCapacityWriterFunc(func(data []byte) (int, error) { close(entered); <-release; return len(data), nil }), func(int64) error { return nil }).Write([]byte("x"))
		close(finished)
	}()
	<-entered
	released := false
	defer func() {
		if !released {
			close(release)
		}
		<-finished
	}()
	result := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		snapshot, _, _, err := snapshotAgentSavedArtifact(ctx, source)
		if snapshot != nil {
			_ = snapshot.Close()
			_ = os.Remove(snapshot.Name())
		}
		result <- err
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	waiting := true
	for waiting {
		select {
		case <-deadline.C:
			cancel()
			t.Fatal("snapshot did not reach guarded copy")
		case <-ticker.C:
			entries, err := os.ReadDir(temporary)
			if err != nil {
				t.Fatal(err)
			}
			waiting = len(entries) == 0
		}
	}
	if err := os.Truncate(filepath.Join(fixture.projectPath, relative), 8); err != nil {
		t.Fatal(err)
	}
	close(release)
	released = true
	if err := <-result; !errors.Is(err, errAgentSavedArtifactSourceChanged) {
		t.Fatalf("mutation was accepted: %v", err)
	}
	entries, err := os.ReadDir(temporary)
	if err != nil || len(entries) != 0 {
		t.Fatalf("incomplete snapshot leaked: %v err=%v", entries, err)
	}
}

func TestAgentSaveArtifactsLargeOrdinaryCSVNotRejectedByEvidencePreview(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	content := "sample,value\n" + strings.Repeat(strings.Repeat("x", 2048)+",1\n", 26000)
	writeAgentSaveArtifactsFile(t, fixture.projectPath, "results.csv", content)
	input := map[string]any{"files": []any{"results.csv"}, "language": "text", "human_description": "Saving measured table"}
	ctx := withTranscriptRunnerChatRun(fixture.toolContext(t, "save-large-table", input), &sessionRunnerChatRun{VerificationExplicitlyDisabled: true})
	result, err := fixture.server.executeAgentSaveArtifacts(ctx, fixture.identity, "save-large-table", input)
	if err != nil || len(agentSaveArtifactResults(t, result)) != 1 || result["errors"] != nil {
		t.Fatalf("large ordinary CSV rejected by evidence preview: result=%v err=%v", result, err)
	}
}

func TestAgentSaveArtifactsTemplateTailBeyondFormerScanBudget(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "template-*.md")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := io.WriteString(file, strings.Repeat("ordinary report\n", (16<<20)/16+1)+"Result: {value}\n"); err != nil {
		t.Fatal(err)
	}
	if err := validateAgentSavedArtifactTemplates(context.Background(), "report.md", file); err == nil {
		t.Fatal("template at large report tail was silently skipped")
	}
}
