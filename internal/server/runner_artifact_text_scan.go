package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"regexp"
	"strings"
)

type runnerTrackingRuneReader struct {
	reader *bufio.Reader
	err    error
}

func (reader *runnerTrackingRuneReader) ReadRune() (rune, int, error) {
	value, size, err := reader.reader.ReadRune()
	if err != nil && !errors.Is(err, io.EOF) {
		reader.err = err
	}
	return value, size, err
}

type runnerErrorTrackingReader struct {
	source io.Reader
	err    error
}

func (reader *runnerErrorTrackingReader) Read(data []byte) (int, error) {
	n, err := reader.source.Read(data)
	if err != nil && !errors.Is(err, io.EOF) {
		reader.err = err
	}
	return n, err
}

// Regex matching on a RuneReader retains offsets rather than complete input.
// Re-seek to each measured match end because a regexp reader may read ahead.
// A non-line-boundary sentinel keeps ^ from treating a resumed cursor as BOF.
func scanRunnerArtifactPattern(ctx context.Context, source io.ReadSeeker, pattern *regexp.Regexp, lineAnchored bool, visit func([]int64) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	base := int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		prefix := ""
		if lineAnchored && base > 0 {
			if _, err := source.Seek(base-1, io.SeekStart); err != nil {
				return err
			}
			var preceding [1]byte
			if _, err := io.ReadFull(source, preceding[:]); err != nil {
				return err
			}
			prefix = "\x00"
			if preceding[0] == '\n' {
				prefix = "\n"
			}
		}
		if _, err := source.Seek(base, io.SeekStart); err != nil {
			return err
		}
		reader := &runnerTrackingRuneReader{reader: bufio.NewReader(io.MultiReader(strings.NewReader(prefix), &contextReader{ctx: ctx, reader: source}))}
		match := pattern.FindReaderSubmatchIndex(reader)
		if reader.err != nil {
			return reader.err
		}
		if len(match) == 0 {
			return nil
		}
		indices := make([]int64, len(match))
		for index, offset := range match {
			if offset < 0 {
				indices[index] = -1
			} else {
				indices[index] = base + int64(offset-len(prefix))
			}
		}
		next := indices[1]
		if next <= base {
			return errors.New("artifact pattern scan did not advance")
		}
		if err := visit(indices); err != nil {
			return err
		}
		base = next
	}
}

func readRunnerArtifactRange(ctx context.Context, source io.ReadSeeker, start, end int64) (string, error) {
	if _, err := source.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: source}, end-start))
	if err != nil {
		return "", err
	}
	if int64(len(data)) != end-start {
		return "", io.ErrUnexpectedEOF
	}
	return string(data), nil
}

func runnerArtifactRangeContainsAny(ctx context.Context, source io.ReadSeeker, start, end int64, markers []string) (bool, error) {
	if _, err := source.Seek(start, io.SeekStart); err != nil {
		return false, err
	}
	reader := io.LimitReader(&contextReader{ctx: ctx, reader: source}, end-start)
	overlap := 0
	for _, marker := range markers {
		overlap = max(overlap, len(marker)-1)
	}
	buffer := make([]byte, (32<<10)+overlap)
	carry := 0
	for {
		n, err := reader.Read(buffer[carry:])
		length := carry + n
		content := strings.ToLower(string(buffer[:length]))
		for _, marker := range markers {
			if strings.Contains(content, marker) {
				return true, nil
			}
		}
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		carry = copy(buffer, buffer[max(0, length-overlap):length])
	}
}
