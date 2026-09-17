package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxWebFSUploadFileBytes     int64 = maxWebConversationInputFileBytes
	maxWebFSUploadRequestBytes  int64 = maxWebFSUploadFileBytes + (1 << 20)
	maxWebFSUploadFilenameBytes       = 180
	maxWebFSUploadMemoryBytes   int64 = 1 << 20
)

// handleWebFSUpload is the single browser-upload boundary used by the
// composer, paste, drag-and-drop, and rich-document resource flows. Uploaded
// bytes are stored in the authenticated user's private Web-FS root and are
// later materialized into the selected task workspace by the conversation
// input-file authority.
func (s *Server) handleWebFSUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWebFSError(w, fmt.Errorf("%w: POST required", errWebFSMethod))
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWebFSUploadRequestBytes)
	if err := r.ParseMultipartForm(maxWebFSUploadMemoryBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeWebFSError(w, errWebFSTooLarge)
			return
		}
		writeWebFSError(w, fmt.Errorf("%w: invalid multipart upload", errWebFSInvalid))
		return
	}
	if r.MultipartForm == nil {
		writeWebFSError(w, fmt.Errorf("%w: multipart form is required", errWebFSInvalid))
		return
	}
	defer r.MultipartForm.RemoveAll()

	if err := validateWebFSUploadFields(r); err != nil {
		writeWebFSError(w, err)
		return
	}
	conversationID := firstWebFSUploadValue(r.MultipartForm.Value["conversation_id"])
	if conversationID != "" {
		if _, _, allowed := s.webConversationAccess(w, r, conversationID); !allowed {
			return
		}
	}

	header := r.MultipartForm.File["file"][0]
	fileName, err := validWebFSUploadFilename(header.Filename, firstWebFSUploadValue(r.MultipartForm.Value["file_name"]))
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	if header.Size < 0 || header.Size > maxWebFSUploadFileBytes {
		writeWebFSError(w, errWebFSTooLarge)
		return
	}
	source, err := header.Open()
	if err != nil {
		writeWebFSError(w, fmt.Errorf("open multipart upload: %w", err))
		return
	}
	defer source.Close()

	root, err := s.webFSTempRoot(userID)
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	path, err := storeWebFSUpload(root, fileName, source)
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"success": true, "data": path})
}

func validateWebFSUploadFields(r *http.Request) error {
	form := r.MultipartForm
	if len(form.File) != 1 || len(form.File["file"]) != 1 {
		return fmt.Errorf("%w: multipart field file is required exactly once", errWebFSInvalid)
	}
	for key := range form.File {
		if key != "file" {
			return fmt.Errorf("%w: unexpected multipart file field", errWebFSInvalid)
		}
	}
	for key, values := range form.Value {
		if key != "file_name" && key != "conversation_id" {
			return fmt.Errorf("%w: unexpected multipart value field", errWebFSInvalid)
		}
		if len(values) > 1 {
			return fmt.Errorf("%w: multipart value field may appear at most once", errWebFSInvalid)
		}
	}
	return nil
}

func firstWebFSUploadValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

func validWebFSUploadFilename(headerName, override string) (string, error) {
	name := strings.TrimSpace(override)
	if name == "" {
		name = strings.TrimSpace(headerName)
	}
	if name == "" || len(name) > maxWebFSUploadFilenameBytes || !utf8.ValidString(name) ||
		name == "." || name == ".." || name != filepath.Base(name) || strings.ContainsAny(name, "/\\") {
		return "", fmt.Errorf("%w: file_name must be a plain UTF-8 file name", errWebFSInvalid)
	}
	for _, value := range name {
		if unicode.IsControl(value) {
			return "", fmt.Errorf("%w: file_name contains a control character", errWebFSInvalid)
		}
	}
	return name, nil
}

func storeWebFSUpload(root, fileName string, source io.Reader) (string, error) {
	temporary, err := os.CreateTemp(root, ".incoming-upload-*")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()

	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(source, maxWebFSUploadFileBytes+1))
	if err != nil {
		return "", err
	}
	if written > maxWebFSUploadFileBytes {
		return "", errWebFSTooLarge
	}
	if err := temporary.Sync(); err != nil {
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(temporaryPath, 0o600); err != nil {
		return "", err
	}

	sum := hex.EncodeToString(hash.Sum(nil))
	contentRoot := filepath.Join(root, "uploads", sum)
	if err := os.MkdirAll(contentRoot, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(contentRoot, 0o700); err != nil {
		return "", err
	}
	destination := filepath.Join(contentRoot, fileName)
	if info, statErr := os.Lstat(destination); statErr == nil {
		if !info.Mode().IsRegular() {
			return "", errWebFSConflict
		}
		existing, hashErr := fileSHA256(destination)
		if hashErr != nil {
			return "", hashErr
		}
		if existing != sum {
			return "", errWebFSConflict
		}
		return filepath.Clean(destination), nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return "", err
	}
	committed = true
	return filepath.Clean(destination), nil
}
