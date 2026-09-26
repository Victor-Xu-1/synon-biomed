package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"io"
	"path/filepath"
	"strings"
)

// Visit the canonical records, not a file-size-limited preview. Memory for
// parsing scales with one record; callbacks must not retain ReuseRecord slices.
// Header selection lets unrelated result tables avoid evidence-only validation.
func visitRunnerEvidenceRows(ctx context.Context, source io.Reader, name string,
	selectHeader func(map[string]int) bool,
	visit func(int, map[string]int, []string) error,
) error {
	var headers map[string]int
	return visitRunnerDelimitedRows(ctx, source, name, func(header []string) bool {
		headers = make(map[string]int, len(header))
		for index, value := range header {
			headers[normalizeRunnerTableToken(value)] = index
		}
		return selectHeader == nil || selectHeader(headers)
	}, func(row int, _ []string, values []string) error { return visit(row, headers, values) })
}

func visitRunnerDelimitedRows(ctx context.Context, source io.Reader, name string,
	selectHeader func([]string) bool,
	visit func(int, []string, []string) error,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	buffer := bufio.NewReader(&contextReader{ctx: ctx, reader: source})
	if prefix, _ := buffer.Peek(3); bytes.Equal(prefix, []byte{0xef, 0xbb, 0xbf}) {
		_, _ = buffer.Discard(3)
	}
	reader := csv.NewReader(buffer)
	if strings.EqualFold(filepath.Ext(name), ".tsv") {
		reader.Comma = '\t'
	}
	reader.FieldsPerRecord = -1
	reader.ReuseRecord = true
	header, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	header = append([]string(nil), header...)
	if selectHeader != nil && !selectHeader(header) {
		return nil
	}
	for rowNumber := 2; ; rowNumber++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := visit(rowNumber, header, row); err != nil {
			return err
		}
	}
}
