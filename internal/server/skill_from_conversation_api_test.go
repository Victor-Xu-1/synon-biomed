package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
	"synon-go/internal/skills"
)

func TestConversationSkillTranscriptAcceptsProjectedWebMessages(t *testing.T) {
	transcript := conversationSkillTranscript([]map[string]any{
		{"type": "text", "position": "right", "content": map[string]any{"content": "user goal"}},
		{"type": "tool_call", "position": "left", "content": map[string]any{"call_id": "tool-1"}},
		{"type": "text", "position": "left", "content": map[string]any{"content": "assistant result"}},
	})
	if !strings.Contains(transcript, "### user message") || !strings.Contains(transcript, "user goal") ||
		!strings.Contains(transcript, "### assistant message") || !strings.Contains(transcript, "assistant result") {
		t.Fatalf("projected transcript=%q", transcript)
	}
	if strings.Contains(transcript, "tool-1") {
		t.Fatalf("tool call leaked into authoring transcript=%q", transcript)
	}
}

func TestLoadConversationSkillMessagesUsesTranscriptAuthority(t *testing.T) {
	store, repository, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-skill", "project-skill", "frame-skill")
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-skill", OwnerID: "owner-skill", ExternalID: "frame-skill", SessionID: "frame-skill",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-skill", RootFrameID: "frame-skill",
		FrameID: "frame-skill", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repository.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "skill-user-1",
		FrameEventID: "skill-frame-event-1", MessageUUID: "skill-message-1", Text: "distill this workflow",
	}); err != nil || !created {
		t.Fatalf("append user event created=%t err=%v", created, err)
	}

	server := &Server{workspaceStore: store, transcriptStore: repository}
	messages, err := server.loadConversationSkillMessages(context.Background(), "owner-skill", "frame-skill")
	if err != nil {
		t.Fatal(err)
	}
	transcript := conversationSkillTranscript(messages)
	if !strings.Contains(transcript, "distill this workflow") {
		t.Fatalf("transcript authority content missing: %q", transcript)
	}
}

func TestConversationSkillDraftBundleProducesLoadableSkill(t *testing.T) {
	bundle, files, err := conversationSkillDraftBundle(conversationSkillDraft{
		Name:          "personal_workflow",
		Description:   "A reusable personal workflow.",
		SkillMarkdown: "# Workflow\n\n1. Validate the inputs.\n2. Produce the result.",
		KernelPython:  "def helper(value):\n    return value\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0] != "SKILL.md" || files[1] != "kernel.py" {
		t.Fatalf("files=%#v", files)
	}
	archive, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		t.Fatal(err)
	}
	staging := t.TempDir()
	for _, entry := range archive.File {
		target := filepath.Join(staging, entry.Name)
		if err := os.WriteFile(target, readZipEntry(t, entry), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	loaded := skills.Load([]string{staging}, skills.LoadOptions{MaxBodyBytes: 12000})
	if len(loaded.LoadErrors()) != 0 {
		t.Fatalf("load errors=%#v", loaded.LoadErrors())
	}
	items := loaded.Skills()
	if len(items) != 1 || items[0].Name != "personal_workflow" {
		t.Fatalf("skills=%#v", items)
	}
}

func TestDecodeConversationSkillDraftRejectsUnsafeName(t *testing.T) {
	raw := map[string]string{
		"name":           "../escape",
		"description":    "bad",
		"skill_markdown": "# body",
		"kernel_py":      "",
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeConversationSkillDraft(string(encoded)); err == nil {
		t.Fatal("expected unsafe Skill name to be rejected")
	}
}

func TestParseSaveConversationSkillInputRejectsUnknownFields(t *testing.T) {
	raw := map[string]json.RawMessage{
		"conversation_id": json.RawMessage([]byte("\"conversation-1\"")),
		"unexpected":      json.RawMessage([]byte("true")),
	}
	if _, err := parseSaveConversationSkillInput(raw); err == nil {
		t.Fatal("expected unknown field to be rejected")
	}
}

func readZipEntry(t *testing.T, entry *zip.File) []byte {
	t.Helper()
	reader, err := entry.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
