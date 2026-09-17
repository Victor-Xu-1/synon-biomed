package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAgentWorkspaceDownloadPublicationUsesCallerCapacity(t *testing.T) {
	workspace := t.TempDir()
	content := []byte("registered package payload")
	digest := sha256.Sum256(content)
	expectedSHA := hex.EncodeToString(digest[:])

	if err := publishAgentWorkspaceDownloadFile(
		context.Background(), workspace, "package.tar.gz", bytes.NewReader(content),
		int64(len(content)), expectedSHA, int64(len(content)),
	); err != nil {
		t.Fatalf("publish within caller capacity: %v", err)
	}
	stored, err := os.ReadFile(filepath.Join(workspace, "package.tar.gz"))
	if err != nil || !bytes.Equal(stored, content) {
		t.Fatalf("stored=%q err=%v", stored, err)
	}

	if err := publishAgentWorkspaceDownloadFile(
		context.Background(), workspace, "too-large.tar.gz", bytes.NewReader(content),
		int64(len(content)), expectedSHA, int64(len(content)-1),
	); !errors.Is(err, errAgentWorkspaceDownloadAuthority) {
		t.Fatalf("over-capacity publication error=%v", err)
	}
}

func TestAgentPublicScientificDownloadStageRecoversReadOnlyCompletedBytes(t *testing.T) {
	server := &Server{fileRoot: t.TempDir()}
	content := []byte("completed registered package")
	request := agentPublicScientificFileRequest{
		SourceURL:      "https://example.com/releases/package.tar.gz",
		DownloadURL:    "https://example.com/releases/package.tar.gz",
		SourceHost:     "example.com",
		Filename:       "package.tar.gz",
		RegisteredSize: int64(len(content)),
	}
	key, err := agentPublicScientificDownloadStageKey(t.TempDir(), request)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := server.openAgentPublicScientificDownloadStage(key, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stage.file.Write(content); err != nil {
		t.Fatal(err)
	}
	stage.state.ExpectedTotal = int64(len(content))
	if err := stage.persist(); err != nil {
		t.Fatal(err)
	}
	if err := stage.file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stage.payload, 0o400); err != nil {
		t.Fatal(err)
	}

	recovered, err := server.openAgentPublicScientificDownloadStage(key, request)
	if err != nil {
		t.Fatalf("recover completed stage: %v", err)
	}
	defer recovered.file.Close()
	stored, err := os.ReadFile(recovered.payload)
	if err != nil || !bytes.Equal(stored, content) {
		t.Fatalf("recovered=%q err=%v", stored, err)
	}
	info, err := recovered.file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v, want 0600", info.Mode().Perm())
	}
}

func TestRegisteredScientificDownloadUsesDeclaredCapacity(t *testing.T) {
	request := agentPublicScientificFileRequest{RegisteredSize: 10 << 30}
	if got := request.maximumBytes(); got != 10<<30 {
		t.Fatalf("maximumBytes=%d, want %d", got, int64(10<<30))
	}
	if got := (agentPublicScientificFileRequest{}).maximumBytes(); got != agentPublicScientificFileLimit {
		t.Fatalf("ordinary maximumBytes=%d, want %d", got, agentPublicScientificFileLimit)
	}
}
