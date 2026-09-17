package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
)

var (
	errWebShellUnavailable = errors.New("desktop shell integration is unavailable")
	webShellToolPattern    = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,64}$`)
)

type webShellLauncher func(context.Context, string, string, string) error

func (s *Server) handleWebShell(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWebFSError(w, fmt.Errorf("%w: POST required", errWebFSMethod))
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	allowLocalRead := (s.synonLinkAuth == nil || !s.synonLinkAuth.enabled) && isLoopbackRequest(r)

	switch r.URL.Path {
	case "/api/shell/check-tool-installed":
		var input struct {
			Tool string `json:"tool"`
		}
		if err := decodeWebFSJSON(r, &input); err != nil {
			writeWebShellError(w, err)
			return
		}
		tool := strings.TrimSpace(input.Tool)
		if !webShellToolPattern.MatchString(tool) {
			writeWebShellError(w, fmt.Errorf("%w: tool is invalid", errWebFSInvalid))
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, webShellToolInstalled(tool))
	case "/api/shell/open-external":
		var input struct {
			URL string `json:"url"`
		}
		if err := decodeWebFSJSON(r, &input); err != nil {
			writeWebShellError(w, err)
			return
		}
		target, err := validateWebShellExternalURL(input.URL)
		if err != nil {
			writeWebShellError(w, err)
			return
		}
		if err := s.launchWebShell(r.Context(), "open-external", target, ""); err != nil {
			writeWebShellError(w, err)
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, nil)
	case "/api/shell/open-file", "/api/shell/show-item-in-folder":
		s.handleWebShellFile(w, r, userID, allowLocalRead)
	case "/api/shell/open-folder-with":
		s.handleWebShellFolder(w, r, userID, allowLocalRead)
	default:
		writeWebShellError(w, os.ErrNotExist)
	}
}

func (s *Server) handleWebShellFile(
	w http.ResponseWriter,
	r *http.Request,
	userID string,
	allowLocalRead bool,
) {
	var input struct {
		FilePath string `json:"file_path"`
	}
	if err := decodeWebFSJSON(r, &input); err != nil {
		writeWebShellError(w, err)
		return
	}
	access, err := s.resolveWebFSPath(userID, input.FilePath, "", false, true, allowLocalRead)
	if err != nil {
		writeWebShellError(w, err)
		return
	}
	info, err := os.Stat(access.Target)
	if err != nil {
		writeWebShellError(w, err)
		return
	}
	action := "show-item-in-folder"
	if r.URL.Path == "/api/shell/open-file" {
		if !info.Mode().IsRegular() {
			writeWebShellError(w, fmt.Errorf("%w: file_path must reference a regular file", errWebFSUnsupported))
			return
		}
		action = "open-file"
	}
	if err := s.launchWebShell(r.Context(), action, access.Target, ""); err != nil {
		writeWebShellError(w, err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, nil)
}

func (s *Server) handleWebShellFolder(
	w http.ResponseWriter,
	r *http.Request,
	userID string,
	allowLocalRead bool,
) {
	var input struct {
		FolderPath string `json:"folder_path"`
		Tool       string `json:"tool"`
	}
	if err := decodeWebFSJSON(r, &input); err != nil {
		writeWebShellError(w, err)
		return
	}
	tool := strings.ToLower(strings.TrimSpace(input.Tool))
	if tool != "vscode" && tool != "terminal" && tool != "explorer" {
		writeWebShellError(w, fmt.Errorf("%w: tool must be vscode, terminal, or explorer", errWebFSInvalid))
		return
	}
	access, err := s.resolveWebFSPath(userID, input.FolderPath, "", false, true, allowLocalRead)
	if err != nil {
		writeWebShellError(w, err)
		return
	}
	info, err := os.Stat(access.Target)
	if err != nil {
		writeWebShellError(w, err)
		return
	}
	if !info.IsDir() {
		writeWebShellError(w, fmt.Errorf("%w: folder_path must reference a directory", errWebFSUnsupported))
		return
	}
	if err := s.launchWebShell(r.Context(), "open-folder-with", access.Target, tool); err != nil {
		writeWebShellError(w, err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, nil)
}

func (s *Server) launchWebShell(ctx context.Context, action, target, tool string) error {
	launcher := s.webShellLauncher
	if launcher == nil {
		launcher = defaultWebShellLauncher
	}
	if err := launcher(ctx, action, target, tool); err != nil {
		return fmt.Errorf("%w: %v", errWebShellUnavailable, err)
	}
	return nil
}

func validateWebShellExternalURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 8192 || strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("%w: url is invalid", errWebFSInvalid)
	}
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() {
		return "", fmt.Errorf("%w: url must be absolute", errWebFSInvalid)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		if parsed.Hostname() == "" || parsed.User != nil {
			return "", fmt.Errorf("%w: web URL host is invalid", errWebFSInvalid)
		}
	case "mailto":
		if parsed.Opaque == "" && parsed.Path == "" {
			return "", fmt.Errorf("%w: mailto recipient is required", errWebFSInvalid)
		}
	default:
		return "", fmt.Errorf("%w: URL scheme is not allowed", errWebFSInvalid)
	}
	return parsed.String(), nil
}

func writeWebShellError(w http.ResponseWriter, err error) {
	if errors.Is(err, errWebShellUnavailable) {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok": false, "error": err.Error(), "code": "SHELL_UNAVAILABLE",
		})
		return
	}
	writeWebFSError(w, err)
}
