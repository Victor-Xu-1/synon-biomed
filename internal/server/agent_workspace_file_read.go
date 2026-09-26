package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"synon-go/internal/agentruntime"
	"unicode/utf8"
)

func readAgentWorkspaceFile(
	ctx context.Context,
	reader io.ReadSeeker,
	filename string,
	declaredContentType string,
	size int64,
	input map[string]any,
) (any, error) {
	if _, selected := input["byte_offset"]; selected {
		return readAgentWorkspaceByteWindow(ctx, reader, filename, declaredContentType, size, input)
	}
	contentType, err := detectAgentWorkspaceContentType(reader, filename, declaredContentType)
	if err != nil {
		return nil, errors.New("read_file could not inspect the requested content")
	}
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType == "chemical/x-cdx" || mediaType == "chemical/x-cdxml" {
		return inspectAgentWorkspaceBinary(reader, filename, mediaType, size, input, "ChemDraw structure detected. Use Open Babel or another verified chemistry reader in the governed task runtime to convert the original to SDF, then validate atom/bond counts, charge and stereochemistry."), nil
	}
	if mediaType == "application/zip" || mediaType == "application/octet-stream" {
		if result, handled, structuredErr := readAgentWorkspaceOOXML(ctx, reader, filename, contentType, size, input); structuredErr != nil {
			return nil, structuredErr
		} else if handled {
			return result, nil
		}
	}
	format, confidence, _ := agentWorkspaceFormatHint(filename, contentType)
	if format.Reader == "builtin_html" {
		return readAgentWorkspaceHTML(ctx, reader, filename, contentType, size, input, format, confidence)
	}
	if format.Reader == "builtin_delimited" {
		return readAgentWorkspaceDelimited(ctx, reader, filename, contentType, size, input, format, confidence)
	}
	if _, selected := input["json_pointer"]; selected {
		if mediaType == "application/pdf" || agentWorkspaceImageMIME(mediaType) {
			return nil, errors.New("read_file.json_pointer requires JSON content")
		}
		return readAgentWorkspaceJSONSelection(ctx, reader, filename, size, input)
	}
	if agentWorkspaceImageMIME(mediaType) {
		if _, found := input["pages"]; found {
			return nil, errors.New("read_file.pages is available only for PDF documents")
		}
		if size > agentWorkspaceVisualMaxBytes {
			return inspectAgentWorkspaceBinary(reader, filename, contentType, size, input,
				"The image is accepted but exceeds the inline model transport budget. Select a verified read-only image reader to inspect metadata, tile it, or derive a bounded preview while preserving the original."), nil
		}
		return readAgentWorkspaceInlineVisual(ctx, reader, filename, mediaType, size, agentruntime.ContentPartImage)
	}
	if mediaType == "application/pdf" {
		if size > agentWorkspacePDFMaxBytes {
			return inspectAgentWorkspaceBinary(reader, filename, contentType, size, input,
				"The PDF is accepted but exceeds the inline model transport budget. Select a verified read-only PDF reader for streaming text extraction or bounded page rendering while preserving the original."), nil
		}
		pages := agentWorkspacePageValues(input["pages"])
		return readAgentWorkspacePDF(ctx, reader, filename, contentType, size, pages, input)
	}
	if agentWorkspaceBinaryMIME(mediaType) {
		return inspectAgentWorkspaceBinary(reader, filename, contentType, size, input, ""), nil
	}
	// Oversized tool evidence is persisted as canonical compact JSON. Present a
	// deterministic indented view before applying the normal line window so a
	// read does not externalize another opaque one-line result and recurse
	// forever. The immutable stored bytes remain unchanged.
	jsonViewWrapped := false
	if mediaType == "application/json" && size > 0 && size <= maxAgentWorkspaceStructuredJSONBytes {
		raw, readErr := readAgentWorkspaceBounded(ctx, reader, maxAgentWorkspaceStructuredJSONBytes+1)
		replaced := false
		if readErr == nil && int64(len(raw)) == size && json.Valid(raw) {
			if view, matched, err := agentWorkspaceStructuredView(ctx, raw, filename, size, input); matched {
				return view, err
			}
			if size > int64(agentWorkspaceReadSerializedLimit(ctx)) {
				if navigation, ok := agentWorkspaceJSONNavigation(ctx, raw, filename, contentType, size, input); ok {
					return navigation, nil
				}
			}
			if formatted, wrapped, err := agentWorkspaceJSONReadView(raw); err == nil {
				reader = bytes.NewReader(formatted)
				jsonViewWrapped = wrapped
				replaced = true
			}
		}
		if !replaced {
			if _, seekErr := reader.Seek(0, io.SeekStart); seekErr != nil {
				return nil, errors.New("read_file could not reset the requested content")
			}
		}
	}
	_, hasOffset := input["offset"]
	_, hasLimit := input["limit"]
	if jsonViewWrapped {
		offset, limit := 1, agentWorkspaceReadDefaultRows
		if raw, found := input["offset"]; found {
			offset, _ = strictPositiveAgentWorkspaceInteger(raw)
		}
		if raw, found := input["limit"]; found {
			limit, _ = strictPositiveAgentWorkspaceInteger(raw)
		}
		return readAgentWorkspaceTextWindowWithMetadata(ctx, reader, filename, contentType, size, offset, limit, map[string]any{"view_format": "indexed-json-display-lines"})
	}
	readTextWindow := func(offset, limit int) (map[string]any, error) {
		return readAgentWorkspaceTextWindowWithMetadata(ctx, reader, filename, contentType, size, offset, limit,
			map[string]any{"reader_contract": agentWorkspaceFormatMetadata(format, confidence, "partial_text_window", filename)})
	}
	if !hasOffset && !hasLimit && size > agentWorkspaceReadMaxBytes {
		return readTextWindow(1, agentWorkspaceReadDefaultRows)
	}
	if !hasOffset && !hasLimit {
		content, readErr := readAgentWorkspaceBounded(ctx, reader, agentWorkspaceReadMaxBytes+1)
		if readErr != nil {
			return nil, errors.New("read_file could not read the requested content")
		}
		if len(content) > agentWorkspaceReadMaxBytes {
			if _, seekErr := reader.Seek(0, io.SeekStart); seekErr != nil {
				return nil, errors.New("read_file could not reset the requested content")
			}
			return readTextWindow(1, agentWorkspaceReadDefaultRows)
		}
		if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
			return inspectAgentWorkspaceBinary(reader, filename, contentType, size, input, ""), nil
		}
		result := map[string]any{
			"filename": filename, "content_type": contentType, "size_bytes": size, "content": string(content),
			"reader_contract": agentWorkspaceFormatMetadata(format, confidence, "parsed", filename),
		}
		if agentWorkspaceReadResultFits(ctx, result) {
			return result, nil
		}
		if _, err := reader.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		return readTextWindow(1, agentWorkspaceReadDefaultRows)
	}
	offset, limit := 1, agentWorkspaceReadDefaultRows
	if raw, found := input["offset"]; found {
		offset, _ = strictPositiveAgentWorkspaceInteger(raw)
	}
	if raw, found := input["limit"]; found {
		limit, _ = strictPositiveAgentWorkspaceInteger(raw)
	}
	return readTextWindow(offset, limit)
}

