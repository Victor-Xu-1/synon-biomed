package server

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func (s *Server) handleWebFSDir(w http.ResponseWriter, r *http.Request, userID string, allowLocalRead bool) {
	var input struct {
		Dir  string `json:"dir"`
		Root string `json:"root"`
	}
	if err := decodeWebFSJSON(r, &input); err != nil {
		writeWebFSError(w, err)
		return
	}
	if strings.TrimSpace(input.Root) == "" {
		writeWebFSError(w, fmt.Errorf("%w: root is required", errWebFSInvalid))
		return
	}
	access, err := s.resolveWebFSPath(userID, input.Dir, input.Root, false, true, allowLocalRead)
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	info, err := os.Stat(access.Target)
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	if !info.IsDir() {
		writeWebFSError(w, fmt.Errorf("%w: dir must reference a directory", errWebFSInvalid))
		return
	}
	count := 0
	nodes, err := buildWebFSTree(access.Root, access.Target, &count)
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, nodes)
}

func (s *Server) handleWebFSList(w http.ResponseWriter, r *http.Request, userID string, allowLocalRead bool) {
	var input struct {
		Root string `json:"root"`
	}
	if err := decodeWebFSJSON(r, &input); err != nil {
		writeWebFSError(w, err)
		return
	}
	if strings.TrimSpace(input.Root) == "" {
		writeWebFSError(w, fmt.Errorf("%w: root is required", errWebFSInvalid))
		return
	}
	access, err := s.resolveWebFSPath(userID, input.Root, input.Root, false, true, allowLocalRead)
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	files, err := listWebFSFiles(access.Root, access.Target)
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, files)
}

func (s *Server) handleWebFSRead(
	w http.ResponseWriter,
	r *http.Request,
	userID string,
	allowLocalRead bool,
	buffer bool,
) {
	var input struct {
		Path      string `json:"path"`
		Workspace string `json:"workspace,omitempty"`
	}
	if err := decodeWebFSJSON(r, &input); err != nil {
		writeWebFSError(w, err)
		return
	}
	access, err := s.resolveWebFSPath(userID, input.Path, input.Workspace, false, true, allowLocalRead)
	if errors.Is(err, os.ErrNotExist) {
		writeWorkspaceJSON(w, http.StatusOK, nil)
		return
	}
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	data, err := readWebFSFile(access.Target, maxWebFSReadBytes)
	if errors.Is(err, os.ErrNotExist) {
		writeWorkspaceJSON(w, http.StatusOK, nil)
		return
	}
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	if buffer {
		writeWorkspaceJSON(w, http.StatusOK, base64.StdEncoding.EncodeToString(data))
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, string(data))
}

func (s *Server) handleWebFSImage(w http.ResponseWriter, r *http.Request, userID string, allowLocalRead bool) {
	var input struct {
		Path      string `json:"path"`
		Workspace string `json:"workspace,omitempty"`
	}
	if err := decodeWebFSJSON(r, &input); err != nil {
		writeWebFSError(w, err)
		return
	}
	access, err := s.resolveWebFSPath(userID, input.Path, input.Workspace, false, true, allowLocalRead)
	if errors.Is(err, os.ErrNotExist) {
		writeWorkspaceJSON(w, http.StatusOK, nil)
		return
	}
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	data, err := readWebFSFile(access.Target, maxWebFSReadBytes)
	if errors.Is(err, os.ErrNotExist) {
		writeWorkspaceJSON(w, http.StatusOK, nil)
		return
	}
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	mediaType, err := webFSImageMediaType(data, filepath.Ext(access.Target), "")
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, webFSDataURL(mediaType, data))
}

func (s *Server) handleWebFSRemoteImage(w http.ResponseWriter, r *http.Request) {
	var input struct {
		URL string `json:"url"`
	}
	if err := decodeWebFSJSON(r, &input); err != nil {
		writeWebFSError(w, err)
		return
	}
	parsed, err := url.Parse(strings.TrimSpace(input.URL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		writeWebFSError(w, fmt.Errorf("%w: a public HTTPS URL without credentials is required", errWebFSInvalid))
		return
	}
	clientForURL := s.webImageClientForURL
	if clientForURL == nil {
		writeWebFSError(w, fmt.Errorf("%w: secure HTTP client is unavailable", errWebFSUpstream))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), webFSRemoteTimeout)
	defer cancel()
	client, err := clientForURL(ctx, parsed.String(), s.httpClient)
	if err != nil {
		writeWebFSError(w, fmt.Errorf("%w: %v", errWebFSForbidden, err))
		return
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		writeWebFSError(w, fmt.Errorf("%w: %v", errWebFSInvalid, err))
		return
	}
	request.Header.Set("Accept", "image/avif,image/webp,image/png,image/jpeg,image/gif,image/svg+xml,image/*;q=0.8")
	response, err := client.Do(request)
	if err != nil {
		writeWebFSError(w, fmt.Errorf("%w: %v", errWebFSUpstream, err))
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		writeWebFSError(w, fmt.Errorf("%w: upstream returned HTTP %d", errWebFSUpstream, response.StatusCode))
		return
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxWebFSImageBytes+1))
	if err != nil {
		writeWebFSError(w, fmt.Errorf("%w: %v", errWebFSUpstream, err))
		return
	}
	if len(data) > maxWebFSImageBytes {
		writeWebFSError(w, errWebFSTooLarge)
		return
	}
	mediaType, err := webFSImageMediaType(data, filepath.Ext(parsed.Path), response.Header.Get("Content-Type"))
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, webFSDataURL(mediaType, data))
}
