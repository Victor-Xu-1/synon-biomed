package server

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/runtimecontrol"
)

type runnerArtifactScanInput struct {
	name string
	open func() (workspace.ArtifactContentReader, error)
}

func (s *Server) runnerArtifactScanInput(name, versionID, projectID string) runnerArtifactScanInput {
	return runnerArtifactScanInput{name: name, open: func() (workspace.ArtifactContentReader, error) {
		artifact, _, reader, found, err := s.workspaceStore.OpenArtifactVersionContent(versionID)
		if err != nil {
			return nil, err
		}
		if !found || artifact.ProjectID != projectID {
			if reader != nil {
				_ = reader.Close()
			}
			return nil, fmt.Errorf("runner cross-artifact version is unavailable: %s", versionID)
		}
		return reader, nil
	}}
}

func runnerArtifactTextInput(name, content string) runnerArtifactScanInput {
	return runnerArtifactScanInput{name: name, open: func() (workspace.ArtifactContentReader, error) {
		return runnerArtifactStringReader{strings.NewReader(content)}, nil
	}}
}

type runnerArtifactStringReader struct{ *strings.Reader }

func (runnerArtifactStringReader) Close() error { return nil }

type runnerArtifactTemporaryReader struct{ *os.File }

func (reader runnerArtifactTemporaryReader) Close() error {
	return errors.Join(reader.File.Close(), os.Remove(reader.Name()))
}

type runnerArtifactReaderAt struct {
	mu     sync.Mutex
	ctx    context.Context
	reader io.ReadSeeker
}

func (reader *runnerArtifactReaderAt) ReadAt(data []byte, offset int64) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	if _, err := reader.reader.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	n, err := io.ReadFull(&contextReader{ctx: reader.ctx, reader: reader.reader}, data)
	if errors.Is(err, io.ErrUnexpectedEOF) {
		err = io.EOF
	}
	return n, err
}

func (input runnerArtifactScanInput) openText(ctx context.Context) (workspace.ArtifactContentReader, error) {
	reader, err := input.open()
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(filepath.Ext(input.name), ".pptx") {
		return reader, nil
	}
	text, err := streamRunnerPresentationText(ctx, reader)
	closeErr := reader.Close()
	if err != nil || closeErr != nil {
		if text != nil {
			_ = text.Close()
		}
		return nil, errors.Join(err, closeErr)
	}
	return text, nil
}

func streamRunnerPresentationText(ctx context.Context, source io.ReadSeeker) (_ workspace.ArtifactContentReader, resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	size, err := source.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	archive, err := zip.NewReader(&runnerArtifactReaderAt{ctx: ctx, reader: source}, size)
	if err != nil {
		return nil, fmt.Errorf("read runner cross-artifact presentation: %w", err)
	}
	output, err := os.CreateTemp("", "synon-artifact-visible-*")
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			_ = output.Close()
			_ = os.Remove(output.Name())
		}
	}()
	writer := runtimecontrol.DiskCapacityWriter(ctx, output, output.Name())
	for _, entry := range archive.File {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := filepath.ToSlash(entry.Name)
		if !strings.HasPrefix(path, "ppt/slides/slide") || !strings.HasSuffix(path, ".xml") {
			continue
		}
		file, err := entry.Open()
		if err != nil {
			return nil, err
		}
		decodeErr := streamRunnerSlideText(ctx, file, writer)
		if err := errors.Join(decodeErr, file.Close()); err != nil {
			return nil, err
		}
	}
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return runnerArtifactTemporaryReader{output}, nil
}

func streamRunnerSlideText(ctx context.Context, source io.Reader, destination io.Writer) error {
	decoder := xml.NewDecoder(&contextReader{ctx: ctx, reader: source})
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "t" {
			continue
		}
		var text string
		if err := decoder.DecodeElement(&text, &start); err != nil {
			return err
		}
		if _, err := io.WriteString(destination, text+"\n"); err != nil {
			return err
		}
	}
}