func agentWorkspaceImageMIME(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func readAgentWorkspaceInlineVisual(
	ctx context.Context,
	reader io.Reader,
	filename string,
	contentType string,
	size int64,
	kind agentruntime.ContentPartType,
) (any, error) {
	if size <= 0 || size > agentWorkspaceVisualMaxBytes {
		return agentWorkspaceReadPolicySkip(filename, contentType, size,
			"read_file visual content exceeds the 25 MiB inline limit",
			"Create a smaller visual file before requesting model inspection."), nil
	}
	content, err := readAgentWorkspaceBounded(ctx, reader, agentWorkspaceVisualMaxBytes+1)
	if err != nil {
		return nil, errors.New("read_file could not read the requested visual content")
	}
	if len(content) == 0 || len(content) > agentWorkspaceVisualMaxBytes {
		return agentWorkspaceReadPolicySkip(filename, contentType, size,
			"read_file visual content exceeds the 25 MiB inline limit",
			"Create a smaller visual file before requesting model inspection."), nil
	}
	return agentRuntimeRichToolResponse{
		value: map[string]any{
			"filename": filename, "content_type": contentType, "size_bytes": size,
			"message": "Visual content is attached to the next model request.", "visual_parts": 1,
		},
		parts: []agentruntime.ContentPart{{
			Type: kind,
			Media: &agentruntime.MediaContent{
				MIMEType: contentType, Filename: filepath.Base(filename),
				Source: agentruntime.MediaSource{Type: agentruntime.MediaSourceData, Data: content},
			},
		}},
	}, nil
}

func agentWorkspacePageValues(raw any) []int {
	values := []int{}
	switch typed := raw.(type) {
	case []any:
		for _, rawPage := range typed {
			page, ok := strictPositiveAgentWorkspaceInteger(rawPage)
			if ok {
				values = append(values, page)
			}
		}
	case []int:
		values = append(values, typed...)
	}
	return values
}

func renderAgentWorkspacePDFPages(
	ctx context.Context,
	reader io.Reader,
	filename string,
	contentType string,
	size int64,
	pages []int,
) (any, error) {
	if size <= 0 || size > agentWorkspacePDFMaxBytes {
		return agentWorkspaceReadPolicySkip(filename, contentType, size,
			"read_file PDF exceeds the 500 MiB source limit",
			"Create a smaller PDF before selecting pages for visual inspection."), nil
	}
	renderer, err := exec.LookPath("pdftoppm")
	if err != nil {
		return agentWorkspaceReadPolicySkip(filename, contentType, size,
			"read_file PDF page rendering is unavailable",
			"Install the governed PDF rendering dependency before requesting selected pages."), nil
	}
	temporaryRoot, err := os.MkdirTemp("", "synon-read-pdf-*")
	if err != nil {
		return nil, errors.New("read_file could not prepare PDF inspection")
	}
	defer os.RemoveAll(temporaryRoot)
	if err := os.Chmod(temporaryRoot, 0o700); err != nil {
		return nil, errors.New("read_file could not secure PDF inspection")
	}
	inputPath := filepath.Join(temporaryRoot, "source.pdf")
	inputFile, err := os.OpenFile(inputPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, errors.New("read_file could not prepare PDF inspection")
	}
	written, copyErr := copyAgentWorkspaceBounded(ctx, inputFile, reader, agentWorkspacePDFMaxBytes+1)
	closeErr := inputFile.Close()
	if copyErr != nil || closeErr != nil {
		return nil, errors.New("read_file could not stage PDF inspection")
	}
	if written > agentWorkspacePDFMaxBytes {
		return agentWorkspaceReadPolicySkip(filename, contentType, size,
			"read_file PDF exceeds the 500 MiB source limit",
			"Create a smaller PDF before selecting pages for visual inspection."), nil
	}
	parts := make([]agentruntime.ContentPart, 0, len(pages))
	total := int64(0)
	for index, page := range pages {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		prefix := filepath.Join(temporaryRoot, fmt.Sprintf("page-%03d", index+1))
		command := exec.CommandContext(ctx, renderer,
			"-f", strconv.Itoa(page), "-l", strconv.Itoa(page), "-singlefile", "-png", "-r", "144", inputPath, prefix,
		)
		if output, renderErr := command.CombinedOutput(); renderErr != nil {
			_ = output
			return nil, fmt.Errorf("read_file could not render PDF page %d", page)
		}
		rendered, openErr := os.Open(prefix + ".png")
		if openErr != nil {
			return nil, fmt.Errorf("read_file rendered PDF page %d exceeds the visual limit", page)
		}
		content, readErr := readAgentWorkspaceBounded(ctx, rendered, agentWorkspaceVisualMaxBytes+1)
		closeErr := rendered.Close()
		if readErr != nil || closeErr != nil || len(content) == 0 || len(content) > agentWorkspaceVisualMaxBytes {
			return nil, fmt.Errorf("read_file rendered PDF page %d exceeds the visual limit", page)
		}
		total += int64(len(content))
		if total > agentWorkspaceVisualTotal {
			return nil, errors.New("read_file selected PDF pages exceed the total visual limit")
		}
		parts = append(parts, agentruntime.ContentPart{
			Type: agentruntime.ContentPartImage,
			Media: &agentruntime.MediaContent{
				MIMEType: "image/png", Filename: fmt.Sprintf("%s-page-%d.png", strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename)), page),
				Source: agentruntime.MediaSource{Type: agentruntime.MediaSourceData, Data: content},
			},
		})
	}
	return agentRuntimeRichToolResponse{
		value: map[string]any{
			"filename": filename, "content_type": contentType, "size_bytes": size,
			"message": "Selected PDF pages are attached to the next model request.", "pages": pages, "visual_parts": len(parts),
		},
		parts: parts,
	}, nil
}

