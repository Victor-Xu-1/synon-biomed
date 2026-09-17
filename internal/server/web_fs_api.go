package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	maxWebFSRequestBytes = 36 << 20
	maxWebFSReadBytes    = 32 << 20
	maxWebFSImageBytes   = 12 << 20
	maxWebFSTreeEntries  = 20_000
	maxWebFSCopyEntries  = 20_000
	maxWebFSCopyBytes    = int64(1 << 30)
	maxWebFSCopySources  = 256
	webFSRemoteTimeout   = 20 * time.Second
)

var (
	errWebFSForbidden   = errors.New("path is outside an authorized directory")
	errWebFSInvalid     = errors.New("invalid file-system request")
	errWebFSTooLarge    = errors.New("file-system payload exceeds the configured limit")
	errWebFSUnsupported = errors.New("unsupported file-system entry")
	errWebFSTooMany     = errors.New("file-system entry limit exceeded")
	errWebFSConflict    = errors.New("file-system destination already exists")
	errWebFSUpstream    = errors.New("remote image request failed")
	errWebFSMethod      = errors.New("file-system method is not allowed")
	errWebFSCanceled    = errors.New("file-system operation was canceled")
)

type webFSRoot struct {
	Path     string
	Writable bool
}

type webFSAccess struct {
	Root   string
	Target string
}

type webFSNode struct {
	Name         string      `json:"name"`
	FullPath     string      `json:"fullPath"`
	RelativePath string      `json:"relativePath"`
	IsDir        bool        `json:"isDir"`
	IsFile       bool        `json:"isFile"`
	Children     []webFSNode `json:"children,omitempty"`
}

type webFSFlatFile struct {
	Name         string `json:"name"`
	FullPath     string `json:"full_path"`
	RelativePath string `json:"relative_path"`
}

type webFSCopyState struct {
	entries int
	bytes   int64
}

func (s *Server) handleWebFS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWebFSError(w, fmt.Errorf("%w: POST required", errWebFSMethod))
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWebFSRequestBytes)
	allowLocalRead := (s.synonLinkAuth == nil || !s.synonLinkAuth.enabled) && isLoopbackRequest(r)

	switch r.URL.Path {
	case "/api/fs/dir":
		s.handleWebFSDir(w, r, userID, allowLocalRead)
	case "/api/fs/list":
		s.handleWebFSList(w, r, userID, allowLocalRead)
	case "/api/fs/image-base64":
		s.handleWebFSImage(w, r, userID, allowLocalRead)
	case "/api/fs/fetch-remote-image":
		s.handleWebFSRemoteImage(w, r)
	case "/api/fs/read":
		s.handleWebFSRead(w, r, userID, allowLocalRead, false)
	case "/api/fs/read-buffer":
		s.handleWebFSRead(w, r, userID, allowLocalRead, true)
	case "/api/fs/temp":
		s.handleWebFSTemp(w, r, userID)
	case "/api/fs/write":
		s.handleWebFSWrite(w, r, userID)
	case "/api/fs/metadata":
		s.handleWebFSMetadata(w, r, userID, allowLocalRead)
	case "/api/fs/copy":
		s.handleWebFSCopy(w, r, userID, allowLocalRead)
	case "/api/fs/remove":
		s.handleWebFSRemove(w, r, userID)
	case "/api/fs/rename":
		s.handleWebFSRename(w, r, userID)
	default:
		writeWebFSError(w, os.ErrNotExist)
	}
}

func decodeWebFSJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return errWebFSTooLarge
		}
		return fmt.Errorf("%w: %v", errWebFSInvalid, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%w: request body must contain one JSON value", errWebFSInvalid)
		}
		return fmt.Errorf("%w: %v", errWebFSInvalid, err)
	}
	return nil
}

func writeWebFSError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	code := "FS_INTERNAL"
	switch {
	case errors.Is(err, errWebFSForbidden):
		status, code = http.StatusForbidden, "FS_FORBIDDEN"
	case errors.Is(err, errWebFSInvalid):
		status, code = http.StatusBadRequest, "FS_INVALID"
	case errors.Is(err, errWebFSTooLarge):
		status, code = http.StatusRequestEntityTooLarge, "FS_TOO_LARGE"
	case errors.Is(err, errWebFSUnsupported):
		status, code = http.StatusUnsupportedMediaType, "FS_UNSUPPORTED"
	case errors.Is(err, errWebFSTooMany):
		status, code = http.StatusUnprocessableEntity, "FS_TOO_MANY_ENTRIES"
	case errors.Is(err, errWebFSConflict), errors.Is(err, os.ErrExist), strings.Contains(strings.ToLower(err.Error()), "exists"):
		status, code = http.StatusConflict, "FS_CONFLICT"
	case errors.Is(err, os.ErrNotExist):
		status, code = http.StatusNotFound, "FS_NOT_FOUND"
	case errors.Is(err, os.ErrPermission):
		status, code = http.StatusForbidden, "FS_FORBIDDEN"
	case errors.Is(err, errWebFSUpstream):
		status, code = http.StatusBadGateway, "FS_UPSTREAM"
	case errors.Is(err, errWebFSMethod):
		status, code = http.StatusMethodNotAllowed, "FS_METHOD_NOT_ALLOWED"
	case errors.Is(err, errWebFSCanceled), errors.Is(err, context.Canceled):
		status, code = http.StatusRequestTimeout, "FS_CANCELED"
	case strings.Contains(strings.ToLower(err.Error()), "escapes file root"):
		status, code = http.StatusForbidden, "FS_FORBIDDEN"
	}
	writeWorkspaceJSON(w, status, map[string]any{"error": err.Error(), "code": code})
}
