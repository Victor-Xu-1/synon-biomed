package server

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

const maxWebOOXMLParts = 20_000

type webOOXMLArchive struct {
	closer io.Closer
	parts  map[string]*zip.File
}

func openWebOOXMLArchive(filePath string) (*webOOXMLArchive, error) {
	reader, err := zip.OpenReader(filePath)
	if err != nil {
		return nil, fmt.Errorf("open OOXML archive: %w", err)
	}
	archive, err := indexWebOOXMLArchive(reader.File, reader)
	if err != nil {
		_ = reader.Close()
		return nil, err
	}
	return archive, nil
}

// openWebOOXMLArchiveReader lets the task-scoped read_file path reuse the same
// hardened OOXML parser without copying an immutable upload to another file.
// Both artifact blobs and workspace files implement ReaderAt; the caller keeps
// ownership of the underlying reader.
func openWebOOXMLArchiveReader(reader io.ReaderAt, size int64) (*webOOXMLArchive, error) {
	if reader == nil || size <= 0 {
		return nil, errors.New("OOXML archive is empty")
	}
	archive, err := zip.NewReader(reader, size)
	if err != nil {
		return nil, fmt.Errorf("open OOXML archive: %w", err)
	}
	return indexWebOOXMLArchive(archive.File, nil)
}

func indexWebOOXMLArchive(files []*zip.File, closer io.Closer) (*webOOXMLArchive, error) {
	if len(files) == 0 || len(files) > maxWebOOXMLParts {
		return nil, errors.New("OOXML archive has an invalid number of parts")
	}
	archive := &webOOXMLArchive{closer: closer, parts: make(map[string]*zip.File, len(files))}
	for _, file := range files {
		name := strings.ReplaceAll(file.Name, "\\", "/")
		isDirectory := strings.HasSuffix(name, "/")
		partName := strings.TrimSuffix(name, "/")
		cleaned := path.Clean(partName)
		if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") ||
			strings.HasPrefix(cleaned, "/") || cleaned != partName {
			return nil, errors.New("OOXML archive contains an unsafe part name")
		}
		// ZIP writers commonly include canonical directory entries such as
		// "word/". They are containers rather than OOXML parts, so validate the
		// normalized name above and then omit them from the part index.
		if isDirectory {
			continue
		}
		if _, duplicate := archive.parts[cleaned]; duplicate {
			return nil, errors.New("OOXML archive contains duplicate parts")
		}
		archive.parts[cleaned] = file
	}
	return archive, nil
}

func (archive *webOOXMLArchive) Close() error {
	if archive == nil || archive.closer == nil {
		return nil
	}
	return archive.closer.Close()
}

func (archive *webOOXMLArchive) ReadPart(name string, limit int64) ([]byte, error) {
	file := archive.parts[name]
	if file == nil {
		return nil, fmt.Errorf("OOXML part %q is missing", name)
	}
	if file.UncompressedSize64 > uint64(limit) {
		return nil, errWebFSTooLarge
	}
	input, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer input.Close()
	data, err := io.ReadAll(io.LimitReader(input, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errWebFSTooLarge
	}
	return data, nil
}

func (archive *webOOXMLArchive) Names(prefix, suffix string) []string {
	names := make([]string, 0)
	for name := range archive.parts {
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, suffix) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
