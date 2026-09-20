package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	workspace "synon-go/internal/persistence/workspace"
)

func TestWorkspaceByteWindowsReassembleLargeSingleLineSources(t *testing.T) {
	for _, format := range []struct{ name, media, prefix, suffix string }{
		{"source.json", "application/json", `{"body":"`, `","tail":"complete-tail"}`},
		{"source.html", "text/html", "<html><body>", "<p>complete-tail</p></body></html>"},
	} {
		t.Run(format.name, func(t *testing.T) {
			source := []byte(format.prefix + strings.Repeat("λ", (33<<20)/2) + format.suffix)
			digest := sha256.Sum256(source)
			reader := bytes.NewReader(source)
			hash := sha256.New()
			offset, windows := 0, 0
			for offset < len(source) {
				value, err := readAgentWorkspaceFile(context.Background(), reader, format.name, format.media, int64(len(source)), map[string]any{"version_id": "immutable-source", "byte_offset": offset, "byte_limit": 65535})
				if err != nil {
					t.Fatal(err)
				}
				page := value.(map[string]any)
				chunk := decodeWorkspaceBytePage(t, page)
				if len(chunk) == 0 || !bytes.Equal(chunk, source[offset:offset+len(chunk)]) {
					t.Fatal("byte window skipped or replaced source bytes")
				}
				_, _ = hash.Write(chunk)
				offset += len(chunk)
				windows++
				if page["eof"] != true && numberValue(page["next_byte_offset"]) != int64(offset) {
					t.Fatal("continuation cursor skipped source bytes")
				}
				if page["snapshot_consistency"] != "immutable_version" {
					t.Fatal("immutable identity lost")
				}
			}
			if hex.EncodeToString(hash.Sum(nil)) != hex.EncodeToString(digest[:]) || windows < 2 {
				t.Fatal("full source digest changed")
			}
		})
	}
}

