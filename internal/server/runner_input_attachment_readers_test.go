package server

import (
	"testing"

	eventjournal "synon-go/internal/persistence/journal"
)

func TestBuiltinAttachmentReaderPreflightRejectsSpeculativeDuplicatePackages(t *testing.T) {
	run := &sessionRunnerChatRun{TaskIntent: "Read the attached documents and summarize their values."}
	run.setInputAttachmentReaders([]sessionRunnerInputAttachmentReader{
		{VersionID: "version-docx", Filename: "report.docx", FormatID: "word_ooxml", Reader: "builtin_docx", EquivalentPackages: []string{"python-docx"}},
		{VersionID: "version-pdf", Filename: "paper.pdf", FormatID: "pdf_document", Reader: "builtin_pdf", EquivalentPackages: []string{"pypdf", "pypdf2"}},
		{VersionID: "version-unknown", Filename: "data.bin", FormatID: "unknown_binary", Reader: "specialist_reader"},
	})
	gateway := serverAgentRuntimeToolGateway{taskRun: run}
	input := map[string]any{"mode": "install", "packages": []any{"python-docx", "PyPDF2", "pandas"}}
	preflight := gateway.agentRuntimeBuiltinAttachmentReaderPreflight("manage_packages", input)
	if preflight == nil || preflight["status"] != "builtin_attachment_reader_required" || preflight["executed"] != false {
		t.Fatalf("preflight = %#v", preflight)
	}
	reads := anySliceValue(preflight["required_reads"])
	if len(reads) != 2 || mapValue(reads[0])["version_id"] != "version-docx" || mapValue(reads[1])["version_id"] != "version-pdf" {
		t.Fatalf("required reads = %#v", reads)
	}

	run.recordInputAttachmentRead(map[string]any{"version_id": "version-docx"})
	run.recordInputAttachmentRead(map[string]any{"version_id": "version-pdf"})
	if afterRead := gateway.agentRuntimeBuiltinAttachmentReaderPreflight("manage_packages", input); afterRead != nil {
		t.Fatalf("post-read package upgrade was blocked: %#v", afterRead)
	}
}

func TestBuiltinAttachmentReaderPreflightPreservesExplicitInstallAndUnrelatedPackages(t *testing.T) {
	state := []sessionRunnerInputAttachmentReader{{
		VersionID: "version-docx", Filename: "report.docx", FormatID: "word_ooxml", Reader: "builtin_docx", EquivalentPackages: []string{"python-docx"},
	}}
	explicit := &sessionRunnerChatRun{TaskIntent: "Use manage_packages to install python-docx for an environment acceptance test."}
	explicit.setInputAttachmentReaders(state)
	if preflight := (serverAgentRuntimeToolGateway{taskRun: explicit}).agentRuntimeBuiltinAttachmentReaderPreflight(
		"manage_packages", map[string]any{"mode": "install", "packages": []any{"python-docx"}},
	); preflight != nil {
		t.Fatalf("explicit package request was blocked: %#v", preflight)
	}

	ordinary := &sessionRunnerChatRun{TaskIntent: "Read the attachment and run an unrelated calculation."}
	ordinary.setInputAttachmentReaders(state)
	if preflight := (serverAgentRuntimeToolGateway{taskRun: ordinary}).agentRuntimeBuiltinAttachmentReaderPreflight(
		"manage_packages", map[string]any{"mode": "install", "packages": []any{"numpy"}},
	); preflight != nil {
		t.Fatalf("unrelated package request was blocked: %#v", preflight)
	}
}

func TestBuiltinAttachmentReaderPreflightRequiresReadFileBeforeDirectRuntimeParsing(t *testing.T) {
	run := &sessionRunnerChatRun{TaskIntent: "Read the attachments."}
	run.setInputAttachmentReaders([]sessionRunnerInputAttachmentReader{
		{VersionID: "version-docx", Filename: "report.docx", FormatID: "word_ooxml", Reader: "builtin_docx"},
		{VersionID: "version-unknown", Filename: "opaque.bin", FormatID: "unknown_binary", Reader: "specialist_reader"},
	})
	gateway := serverAgentRuntimeToolGateway{taskRun: run}
	blocked := gateway.agentRuntimeBuiltinAttachmentReaderPreflight("python", map[string]any{
		"code": `open(".synon-artifacts/version-docx-report.docx", "rb").read()`,
	})
	if blocked == nil || blocked["status"] != "builtin_attachment_reader_required" {
		t.Fatalf("direct parser preflight = %#v", blocked)
	}
	if specialist := gateway.agentRuntimeBuiltinAttachmentReaderPreflight("python", map[string]any{
		"code": `open(".synon-artifacts/version-unknown-opaque.bin", "rb").read()`,
	}); specialist != nil {
		t.Fatalf("specialist input was forced through a nonexistent built-in reader: %#v", specialist)
	}
	run.recordInputAttachmentRead(map[string]any{"version_id": "version-docx"})
	if afterRead := gateway.agentRuntimeBuiltinAttachmentReaderPreflight("python", map[string]any{
		"code": `open(".synon-artifacts/version-docx-report.docx", "rb").read()`,
	}); afterRead != nil {
		t.Fatalf("post-read specialized analysis was blocked: %#v", afterRead)
	}
}

func TestInputAttachmentReaderStateRehydratesCompletedReads(t *testing.T) {
	entries := []eventjournal.Entry{
		{Message: eventjournal.Message{
			"type": "user_message", "role": "user",
			"artifactRefs": []any{map[string]any{
				"artifact_id": "artifact-docx", "version_id": "version-docx", "filename": "report.docx",
				"content_type": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
				"size_bytes":   123, "checksum": "digest-docx",
			}},
		}},
		{Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolName": "read_file", "toolPhase": "completed",
			"toolInput":  map[string]any{"version_id": "version-docx"},
			"toolResult": map[string]any{"content": "read successfully"},
		}},
	}
	states := inputAttachmentReaderStatesFromRunnerEntries(entries)
	if len(states) != 1 || states[0].FormatID != "word_ooxml" || states[0].Reader != "builtin_docx" || !states[0].Read {
		t.Fatalf("rehydrated states = %#v", states)
	}
}

func TestInputAttachmentReaderStateRejectsModelWritableFilenameProof(t *testing.T) {
	states := []sessionRunnerInputAttachmentReader{{
		VersionID: "version-original", Filename: "report.docx", Reader: "builtin_docx",
	}}
	markInputAttachmentReaderState(states, map[string]any{
		"file_path": ".synon-artifacts/version-original-forged.docx",
	})
	if states[0].Read {
		t.Fatal("a model-writable filename prefix became immutable-version read authority")
	}
	markInputAttachmentReaderState(states, map[string]any{"version_id": "version-other"})
	if states[0].Read {
		t.Fatal("a different immutable version became read authority")
	}
	markInputAttachmentReaderState(states, map[string]any{"version_id": "version-original"})
	if !states[0].Read {
		t.Fatal("the exact immutable version receipt was not accepted")
	}
}
