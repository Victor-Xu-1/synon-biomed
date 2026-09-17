package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"synon-go/internal/agentruntime"
	"unicode/utf8"
)

const agentWorkspacePDFTextMaxBytes = agentWorkspaceReadMaxBytes

func readAgentWorkspacePDF(
	ctx context.Context,
	reader io.ReadSeeker,
	filename string,
	contentType string,
	size int64,
	pages []int,
) (any, error) {
	text, extraction := extractAgentWorkspacePDFText(ctx, reader, size, pages)
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return nil, errors.New("read_file could not reset the requested PDF")
	}
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
	rich, ok := result.(agentRuntimeRichToolResponse)
	if !ok {
		return result, nil
	}
	value, ok := rich.value.(map[string]any)
	if !ok {
		return result, nil
	}
	format, confidence, _ := agentWorkspaceFormatHint(filename, contentType)
	status := "attached"
	if text != "" {
		status = "parsed_and_attached"
		value["content"] = text
	}
	value["text_extraction"] = extraction
	value["reader_contract"] = agentWorkspaceFormatMetadata(format, confidence, status, filename)
	rich.value = value
	return rich, nil
}

func extractAgentWorkspacePDFText(ctx context.Context, reader io.ReadSeeker, size int64, pages []int) (string, map[string]any) {
	metadata := map[string]any{"status": "unavailable", "method": "pdftotext", "pages": pages}
	converter, err := exec.LookPath("pdftotext")
	if err != nil {
		metadata["reason"] = "governed PDF text extractor is not installed"
		return "", metadata
	}
	path, cleanup, err := stageAgentWorkspacePDFSource(ctx, reader, size)
	if err != nil {
		metadata["reason"] = err.Error()
		return "", metadata
	}
	defer cleanup()
	var output strings.Builder
	truncated := false
	selectedPages := pages
	if len(selectedPages) == 0 {
		selectedPages = []int{0}
	}
	for _, page := range selectedPages {
		remaining := agentWorkspacePDFTextMaxBytes - output.Len()
		if remaining <= 0 {
			truncated = true
			break
		}
		arguments := []string{"-layout", "-nopgbrk"}
		if page > 0 {
			arguments = append(arguments, "-f", strconv.Itoa(page), "-l", strconv.Itoa(page))
		}
		arguments = append(arguments, path, "-")
		capture := &agentWorkspaceBoundedCapture{limit: remaining}
		command := exec.CommandContext(ctx, converter, arguments...)
		command.Stdout = capture
		var stderr agentWorkspaceBoundedCapture
		stderr.limit = 4 << 10
		command.Stderr = &stderr
		if runErr := command.Run(); runErr != nil {
			metadata["reason"] = "PDF text extraction failed"
			if detail := strings.TrimSpace(stderr.String()); detail != "" {
				metadata["diagnostic"] = detail
			}
			return "", metadata
		}
		if capture.truncated {
			truncated = true
		}
		content := strings.TrimSpace(capture.String())
		if content == "" {
			continue
		}
		if page > 0 && len(pages) > 1 {
			if output.Len() > 0 {
				output.WriteString("\n\n")
			}
			fmt.Fprintf(&output, "[PDF page %d]\n", page)
		}
		output.WriteString(content)
	}
	text := strings.TrimSpace(output.String())
	if !utf8.ValidString(text) {
		metadata["reason"] = "PDF text extractor returned invalid UTF-8"
		return "", metadata
	}
	if text == "" {
		metadata["status"] = "no_extractable_text"
		return "", metadata
	}
	metadata["status"] = "parsed"
	metadata["truncated"] = truncated
	metadata["size_bytes"] = len(text)
	return text, metadata
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
	if named, ok := reader.(interface{ Name() string }); ok {
		path := strings.TrimSpace(named.Name())
		if path != "" {
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Size() == size {
				return path, func() {}, nil
			}
		}
	}
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