func TestWorkspaceByteWindowBudgetUTF8TailAndInvalidSelections(t *testing.T) {
	source := []byte("head-λ-tail")
	ctx := withAgentWorkspaceReadBudget(context.Background(), 1024)
	for _, offset := range []int{0, 6, len(source)} {
		page, err := readAgentWorkspaceByteWindow(ctx, bytes.NewReader(source), "source.html", "text/html", int64(len(source)), map[string]any{"byte_offset": offset, "byte_limit": 100000})
		if err != nil || !agentWorkspaceReadResultFits(ctx, page) {
			t.Fatalf("byte window exceeds budget: %v", err)
		}
		if !bytes.Equal(decodeWorkspaceBytePage(t, page), source[offset:]) {
			t.Fatal("UTF-8 byte cursor lost data")
		}
		if offset == 6 && page["encoding"] != "base64" {
			t.Fatal("mid-codepoint read did not use lossless base64")
		}
		if page["eof"] != true || page["next_byte_offset"] != nil || page["snapshot_consistency"] != "mutable_path_no_snapshot" {
			t.Fatal("tail/snapshot receipt is misleading")
		}
	}
	for _, input := range []map[string]any{
		{"byte_offset": -1}, {"byte_offset": 1.5}, {"byte_limit": 4}, {"byte_offset": 0, "byte_limit": 0},
		{"byte_offset": 0, "offset": 1}, {"byte_offset": 0, "limit": 1}, {"byte_offset": 0, "json_pointer": "/body"},
		{"byte_offset": 0, "pages": []any{1}}, {"byte_offset": 0, "recovery_condition_id": strings.Repeat("a", 64)},
	} {
		input["version_id"] = "source"
		if err := validateAgentWorkspaceReadFileInput(normalizeAgentWorkspaceReadFileArguments(input)); err == nil {
			t.Fatalf("incompatible read selector accepted: %v", input)
		}
	}
	if _, err := readAgentWorkspaceByteWindow(ctx, bytes.NewReader(source), "source", "text/plain", int64(len(source)), map[string]any{"byte_offset": len(source) + 1}); err == nil {
		t.Fatal("past-EOF cursor accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := readAgentWorkspaceByteWindow(cancelled, bytes.NewReader(source), "source", "text/plain", int64(len(source)), map[string]any{"byte_offset": 0}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
}

func TestWorkspaceByteWindowKeepsVersionAuthorityAndReuseIdentities(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	ctx := context.Background()
	versions := make([]string, 0, 2)
	for _, content := range []string{"first-version-tail", "second-version-tail"} {
		_, version, err := fixture.store.WriteArtifactVersionRealtime(ctx, workspace.WriteArtifactVersionInput{ArtifactID: "byte-window-source", ProjectID: fixture.stream.ProjectID,
			Name: "source.txt", ContentType: "text/plain", Content: strings.NewReader(content), MaxBytes: int64(len(content)), CreatedBy: fixture.claim.RunnerID}, fixture.stream.OwnerID)
		if err != nil {
			t.Fatal(err)
		}
		versions = append(versions, version.ID)
	}
	keys, fingerprints := map[string]bool{}, map[string]bool{}
	for index, version := range versions {
		for _, offset := range []int{0, 3} {
			input := map[string]any{"version_id": version, "byte_offset": offset, "byte_limit": 5, "human_description": "Reading exact source bytes"}
			value, err := fixture.server.executeAgentWorkspaceFileTool(ctx, fixture.identity, "read_file", input)
			if err != nil {
				t.Fatal(err)
			}
			page := mapValue(value)
			expected := []string{"first-version-tail", "second-version-tail"}[index]
			if string(decodeWorkspaceBytePage(t, page)) != expected[offset:offset+5] {
				t.Fatal("byte window read a different immutable version")
			}
			if page["source_version_id"] != version || page["snapshot_consistency"] != "immutable_version" {
				t.Fatalf("version lost at real reader: %v", page)
			}
			key, ok := sessionRunnerReadReuseKey("read_file", input)
			if !ok || keys[key] {
				t.Fatal("byte cursor/version aliased in read cache")
			}
			keys[key] = true
			raw, _ := json.Marshal(input)
			fingerprint := agentruntime.ExecutionCallFingerprint("read_file", raw)
			if fingerprints[fingerprint] {
				t.Fatal("byte cursor/version aliased in no-progress identity")
			}
			fingerprints[fingerprint] = true
			cacheRun := &sessionRunnerChatRun{}
			cacheRun.storeReadReuse("read_file", input, value)
			if _, found := cacheRun.lookupReadReuse("read_file", input); !found {
				t.Fatal("identical byte window was not reusable")
			}
			input["byte_offset"] = offset + 1
			if _, found := cacheRun.lookupReadReuse("read_file", input); found {
				t.Fatal("next window reused previous bytes")
			}
		}
	}
	forged := *fixture.identity
	forged.access.UserID = "foreign-owner"
	if _, err := fixture.server.executeAgentWorkspaceFileTool(ctx, &forged, "read_file", map[string]any{"version_id": versions[0], "byte_offset": 0}); err == nil {
		t.Fatal("byte window bypassed owner authority")
	}
	if _, err := fixture.server.executeAgentWorkspaceFileTool(ctx, fixture.identity, "read_file", map[string]any{"file_path": "../outside.txt", "byte_offset": 0}); err == nil {
		t.Fatal("byte window bypassed task path authority")
	}
	if err := os.WriteFile(filepath.Join(fixture.projectPath, "mutable.txt"), []byte("mutable"), 0600); err != nil {
		t.Fatal(err)
	}
	value, err := fixture.server.executeAgentWorkspaceFileTool(ctx, fixture.identity, "read_file", map[string]any{"file_path": "mutable.txt", "byte_offset": 0})
	if err != nil || mapValue(value)["snapshot_consistency"] != "mutable_path_no_snapshot" {
		t.Fatal("mutable path falsely claimed an immutable snapshot")
	}
}

func decodeWorkspaceBytePage(t *testing.T, page map[string]any) []byte {
	t.Helper()
	raw := []byte(stringValue(page["content"]))
	if page["encoding"] == "base64" {
		var err error
		raw, err = base64.StdEncoding.DecodeString(string(raw))
		if err != nil {
			t.Fatal(err)
		}
	}
	digest := sha256.Sum256(raw)
	if page["page_sha256"] != hex.EncodeToString(digest[:]) || numberValue(page["bytes_read"]) != int64(len(raw)) {
		t.Fatal("page digest or byte count mismatch")
	}
	return raw
}
