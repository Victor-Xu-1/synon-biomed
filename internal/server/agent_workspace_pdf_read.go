package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"synon-go/internal/agentruntime"
)

func readAgentWorkspacePDF(
	ctx context.Context,
	reader io.ReadSeeker,
	filename string,
	contentType string,
	size int64,
	pages []int,
	input map[string]any,
) (any, error) {
	_, hasOffset := input["offset"]
	_, hasLimit := input["limit"]
	textOnly := hasOffset || hasLimit
	rich := agentRuntimeRichToolResponse{value: map[string]any{
		"filename": filename, "content_type": contentType, "size_bytes": size,
		"visual_parts": 0, "message": "PDF text window; visual content was not requested. The original remains unchanged.",
	}}
	if !textOnly {
		var result any
		var err error
		if len(pages) > 0 {
			result, err = renderAgentWorkspacePDFPages(ctx, reader, filename, contentType, size, pages)
		} else {
			result, err = readAgentWorkspaceInlineVisual(ctx, reader, filename, "application/pdf", size, agentruntime.ContentPartDocument)
		}
		if err != nil {
			return nil, err
		}
		var ok bool
		rich, ok = result.(agentRuntimeRichToolResponse)
		if !ok {
			return result, nil
		}
	}
	value := rich.value.(map[string]any)
	format, confidence, _ := agentWorkspaceFormatHint(filename, contentType)
	status := "parsed_and_attached"
	if textOnly {
		status = "parsed"
	}
	value["reader_contract"] = agentWorkspaceFormatMetadata(format, confidence, status, filename)
	if textOnly {
		mapValue(value["reader_contract"])["representation"] = "extracted_text_lines"
	}
	value["view_format"] = "pdf-extracted-text-lines"
	// Selected-page visual reads keep their page scope. Their line positions
	// are not offsets in the whole document, so expose a separate exact read
	// input instead of an incompatible pages+offset continuation.
	if len(pages) > 0 {
		readWith := copyMapAny(input)
		delete(readWith, "pages")
		readWith["offset"] = 1
		readWith["limit"] = agentWorkspaceReadDefaultRows
		value["text_read_with"] = readWith
	}
	view, err := readAgentWorkspacePDFTextWindow(ctx, reader, filename, contentType, size, pages, input, value)
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return nil, cause
		}
		if textOnly {
			return nil, err
		}
		value["reader_contract"] = agentWorkspaceFormatMetadata(format, confidence, "attached", filename)
		delete(value, "view_format")
		value["text_extraction"] = map[string]any{"status": "unavailable", "method": "pdftotext", "pages": pages, "reason": err.Error()}
		rich.value = value
		return rich, nil
	}
	if len(pages) > 0 {
		delete(view, "next_offset")
		delete(view, "system_hint")
	}
	if len(rich.parts) > 0 && mapValue(view["text_extraction"])["status"] == "no_extractable_text" {
		view["reader_contract"] = agentWorkspaceFormatMetadata(format, confidence, "attached", filename)
	}
	if len(rich.parts) == 0 {
		return view, nil
	}
	rich.value = view
	return rich, nil
}

type agentWorkspaceBoundedCapture struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (capture *agentWorkspaceBoundedCapture) Write(content []byte) (int, error) {
	accepted := len(content)
	remaining := capture.limit - capture.buffer.Len()
	if remaining <= 0 {
		capture.truncated = capture.truncated || len(content) > 0
		return accepted, nil
	}
	if len(content) > remaining {
		capture.buffer.Write(content[:remaining])
		capture.truncated = true
		return accepted, nil
	}
	capture.buffer.Write(content)
	return accepted, nil
}

func (capture *agentWorkspaceBoundedCapture) String() string {
	if capture == nil {
		return ""
	}
	return capture.buffer.String()
}

func stageAgentWorkspacePDFSource(ctx context.Context, reader io.ReadSeeker, size int64) (string, func(), error) {
	// Stage from the already-authorized handle. Reopening its pathname could
	// consume a same-sized replacement after the source was verified.
	if size <= 0 || size == int64(^uint64(0)>>1) {
		return "", func() {}, errors.New("PDF source size is invalid")
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return "", func() {}, errors.New("PDF source could not be reset")
	}
	directory, err := os.MkdirTemp("", "synon-read-pdf-text-*")
	if err != nil {
		return "", func() {}, errors.New("PDF text extraction could not prepare a secure workspace")
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	if err := os.Chmod(directory, 0o700); err != nil {
		cleanup()
		return "", func() {}, errors.New("PDF text extraction could not secure its workspace")
	}
	path := filepath.Join(directory, "source.pdf")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		cleanup()
		return "", func() {}, errors.New("PDF text extraction could not stage the source")
	}
	written, copyErr := copyAgentWorkspaceBounded(ctx, file, reader, size+1)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written != size {
		cleanup()
		return "", func() {}, errors.New("PDF text extraction could not stage the complete source")
	}
	return path, cleanup, nil
}
