package server

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"synon-go/internal/tools/fileevents"
)

type webFSZipContent struct {
	Present bool
	Data    []byte
}

func (content *webFSZipContent) UnmarshalJSON(raw []byte) error {
	content.Present = true
	if string(raw) == "null" {
		content.Data = nil
		return nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		content.Data = []byte(text)
		return nil
	}
	var bytesArray []uint8
	if err := json.Unmarshal(raw, &bytesArray); err == nil {
		content.Data = bytesArray
		return nil
	}
	var indexed map[string]uint8
	if err := json.Unmarshal(raw, &indexed); err != nil {
		return errors.New("zip file content must be a string or byte array")
	}
	content.Data = make([]byte, len(indexed))
	for index := range content.Data {
		value, ok := indexed[strconv.Itoa(index)]
		if !ok {
			return errors.New("zip byte object indexes must be contiguous")
		}
		content.Data[index] = value
	}
	return nil
}

type webFSZipFileInput struct {
	Name            string          `json:"name"`
	Content         webFSZipContent `json:"content,omitempty"`
	SourcePath      string          `json:"source_path,omitempty"`
	SourcePathCamel string          `json:"sourcePath,omitempty"`
}

func (s *Server) handleWebZip(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWebFSError(w, fmt.Errorf("%w: POST required", errWebFSMethod))
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWebFSRequestBytes)
	if r.URL.Path == "/api/fs/zip/cancel" {
		s.handleWebZipCancel(w, r)
		return
	}
	allowLocalRead := (s.synonLinkAuth == nil || !s.synonLinkAuth.enabled) && isLoopbackRequest(r)
	var input struct {
		Path       string              `json:"path"`
		Workspace  string              `json:"workspace,omitempty"`
		SourceRoot string              `json:"source_root,omitempty"`
		RequestID  string              `json:"request_id,omitempty"`
		Files      []webFSZipFileInput `json:"files"`
	}
	if err := decodeWebFSJSON(r, &input); err != nil {
		writeWebFSError(w, err)
		return
	}
	if len(input.Files) == 0 || len(input.Files) > maxWebFSCopyEntries {
		writeWebFSError(w, fmt.Errorf("%w: files must contain 1 to %d entries", errWebFSInvalid, maxWebFSCopyEntries))
		return
	}
	if input.RequestID != "" && !validWebFSRequestID(input.RequestID) {
		writeWebFSError(w, fmt.Errorf("%w: request_id is invalid", errWebFSInvalid))
		return
	}
	destination, err := s.resolveWebFSPath(userID, input.Path, input.Workspace, true, false, false)
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	if _, err := os.Lstat(destination.Target); err == nil {
		writeWebFSError(w, errWebFSConflict)
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		writeWebFSError(w, err)
		return
	}
	var sourceRoot string
	if strings.TrimSpace(input.SourceRoot) != "" {
		source, err := s.resolveWebFSPath(userID, input.SourceRoot, "", false, true, allowLocalRead)
		if err != nil {
			writeWebFSError(w, err)
			return
		}
		info, err := os.Stat(source.Target)
		if err != nil || !info.IsDir() {
			writeWebFSError(w, fmt.Errorf("%w: source_root must be an existing directory", errWebFSInvalid))
			return
		}
		sourceRoot = source.Target
	}

	ctx, done, err := s.beginWebZip(r.Context(), input.RequestID)
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	defer done()
	if err := os.MkdirAll(filepath.Dir(destination.Target), 0o700); err != nil {
		writeWebFSError(w, err)
		return
	}
	temp, err := os.CreateTemp(filepath.Dir(destination.Target), ".synon-zip-*")
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	writer := zip.NewWriter(temp)
	closed := false
	defer func() {
		if !closed {
			_ = writer.Close()
			_ = temp.Close()
		}
	}()

	seenNames := make(map[string]struct{}, len(input.Files))
	var totalBytes int64
	for _, entry := range input.Files {
		if err := ctx.Err(); err != nil {
			writeWebFSError(w, errWebFSCanceled)
			return
		}
		name, err := normalizeWebFSZipName(entry.Name)
		if err != nil {
			writeWebFSError(w, err)
			return
		}
		if _, exists := seenNames[name]; exists {
			writeWebFSError(w, fmt.Errorf("%w: duplicate zip entry %q", errWebFSConflict, name))
			return
		}
		seenNames[name] = struct{}{}
		sourcePath := strings.TrimSpace(firstNonEmpty(entry.SourcePath, entry.SourcePathCamel))
		if entry.Content.Present == (sourcePath != "") {
			writeWebFSError(w, fmt.Errorf("%w: zip entry %q requires exactly one of content or source_path", errWebFSInvalid, name))
			return
		}
		header := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Now().UTC()}
		header.SetMode(0o600)
		entryWriter, err := writer.CreateHeader(header)
		if err != nil {
			writeWebFSError(w, err)
			return
		}
		if entry.Content.Present {
			if int64(len(entry.Content.Data)) > maxWebFSCopyBytes-totalBytes {
				writeWebFSError(w, errWebFSTooLarge)
				return
			}
			if err := writeWebFSZipBytes(ctx, entryWriter, entry.Content.Data); err != nil {
				writeWebFSError(w, err)
				return
			}
			totalBytes += int64(len(entry.Content.Data))
			continue
		}

		source, err := s.resolveWebFSPath(userID, sourcePath, sourceRoot, false, true, allowLocalRead)
		if err != nil {
			writeWebFSError(w, err)
			return
		}
		info, err := os.Lstat(source.Target)
		if err != nil {
			writeWebFSError(w, err)
			return
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			writeWebFSError(w, errWebFSUnsupported)
			return
		}
		if info.Size() < 0 || info.Size() > maxWebFSCopyBytes-totalBytes {
			writeWebFSError(w, errWebFSTooLarge)
			return
		}
		file, err := os.Open(source.Target)
		if err != nil {
			writeWebFSError(w, err)
			return
		}
		written, copyErr := copyWebFSZipStream(ctx, entryWriter, file, info.Size())
		closeErr := file.Close()
		if copyErr != nil {
			writeWebFSError(w, copyErr)
			return
		}
		if closeErr != nil {
			writeWebFSError(w, closeErr)
			return
		}
		totalBytes += written
	}
	if err := writer.Close(); err != nil {
		writeWebFSError(w, err)
		return
	}
	if err := temp.Sync(); err != nil {
		writeWebFSError(w, err)
		return
	}
	if err := temp.Close(); err != nil {
		writeWebFSError(w, err)
		return
	}
	closed = true
	if ctx.Err() != nil {
		writeWebFSError(w, errWebFSCanceled)
		return
	}
	if err := os.Rename(tempPath, destination.Target); err != nil {
		writeWebFSError(w, err)
		return
	}
	fileevents.NotifyChanged(destination.Target)
	writeWorkspaceJSON(w, http.StatusOK, true)
}

