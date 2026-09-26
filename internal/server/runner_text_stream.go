package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

func validateRunnerTextEncoding(ctx context.Context, source io.Reader) error {
	if ctx == nil {
		ctx = context.Background()
	}
	reader := &contextReader{ctx: ctx, reader: source}
	buffer := make([]byte, 64<<10)
	carry := 0
	for {
		n, err := reader.Read(buffer[carry:])
		length := carry + n
		complete := length
		if length > 0 && err == nil {
			start := length - 1
			for start > 0 && length-start < utf8.UTFMax && !utf8.RuneStart(buffer[start]) {
				start--
			}
			if !utf8.FullRune(buffer[start:length]) {
				complete = start
			}
		}
		if !utf8.Valid(buffer[:complete]) {
			return errAgentSavedArtifactTextInvalid
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		carry = copy(buffer, buffer[complete:length])
	}
}

// Visit table rows in document order without retaining the document or entire
// tables. Ordinals count only nonempty tables, matching the artifact contract.
func visitRunnerMarkdownRows(ctx context.Context, source io.Reader, visit func(int, int, []string, []string) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	reader := bufio.NewReader(&contextReader{ctx: ctx, reader: source})
	var candidate, headers []string
	ordinal, rowIndex := 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			row, parsed := splitRunnerMarkdownTableLine(line)
			if headers != nil && parsed && len(row) == len(headers) {
				if err := visit(ordinal, rowIndex+2, headers, row); err != nil {
					return err
				}
				rowIndex++
			} else {
				if headers != nil {
					if rowIndex > 0 {
						ordinal++
					}
					headers = nil
					rowIndex = 0
				}
				if candidate != nil && parsed && len(row) == len(candidate) && runnerMarkdownSeparatorRow(row) {
					headers, candidate = candidate, nil
				} else if parsed && len(row) >= 2 {
					candidate = row
				} else {
					candidate = nil
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
