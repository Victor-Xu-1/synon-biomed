package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"synon-go/internal/subprocess"
)

type webOfficeWatch struct {
	UserID   string
	Kind     string
	FilePath string
	Port     int
	Command  *exec.Cmd
	Control  *subprocess.Control
	Done     chan error
	Stdout   *cappedCommandBuffer
	Stderr   *cappedCommandBuffer
}

func (s *Server) handleWebOfficePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWebFSError(w, fmt.Errorf("%w: POST required", errWebFSMethod))
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	kind, start, ok := webOfficeRoute(r.URL.Path)
	if !ok {
		writeWebFSError(w, os.ErrNotExist)
		return
	}
	var input struct {
		FilePath      string `json:"file_path,omitempty"`
		FilePathCamel string `json:"filePath,omitempty"`
		ArtifactID    string `json:"artifact_id,omitempty"`
		VersionID     string `json:"version_id,omitempty"`
		Workspace     string `json:"workspace,omitempty"`
	}
	if err := decodeWebFSJSON(r, &input); err != nil {
		writeWebOfficeError(w, err)
		return
	}
	filePath := firstNonEmpty(input.FilePath, input.FilePathCamel)
	artifactID, versionID := strings.TrimSpace(input.ArtifactID), strings.TrimSpace(input.VersionID)
	if artifactID != "" {
		if filePath != "" {
			writeWebOfficeError(w, fmt.Errorf("%w: provide file_path or artifact_id, not both", errWebFSInvalid))
			return
		}
		resolvedPath, resolveErr := s.resolveOwnedArtifactOfficePath(r.Context(), userID, artifactID, versionID)
		if resolveErr != nil {
			writeWebOfficeError(w, resolveErr)
			return
		}
		filePath = resolvedPath
	} else {
		allowLocalRead := (s.synonLinkAuth == nil || !s.synonLinkAuth.enabled) && isLoopbackRequest(r)
		access, resolveErr := s.resolveWebFSPath(userID, filePath, input.Workspace, false, true, allowLocalRead)
		if resolveErr != nil {
			writeWebOfficeError(w, resolveErr)
			return
		}
		filePath = access.Target
	}
	if start {
		s.handleWebOfficeStart(w, r, userID, kind, filePath)
		return
	}
	if err := s.stopWebOfficeWatch(r.Context(), webOfficeWatchKey(userID, kind, filePath)); err != nil {
		writeWebOfficeError(w, err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, nil)
}

func (s *Server) resolveOwnedArtifactOfficePath(
	ctx context.Context, userID, artifactID, versionID string,
) (string, error) {
	if s.workspaceStore == nil {
		return "", os.ErrNotExist
	}
	artifact, found, err := s.workspaceStore.GetCompatibilityArtifactMetadata(ctx, userID, artifactID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", os.ErrNotExist
	}
	if versionID == "" || versionID == artifact.VersionID {
		return materializeWebOfficeArtifact(userID, artifact.ID, artifact.VersionID, artifact.Filename, artifact.FilePath)
	}
	versions, found, err := s.workspaceStore.ListCompatibilityArtifactVersions(ctx, userID, artifactID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", os.ErrNotExist
	}
	for _, version := range versions {
		if version.VersionID == versionID && strings.TrimSpace(version.FilePath) != "" {
			return materializeWebOfficeArtifact(userID, artifact.ID, version.VersionID, artifact.Filename, version.FilePath)
		}
	}
	return "", os.ErrNotExist
}