func copyAgentWorkspaceBounded(ctx context.Context, writer io.Writer, reader io.Reader, maxBytes int64) (int64, error) {
	buffer := make([]byte, 64<<10)
	written := int64(0)
	for written < maxBytes {
		if err := context.Cause(ctx); err != nil {
			return written, err
		}
		remaining := maxBytes - written
		chunk := len(buffer)
		if int64(chunk) > remaining {
			chunk = int(remaining)
		}
		count, readErr := reader.Read(buffer[:chunk])
		if count > 0 {
			writtenNow, writeErr := writer.Write(buffer[:count])
			written += int64(writtenNow)
			if writeErr != nil {
				return written, writeErr
			}
			if writtenNow != count {
				return written, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
		if count == 0 {
			return written, io.ErrNoProgress
		}
	}
	return written, nil
}

func readAgentWorkspaceTextWindow(
	ctx context.Context,
	reader io.Reader,
	filename string,
	contentType string,
	size int64,
	offset int,
	limit int,
) (map[string]any, error) {
	buffered := bufio.NewReaderSize(reader, 64<<10)
	var content strings.Builder
	totalLines, firstLine, lastLine := 0, 0, 0
	truncatedLines := 0
	windowTruncated := false
	// Reserve metadata/envelope space and count JSON escaping, not just raw
	// bytes. A read page must fit the same transport as its source descriptor;
	// otherwise the page is externalized again and its cursor is hidden.
	serializedContentBytes := 0
	serializedContentLimit := agentWorkspaceReadSerializedLimit(ctx) - agentWorkspaceTextWindowOverhead(filename, contentType, size, offset, limit)
	const lineTruncationMarker = "\n[read_file: line exceeds the output limit; content truncated]\n"
	for {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		line, truncated, readErr := readAgentWorkspaceBoundedLine(
			buffered, agentWorkspaceReadMaxBytes-len(lineTruncationMarker)-64,
		)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, readErr
		}
		if len(line) == 0 && errors.Is(readErr, io.EOF) {
			break
		}
		totalLines++
		if !utf8.ValidString(line) {
			return agentWorkspaceReadPolicySkip(filename, contentType, size,
				"read_file content is not valid UTF-8 text",
				"Use the project Python runtime for binary or structured scientific data."), nil
		}
		if truncated {
			truncatedLines++
		}
		if totalLines < offset || totalLines-offset >= limit {
			if errors.Is(readErr, io.EOF) {
				break
			}
			continue
		}
		if firstLine == 0 {
			firstLine = totalLines
		}
		entry := strconv.Itoa(totalLines) + "\t" + line
		if truncated {
			entry += lineTruncationMarker
		} else {
			entry += "\n"
		}
		encodedEntry, encodeErr := json.Marshal(entry)
		if encodeErr != nil {
			return nil, encodeErr
		}
		entryBytes := len(encodedEntry) - 2
		firstEntryBounded := false
		if !windowTruncated && content.Len() == 0 &&
			(len(entry) > agentWorkspaceReadMaxBytes || entryBytes > serializedContentLimit) {
			bounded, fits := boundedAgentWorkspaceTextEntry(entry, lineTruncationMarker, min(serializedContentLimit, agentWorkspaceReadMaxBytes))
			if !fits {
				return nil, errors.New("read_file metadata leaves no room for text; use a smaller source selection or its task-local file path")
			}
			entry = bounded
			encodedEntry, _ = json.Marshal(entry)
			entryBytes = len(encodedEntry) - 2
			firstEntryBounded = true
			if !truncated {
				truncatedLines++
			}
		}
		if windowTruncated || content.Len()+len(entry) > agentWorkspaceReadMaxBytes || serializedContentBytes+entryBytes > serializedContentLimit {
			windowTruncated = true
			if errors.Is(readErr, io.EOF) {
				break
			}
			continue
		}
		content.WriteString(entry)
		serializedContentBytes += entryBytes
		lastLine = totalLines
		if firstEntryBounded {
			windowTruncated = true
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	showing := "0-0"
	if firstLine > 0 {
		showing = fmt.Sprintf("%d-%d", firstLine, lastLine)
	}
	result := map[string]any{
		"filename": filename, "content_type": contentType, "size_bytes": size,
		"total_lines": totalLines, "showing_lines": showing, "content": content.String(),
	}
	if truncatedLines > 0 {
		result["truncated_lines"] = truncatedLines
	}
	hasMore := lastLine > 0 && lastLine < totalLines
	if windowTruncated || hasMore || truncatedLines > 0 {
		result["truncated"] = true
		result["requested_offset"] = offset
		result["requested_limit"] = limit
		if hasMore {
			result["next_offset"] = lastLine + 1
		}
		result["system_hint"] = agentWorkspaceTextContinuationHint
		if truncatedLines > 0 {
			result["system_hint"] = "Use byte_offset=0 and byte_limit for complete raw bytes; follow next_byte_offset."
		}
	}
	return result, nil
}

// readAgentWorkspaceBoundedLine reads one logical line up to budget bytes.
// Oversized lines are consumed in full (so line numbering stays correct) while
// only the bounded prefix is returned, mirroring the read_file contract of
// returning a bounded preview instead of failing on machine-generated
// single-line JSON artifacts.
func readAgentWorkspaceBoundedLine(reader *bufio.Reader, budget int) (string, bool, error) {
	var builder strings.Builder
	truncated := false
	preview := func() string {
		line := strings.TrimSuffix(builder.String(), "\n")
		if truncated {
			// A byte budget may end inside the last code point. Omit only that
			// unfinished code point; invalid interior bytes remain detectable by
			// the caller, and the consumed logical line/cursor is unchanged.
			for start := max(0, len(line)-(utf8.UTFMax-1)); start < len(line); start++ {
				if !utf8.FullRuneInString(line[start:]) {
					return line[:start]
				}
			}
		}
		return line
	}
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(chunk) > 0 {
			remaining := budget - builder.Len()
			if remaining <= 0 {
				truncated = true
			} else if len(chunk) > remaining {
				builder.Write(chunk[:remaining])
				truncated = true
			} else {
				builder.Write(chunk)
			}
		}
		if err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if errors.Is(err, io.EOF) {
				return preview(), truncated, io.EOF
			}
			return builder.String(), truncated, err
		}
		if len(chunk) > 0 && chunk[len(chunk)-1] == '\n' {
			return preview(), truncated, nil
		}
	}
}

func detectAgentWorkspaceContentType(reader io.ReadSeeker, filename string, declared string) (string, error) {
	declared = strings.TrimSpace(declared)
	buffer := make([]byte, 512)
	count, err := reader.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	if count == 0 {
		return "text/plain; charset=utf-8", nil
	}
	sample := buffer[:count]
	if bytes.HasPrefix(sample, []byte("VjCD0100")) {
		return "chemical/x-cdx", nil
	}
	if bytes.Contains(sample, []byte("<CDXML")) {
		return "chemical/x-cdxml", nil
	}
	sniffed := http.DetectContentType(sample)
	if !strings.HasPrefix(sniffed, "text/plain") {
		return sniffed, nil
	}
	// Browser MIME and filename are hints, not evidence. Never send text to a
	// vision model just because it was named .png or declared as image/png.
	for _, hint := range []string{declared, mime.TypeByExtension(filepath.Ext(filename))} {
		base, _, err := mime.ParseMediaType(hint)
		if err == nil && (strings.HasPrefix(base, "text/") || strings.HasPrefix(base, "chemical/") || base == "application/json" || base == "application/xml") {
			return hint, nil
		}
	}
	return sniffed, nil
}

func agentWorkspaceReadPolicySkip(filename, contentType string, size int64, message, hint string) map[string]any {
	return map[string]any{
		"filename": filename, "content_type": contentType, "size_bytes": size,
		"error": message, "system_hint": hint, "_policy_skip": true,
	}
}

func agentWorkspacePageCount(raw any) int {
	switch typed := raw.(type) {
	case []any:
		return len(typed)
	case []int:
		return len(typed)
	default:
		return 0
	}
}

func secureAgentWorkspacePathParts(relative string) ([]string, error) {
	relative = filepath.Clean(relative)
	if relative == "." || filepath.IsAbs(relative) || filepath.VolumeName(relative) != "" {
		return nil, errors.New("workspace path must name a file below the authorized root")
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) == 0 {
		return nil, errors.New("workspace path must name a file below the authorized root")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.IndexByte(part, 0) >= 0 {
			return nil, errors.New("workspace path contains an invalid component")
		}
		if runtime.GOOS == "windows" && strings.Contains(part, ":") {
			return nil, errors.New("workspace path contains an invalid Windows stream component")
		}
	}
	return parts, nil
}
