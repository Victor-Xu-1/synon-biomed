package server

import (
	"encoding/hex"
	"io"
	"strings"
)

func agentWorkspaceBinaryMIME(value string) bool {
	return value == "application/octet-stream" || value == "application/zip" || value == "application/x-gzip" || value == "application/gzip" || strings.HasPrefix(value, "audio/") || strings.HasPrefix(value, "video/") || (strings.HasPrefix(value, "image/") && !agentWorkspaceImageMIME(value))
}

// Inspection is not parsing success. Preserve the original authority and provide
// an actionable runtime path instead of pretending binary bytes are UTF-8.
func inspectAgentWorkspaceBinary(reader io.ReadSeeker, filename, contentType string, size int64, input map[string]any, reason string) map[string]any {
	sample := make([]byte, 32)
	if _, err := reader.Seek(0, io.SeekStart); err == nil {
		n, _ := reader.Read(sample)
		sample = sample[:n]
	}
	source := map[string]any{}
	for _, key := range []string{"version_id", "file_path"} {
		if value, ok := input[key]; ok {
			source[key] = value
		}
	}
	if reason == "" {
		reason = "The original is binary or structured data, not plain text."
	}
	format, confidence, _ := agentWorkspaceFormatHint(filename, contentType)
	readerContract := agentWorkspaceFormatMetadata(format, confidence, "reader_required", filename)
	return map[string]any{
		"filename": filename, "content_type": contentType, "size_bytes": size,
		"inspection_status": "reader_required", "source_reference": source, "header_hex": hex.EncodeToString(sample),
		"reader_contract": readerContract,
		"reader_selection": map[string]any{
			"mode": "model_select_verified_reader", "preferred_tools": readerContract["preferred_tools"],
			"requirements": []string{"inspect_content_magic", "preserve_exact_original", "use_read_only_parser", "validate_extracted_content"},
		},
		"recovery": map[string]any{"tool": "repl", "reason": reason, "instruction": "Use import host; path = host.artifact_path(version_id) in repl to materialize the exact authorized original attachment, or use the validated file_path for an existing workspace file. Select a matching available Skill or verified read-only parser from the reader_contract; inspect format magic and available runtime packages first. Reuse an already verified reader/runtime from this task; provision a new managed environment only when no suitable reader is actually available. Do not treat a version ID as a filesystem path, execute uploaded programs/macros, or claim content was parsed from this metadata alone."},
	}
}
