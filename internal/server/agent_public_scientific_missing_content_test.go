package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestPublicScientificWorkspaceDownloadPreservesCancellation(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		if err := agentPublicScientificWorkspaceDownloadError(errors.Join(errAgentWorkspaceDownloadConflict, cause)); !errors.Is(err, cause) {
			t.Fatalf("download cancellation changed into ordinary failure: %v", err)
		}
	}
}

func TestPublicScientificDownloadRecoversMissingCompletedContent(t *testing.T) {
	for _, scenario := range []string{"workspace", "refetch", "unavailable_flag", "changed_source", "changed_workspace", "metadata_conflict"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newAgentSaveArtifactsFixture(t)
			const content = `{"entry":"TEST1"}`
			fetcher := &agentPublicScientificFileFetcher{body: content, contentType: "application/json"}
			fixture.server.publicScientificFiles = fetcher
			fixture.server.publicScientificDownloadSlots = make(chan struct{}, 2)
			const sourceURL = "https://data.example.org/records/TEST1.json"
			sourceID := appendAgentPublicScientificSourceCheckpoint(t, fixture, "missing-source", sourceURL, false, nil)
			input := map[string]any{"source_tool_call_id": sourceID, "url": sourceURL, "human_description": "Downloading a public record"}
			first, err := fixture.server.executeAgentPublicScientificFileDownload(agentPublicScientificToolContext(t, fixture, "first-download", input), fixture.identity, "first-download", input)
			if err != nil {
				t.Fatal(err)
			}
			firstResult := agentSaveArtifactResults(t, first)[0]
			versionID := stringValue(firstResult["version_id"])
			_, version, found, err := fixture.store.GetArtifactVersionMetadata(versionID)
			if err != nil || !found {
				t.Fatalf("metadata found=%v err=%v", found, err)
			}
			payload, err := json.Marshal(map[string]any{"status": "completed", "toolPhase": "completed", "toolName": "download_public_scientific_file", "toolCallId": "first-download", "toolInput": input, "toolResult": first})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{Claim: fixture.claim, ClientMessageID: "completed-download", Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: payload, Destinations: []string{"ws"}}); err != nil {
				t.Fatal(err)
			}
			blob := filepath.Join(fixture.databasePath+".blobs", filepath.FromSlash(version.StoragePath))
			if err := os.Remove(blob); err != nil {
				t.Fatal(err)
			}
			workspaceFile := filepath.Join(fixture.projectPath, "TEST1.json")
			switch scenario {
			case "refetch", "changed_source":
				if err := os.Remove(workspaceFile); err != nil {
					t.Fatal(err)
				}
				if scenario == "changed_source" {
					fetcher.body = `{"entry":"CHANGED"}`
				}
			case "unavailable_flag":
				if _, err := fixture.db.Exec(`UPDATE artifact_versions SET content_available=0 WHERE id=?`, versionID); err != nil {
					t.Fatal(err)
				}
			case "changed_workspace":
				if err := os.WriteFile(workspaceFile, []byte("user changes"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "metadata_conflict":
				if _, err := fixture.db.Exec(`UPDATE artifact_versions SET content_sha256=? WHERE id=?`, strings.Repeat("f", 64), versionID); err != nil {
					t.Fatal(err)
				}
			}
			result, err := fixture.server.executeAgentPublicScientificFileDownload(agentPublicScientificToolContext(t, fixture, "resumed-download", input), fixture.identity, "resumed-download", input)
			if scenario == "changed_source" || scenario == "changed_workspace" || scenario == "metadata_conflict" {
				if err == nil {
					t.Fatal("changed authority or content was accepted")
				}
				if scenario == "changed_source" && !errors.Is(err, errAgentPublicScientificFileChecksum) {
					t.Fatalf("changed upstream error=%v", err)
				}
				if scenario == "changed_workspace" {
					bytes, _ := os.ReadFile(workspaceFile)
					if string(bytes) != "user changes" {
						t.Fatal("workspace changes were overwritten")
					}
				}
				if scenario != "changed_source" && len(fetcher.callSnapshot()) != 1 {
					t.Fatal("invalid authority reached the network")
				}
				return
			}
			if err != nil {
				t.Fatalf("missing historical content blocked recovery: %v", err)
			}
			recoveredID := stringValue(agentSaveArtifactResults(t, result)[0]["version_id"])
			if recoveredID == versionID {
				t.Fatal("unreadable historical version was claimed as recovered")
			}
			_, recovered, found, err := fixture.store.GetArtifactVersion(recoveredID)
			if err != nil || !found || string(recovered.Content) != content || recovered.ParentID != versionID {
				t.Fatalf("recovered metadata=%#v found=%v err=%v", recovered, found, err)
			}
			wantCalls := 1
			if scenario == "refetch" {
				wantCalls = 2
			}
			if len(fetcher.callSnapshot()) != wantCalls {
				t.Fatalf("network calls=%d want=%d", len(fetcher.callSnapshot()), wantCalls)
			}
		})
	}
}