func (s *Server) handleWebZipCancel(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RequestID string `json:"request_id"`
	}
	if err := decodeWebFSJSON(r, &input); err != nil {
		writeWebFSError(w, err)
		return
	}
	if !validWebFSRequestID(input.RequestID) {
		writeWebFSError(w, fmt.Errorf("%w: request_id is required", errWebFSInvalid))
		return
	}
	s.webZipMu.Lock()
	cancel, found := s.webZipCancels[input.RequestID]
	s.webZipMu.Unlock()
	if found {
		cancel()
	}
	writeWorkspaceJSON(w, http.StatusOK, found)
}

func (s *Server) beginWebZip(parent context.Context, requestID string) (context.Context, func(), error) {
	ctx, cancel := context.WithCancel(parent)
	if requestID == "" {
		return ctx, cancel, nil
	}
	s.webZipMu.Lock()
	if _, exists := s.webZipCancels[requestID]; exists {
		s.webZipMu.Unlock()
		cancel()
		return nil, nil, fmt.Errorf("%w: request_id is already active", errWebFSConflict)
	}
	s.webZipCancels[requestID] = cancel
	s.webZipMu.Unlock()
	return ctx, func() {
		cancel()
		s.webZipMu.Lock()
		delete(s.webZipCancels, requestID)
		s.webZipMu.Unlock()
	}, nil
}

func normalizeWebFSZipName(value string) (string, error) {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	if value == "" || strings.ContainsRune(value, 0) || strings.HasPrefix(value, "/") || len(value) > 1024 {
		return "", fmt.Errorf("%w: zip entry name is invalid", errWebFSInvalid)
	}
	cleaned := path.Clean(value)
	first := strings.SplitN(cleaned, "/", 2)[0]
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(first, ":") {
		return "", fmt.Errorf("%w: zip entry escapes the archive root", errWebFSInvalid)
	}
	return cleaned, nil
}

func validWebFSRequestID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if char < 33 || char > 126 {
			return false
		}
	}
	return true
}

func writeWebFSZipBytes(ctx context.Context, destination io.Writer, data []byte) error {
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return errWebFSCanceled
		}
		size := min(len(data), 64<<10)
		if _, err := destination.Write(data[:size]); err != nil {
			return err
		}
		data = data[size:]
	}
	return nil
}

func copyWebFSZipStream(ctx context.Context, destination io.Writer, source io.Reader, expected int64) (int64, error) {
	buffer := make([]byte, 64<<10)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, errWebFSCanceled
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			next := written + int64(count)
			if next > expected {
				return written, errWebFSTooLarge
			}
			outputCount, writeErr := destination.Write(buffer[:count])
			written += int64(outputCount)
			if writeErr != nil {
				return written, writeErr
			}
			if outputCount != count {
				return written, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return written, readErr
		}
	}
	if written != expected {
		return written, errors.New("zip source changed while it was read")
	}
	return written, nil
}
