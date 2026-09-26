package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"strconv"
	"strings"
)

// PDF text uses the same serialized-budgeted line window as ordinary text.
// The extractor streams its complete output; it does not discard a prefix's
// tail before the reader can supply a continuation cursor.
func readAgentWorkspacePDFTextWindow(ctx context.Context, source io.ReadSeeker, filename, contentType string, size int64, pages []int, input, metadata map[string]any) (map[string]any, error) {
	converter, err := exec.LookPath("pdftotext")
	if err != nil {
		return nil, errors.New("governed PDF text extractor is not installed")
	}
	path, cleanup, err := stageAgentWorkspacePDFSource(ctx, source, size)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, finished := streamAgentWorkspacePDFText(streamCtx, converter, path, pages)
	defer func() {
		cancel()
		_ = reader.Close()
		<-finished
	}()
	counted := &agentWorkspacePDFTextCounter{Reader: reader}
	offset, limit := 1, agentWorkspaceReadDefaultRows
	if raw, found := input["offset"]; found {
		offset, _ = strictPositiveAgentWorkspaceInteger(raw)
	}
	if raw, found := input["limit"]; found {
		limit, _ = strictPositiveAgentWorkspaceInteger(raw)
	}
	reserved := copyMapAny(metadata)
	extraction := map[string]any{
		"status": "no_extractable_text", "method": "pdftotext", "pages": pages,
		"size_bytes": int64(math.MaxInt64), "total_bytes": int64(math.MaxInt64), "truncated": true,
	}
	reserved["text_extraction"] = extraction
	view, err := readAgentWorkspaceTextWindowWithMetadata(ctx, counted, filename, contentType, size, offset, limit, reserved)
	if err != nil {
		return nil, err
	}
	if numberValue(view["total_lines"]) > 0 {
		extraction["status"] = "parsed"
	}
	extraction["size_bytes"] = len(stringValue(view["content"]))
	extraction["total_bytes"] = counted.bytes
	extraction["truncated"] = view["truncated"] == true || numberValue(view["truncated_lines"]) > 0
	return view, nil
}

type agentWorkspacePDFTextCounter struct {
	io.Reader
	bytes int64
}

func (reader *agentWorkspacePDFTextCounter) Read(buffer []byte) (int, error) {
	count, err := reader.Reader.Read(buffer)
	reader.bytes += int64(count)
	return count, err
}

func streamAgentWorkspacePDFText(ctx context.Context, converter, path string, pages []int) (*io.PipeReader, <-chan struct{}) {
	reader, writer := io.Pipe()
	finished := make(chan struct{})
	selected := append([]int(nil), pages...)
	if len(selected) == 0 {
		selected = []int{0}
	}
	go func() {
		defer close(finished)
		var failure error
		defer func() { _ = writer.CloseWithError(failure) }()
		for index, page := range selected {
			if failure = context.Cause(ctx); failure != nil {
				return
			}
			if len(pages) > 1 {
				prefix := ""
				if index > 0 {
					prefix = "\n"
				}
				if _, failure = fmt.Fprintf(writer, "%s[PDF page %d]\n", prefix, page); failure != nil {
					return
				}
			}
			arguments := []string{"-layout", "-nopgbrk", "-enc", "UTF-8"}
			if page > 0 {
				arguments = append(arguments, "-f", strconv.Itoa(page), "-l", strconv.Itoa(page))
			}
			arguments = append(arguments, path, "-")
			command := exec.CommandContext(ctx, converter, arguments...)
			command.Stdout = writer
			stderr := &agentWorkspaceBoundedCapture{limit: 4 << 10}
			command.Stderr = stderr
			if err := command.Run(); err != nil {
				failure = context.Cause(ctx)
				if failure == nil {
					failure = fmt.Errorf("PDF text extraction failed: %s", strings.TrimSpace(stderr.String()))
				}
				return
			}
		}
	}()
	return reader, finished
}
