package server

import (
	"bytes"
	"context"
	"strings"
	"synon-go/internal/agentruntime"
	"testing"
)

func TestAgentWorkspaceSniffsImageDespiteGenericUploadMIME(t *testing.T) {
	raw := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	result, err := readAgentWorkspaceFile(context.Background(), bytes.NewReader(raw), "image.data", "application/octet-stream", int64(len(raw)), nil)
	if err != nil {
		t.Fatal(err)
	}
	rich, ok := result.(agentRuntimeRichToolResponse)
	if !ok || len(rich.parts) != 1 || rich.parts[0].Type != agentruntime.ContentPartImage {
		t.Fatalf("image lost at upload boundary: %#v", result)
	}
}

func TestAgentWorkspaceDoesNotTrustSpoofedImageMIME(t *testing.T) {
	raw := []byte("ordinary research notes\n")
	result, err := readAgentWorkspaceFile(context.Background(), bytes.NewReader(raw), "notes.png", "image/png", int64(len(raw)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.(agentRuntimeRichToolResponse); ok {
		t.Fatal("ordinary text was sent to the vision model as a PNG")
	}
	if !strings.Contains(result.(map[string]any)["content"].(string), "research notes") {
		t.Fatal(result)
	}
}

func TestAgentWorkspaceDetectsChemDrawByContent(t *testing.T) {
	tests := []struct {
		name      string
		filename  string
		content   []byte
		mediaType string
	}{
		{name: "binary CDX", filename: "compound.data", content: []byte("VjCD0100\x00\x01"), mediaType: "chemical/x-cdx"},
		{name: "CDXML before generic XML sniffing", filename: "compound.xml", content: []byte("<?xml version=\"1.0\"?><CDXML CreationProgram=\"ChemDraw\"></CDXML>"), mediaType: "chemical/x-cdxml"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := readAgentWorkspaceFile(
				context.Background(), bytes.NewReader(test.content), test.filename,
				"application/octet-stream", int64(len(test.content)), map[string]any{"version_id": "version-input"},
			)
			if err != nil {
				t.Fatal(err)
			}
			value := result.(map[string]any)
			if value["inspection_status"] != "reader_required" || value["content_type"] != test.mediaType {
				t.Fatalf("ChemDraw content was not routed to a verified structure reader: %#v", value)
			}
			recovery := value["recovery"].(map[string]any)
			if !strings.Contains(recovery["reason"].(string), "ChemDraw structure detected") {
				t.Fatalf("ChemDraw recovery did not explain the required conversion: %#v", recovery)
			}
		})
	}
}

func TestAgentWorkspaceBinaryIsNotPretendUTF8Text(t *testing.T) {
	raw := []byte("\x00\x01\x02opaque scientific data")
	result, err := readAgentWorkspaceFile(context.Background(), bytes.NewReader(raw), "measurement.unknown", "application/octet-stream", int64(len(raw)), nil)
	if err != nil {
		t.Fatal(err)
	}
	value := result.(map[string]any)
	if value["content"] != nil || value["inspection_status"] != "reader_required" || value["recovery"] == nil {
		t.Fatalf("opaque file needs honest tool recovery, not a false text result: %#v", value)
	}
	recovery := value["recovery"].(map[string]any)
	if instruction := recovery["instruction"].(string); !strings.Contains(instruction, "Reuse an already verified reader/runtime") {
		t.Fatalf("binary recovery can create a duplicate environment instead of reusing the task runtime: %q", instruction)
	}
}