func materializeWebOfficeArtifact(userID, artifactID, versionID, filename, sourcePath string) (string, error) {
	if strings.TrimSpace(sourcePath) == "" {
		return "", fmt.Errorf("%w: artifact version has no previewable file", errWebFSUnsupported)
	}
	filename = filepath.Base(strings.TrimSpace(filename))
	if filename == "" || filename == "." || filename == string(filepath.Separator) {
		return "", fmt.Errorf("%w: artifact filename is invalid", errWebFSInvalid)
	}
	digest := sha256.Sum256([]byte(strings.Join([]string{userID, artifactID, versionID}, "\x00")))
	directory := filepath.Join(os.TempDir(), "synon-biomed-office-cache", fmt.Sprintf("%x", digest[:16]))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	targetPath := filepath.Join(directory, filename)
	if info, err := os.Stat(targetPath); err == nil && info.Mode().IsRegular() {
		return targetPath, nil
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return "", err
	}
	defer source.Close()
	temporary, err := os.CreateTemp(directory, ".materialize-*")
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
	if err := temporary.Chmod(0o600); err != nil {
		return "", err
	}
	written, err := io.Copy(temporary, io.LimitReader(source, (512<<20)+1))
	if err != nil {
		return "", err
	}
	if written > 512<<20 {
		return "", fmt.Errorf("%w: artifact exceeds the office preview size limit", errWebFSUnsupported)
	}
	if err := temporary.Sync(); err != nil {
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(temporaryPath, targetPath); err != nil {
		if _, statErr := os.Stat(targetPath); statErr != nil {
			return "", err
		}
	}
	committed = true
	return targetPath, nil
}

func webOfficeRoute(route string) (kind string, start bool, ok bool) {
	switch route {
	case "/api/word-preview/start":
		return "word", true, true
	case "/api/word-preview/stop":
		return "word", false, true
	case "/api/excel-preview/start":
		return "excel", true, true
	case "/api/excel-preview/stop":
		return "excel", false, true
	case "/api/ppt-preview/start":
		return "ppt", true, true
	case "/api/ppt-preview/stop":
		return "ppt", false, true
	default:
		return "", false, false
	}
}

func (s *Server) handleWebOfficeStart(
	w http.ResponseWriter,
	r *http.Request,
	userID string,
	kind string,
	filePath string,
) {
	if err := validateWebOfficeFile(kind, filePath); err != nil {
		writeWebOfficeError(w, err)
		return
	}
	watch, code, err := s.startWebOfficeWatch(r.Context(), userID, kind, filePath)
	if code != "" {
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"url": "", "error": code})
		return
	}
	if err != nil {
		writeWebOfficeError(w, err)
		return
	}
	proxy := "office-watch-proxy"
	if kind == "ppt" {
		proxy = "ppt-proxy"
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"url": fmt.Sprintf("/api/%s/%d", proxy, watch.Port),
	})
}

func validateWebOfficeFile(kind string, filePath string) error {
	info, err := os.Stat(filePath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: file_path must reference a regular file", errWebFSUnsupported)
	}
	extension := strings.ToLower(filepath.Ext(filePath))
	valid := false
	switch kind {
	case "word":
		valid = extension == ".docx" || extension == ".doc" || extension == ".odt"
	case "excel":
		valid = extension == ".xlsx" || extension == ".xls" || extension == ".ods" || extension == ".csv"
	case "ppt":
		valid = extension == ".pptx" || extension == ".ppt" || extension == ".odp"
	}
	if !valid {
		return fmt.Errorf("%w: file extension is not supported for %s preview", errWebFSInvalid, kind)
	}
	return nil
}

func webOfficeWatchKey(userID, kind, filePath string) string {
	return strings.TrimSpace(userID) + "\x00" + kind + "\x00" + filepath.Clean(filePath)
}

func resolveWebOfficeCLI() (string, []string, error) {
	configured := strings.TrimSpace(os.Getenv("SYNON_OFFICECLI_PATH"))
	path := ""
	var err error
	if configured != "" {
		if filepath.IsAbs(configured) {
			path = filepath.Clean(configured)
			if info, statErr := os.Stat(path); statErr != nil || !info.Mode().IsRegular() {
				return "", nil, errors.New("configured OfficeCLI path is not a regular file")
			}
		} else {
			path, err = exec.LookPath(configured)
		}
	} else {
		path, err = exec.LookPath("officecli")
	}
	if err != nil || path == "" {
		return "", nil, errors.New("OfficeCLI is not installed or not on PATH")
	}
	prefix := []string{}
	if raw := strings.TrimSpace(os.Getenv("SYNON_OFFICECLI_ARGS_JSON")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &prefix); err != nil {
			return "", nil, fmt.Errorf("decode SYNON_OFFICECLI_ARGS_JSON: %w", err)
		}
		if len(prefix) > 32 {
			return "", nil, errors.New("configured OfficeCLI prefix has too many arguments")
		}
		for _, argument := range prefix {
			if len(argument) > 8192 || strings.ContainsRune(argument, 0) {
				return "", nil, errors.New("configured OfficeCLI prefix argument is invalid")
			}
		}
	}
	return path, prefix, nil
}

func reserveWebOfficePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return 0, err
	}
	if port < 1024 || port > 65535 {
		return 0, errors.New("operating system returned an invalid preview port")
	}
	return port, nil
}

func (s *Server) startWebOfficeWatch(
	ctx context.Context,
	userID string,
	kind string,
	filePath string,
) (*webOfficeWatch, string, error) {
	key := webOfficeWatchKey(userID, kind, filePath)
	s.webOfficeMu.Lock()
	existing := s.webOfficeWatches[key]
	if existing != nil {
		select {
		case <-existing.Done:
			delete(s.webOfficeWatches, key)
			_ = existing.Control.Close()
			existing = nil
		default:
		}
	}
	s.webOfficeMu.Unlock()
	if existing != nil {
		return existing, "", nil
	}

	executable, prefix, err := resolveWebOfficeCLI()
	if err != nil {
		return nil, "OFFICECLI_NOT_FOUND", nil
	}
	port, err := reserveWebOfficePort()
	if err != nil {
		return nil, "OFFICECLI_START_FAILED", err
	}
	arguments := append(append([]string(nil), prefix...),
		"watch", filePath, "--port", strconv.Itoa(port),
	)
	command := exec.CommandContext(context.Background(), executable, arguments...)
	command.Dir = filepath.Dir(filePath)
	command.Env = append(os.Environ(), "OFFICECLI_SKIP_UPDATE=1", "BROWSER=none")
	stdout := &cappedCommandBuffer{max: 256 << 10}
	stderr := &cappedCommandBuffer{max: 256 << 10}
	command.Stdout = stdout
	command.Stderr = stderr
	control, err := subprocess.Prepare(command)
	if err != nil {
		return nil, "OFFICECLI_START_FAILED", err
	}
	if err := command.Start(); err != nil {
		_ = control.Close()
		return nil, "OFFICECLI_START_FAILED", err
	}
	if err := control.Attach(command); err != nil {
		_ = control.Kill(command)
		_ = command.Wait()
		_ = control.Close()
		return nil, "OFFICECLI_START_FAILED", err
	}
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
		close(done)
	}()
	watch := &webOfficeWatch{
		UserID: userID, Kind: kind, FilePath: filePath, Port: port,
		Command: command, Control: control, Done: done, Stdout: stdout, Stderr: stderr,
	}
	if err := waitWebOfficeReady(ctx, watch); err != nil {
		stopWebOfficeProcess(context.Background(), watch)
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, "OFFICECLI_PORT_TIMEOUT", err
		}
		return nil, "OFFICECLI_START_FAILED", err
	}
	return s.registerWebOfficeWatch(key, watch), "", nil
}

func waitWebOfficeReady(parent context.Context, watch *webOfficeWatch) error {
	ctx, cancel := context.WithTimeout(parent, 12*time.Second)
	defer cancel()
	transport := &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout: 500 * time.Millisecond,
		}).DialContext,
		DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/", watch.Port)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		response, err := client.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
			_ = response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 500 {
				return nil
			}
		}
		select {
		case processErr := <-watch.Done:
			detail := strings.TrimSpace(firstNonEmpty(watch.Stderr.String(), watch.Stdout.String()))
			if detail == "" && processErr != nil {
				detail = processErr.Error()
			}
			if detail == "" {
				detail = "OfficeCLI watch process exited before becoming ready"
			}
			return errors.New(detail)
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Server) registerWebOfficeWatch(key string, watch *webOfficeWatch) *webOfficeWatch {
	s.webOfficeMu.Lock()
	existing := s.webOfficeWatches[key]
	if existing != nil {
		select {
		case <-existing.Done:
			delete(s.webOfficeWatches, key)
			_ = existing.Control.Close()
			existing = nil
		default:
		}
	}
	if existing == nil {
		s.webOfficeWatches[key] = watch
	}
	s.webOfficeMu.Unlock()
	if existing != nil {
		stopWebOfficeProcess(context.Background(), watch)
		return existing
	}
	return watch
}

func (s *Server) stopWebOfficeWatch(ctx context.Context, key string) error {
	s.webOfficeMu.Lock()
	watch := s.webOfficeWatches[key]
	delete(s.webOfficeWatches, key)
	s.webOfficeMu.Unlock()
	if watch == nil {
		return nil
	}
	return stopWebOfficeProcess(ctx, watch)
}

func stopWebOfficeProcess(ctx context.Context, watch *webOfficeWatch) error {
	if watch == nil {
		return nil
	}
	killErr := watch.Control.Kill(watch.Command)
	if errors.Is(killErr, os.ErrProcessDone) {
		killErr = nil
	}
	select {
	case <-watch.Done:
	case <-ctx.Done():
		_ = watch.Control.Close()
		return ctx.Err()
	case <-time.After(3 * time.Second):
		_ = watch.Control.Close()
		return errors.New("timed out waiting for OfficeCLI preview process to stop")
	}
	closeErr := watch.Control.Close()
	if killErr != nil {
		return killErr
	}
	return closeErr
}

func (s *Server) stopAllWebOfficeWatches(ctx context.Context) error {
	s.webOfficeMu.Lock()
	watches := make([]*webOfficeWatch, 0, len(s.webOfficeWatches))
	for key, watch := range s.webOfficeWatches {
		watches = append(watches, watch)
		delete(s.webOfficeWatches, key)
	}
	s.webOfficeMu.Unlock()
	var failures []error
	for _, watch := range watches {
		if err := stopWebOfficeProcess(ctx, watch); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func writeWebOfficeError(w http.ResponseWriter, err error) {
	if errors.Is(err, errWebFSForbidden) {
		writeWorkspaceJSON(w, http.StatusForbidden, map[string]any{
			"ok": false, "error": "path is outside an authorized workspace",
			"code": "PATH_OUTSIDE_SANDBOX",
		})
		return
	}
	writeWebFSError(w, err)
}
