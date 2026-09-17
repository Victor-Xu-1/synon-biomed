package server

import (
	"archive/tar"
	"bytes"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	localSpeechProvider       = "local"
	localSpeechModelName      = "sherpa-onnx-streaming-zipformer-small-ctc-zh-int8-2025-04-01"
	localSpeechRuntimeVersion = "v1.13.5"

	localSpeechRuntimeURL  = "https://github.com/k2-fsa/sherpa-onnx/releases/download/v1.13.5/sherpa-onnx-v1.13.5-linux-x64-shared-no-tts.tar.bz2"
	localSpeechRuntimeSHA  = "a39369615d610cb835f225b6b7fbff684aedf46557eab8a90e1ccc11fac84166"
	localSpeechRuntimeSize = int64(24526724)

	localSpeechModelURL  = "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-streaming-zipformer-small-ctc-zh-int8-2025-04-01.tar.bz2"
	localSpeechModelSHA  = "b3b309f7ce4a737195fcc6963ea19b0653a7d3401580af5ae0d3e284cbb71f0b"
	localSpeechModelSize = int64(21264113)

	localSpeechInstallTimeout  = 20 * time.Minute
	localSpeechMaxArchiveBytes = int64(180 * 1024 * 1024)
	localSpeechMaxOutputBytes  = int64(2 * 1024 * 1024)
	localSpeechTempDirName     = "tmp"
	localSpeechRequestPrefix   = "request-"
)

var (
	errLocalSpeechDependencyMissing = errors.New("local speech audio dependency is missing")
	errLocalSpeechAudioConversion   = errors.New("local speech audio conversion failed")
	errLocalSpeechRuntimeFailed     = errors.New("local speech runtime failed")
	errLocalSpeechTempCleanup       = errors.New("local speech temporary audio cleanup failed")
)

type localSpeechArtifact struct {
	URL      string
	SHA256   string
	ByteSize int64
}

type localSpeechRuntime struct {
	root       string
	httpClient *http.Client

	mu          sync.Mutex
	phase       string
	progress    int
	ready       bool
	lastError   string
	installWait chan struct{}
	installErr  error

	tempMu         sync.Mutex
	activeTempDirs map[string]struct{}
}

type localSpeechHostSpec struct {
	runtimeRoot string
	binaryPath  string
	libraryDir  string
}

type boundedLocalSpeechBuffer struct {
	buffer bytes.Buffer
	limit  int64
}

func (b *boundedLocalSpeechBuffer) Write(data []byte) (int, error) {
	if int64(b.buffer.Len()+len(data)) > b.limit {
		remaining := b.limit - int64(b.buffer.Len())
		if remaining > 0 {
			_, _ = b.buffer.Write(data[:remaining])
		}
		return len(data), io.ErrShortWrite
	}
	return b.buffer.Write(data)
}

func (b *boundedLocalSpeechBuffer) String() string {
	return b.buffer.String()
}

func newLocalSpeechRuntime(fileRoot string, httpClient *http.Client) *localSpeechRuntime {
	root := strings.TrimSpace(fileRoot)
	if root == "" {
		root = os.TempDir()
	}
	root = filepath.Join(root, ".synon", "local-speech")
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	runtimeState := &localSpeechRuntime{
		root:           root,
		httpClient:     httpClient,
		phase:          "not_installed",
		activeTempDirs: make(map[string]struct{}),
	}
	if localSpeechInstallComplete(root) {
		runtimeState.ready = true
		runtimeState.phase = "ready"
		runtimeState.progress = 100
	}
	return runtimeState
}

func localSpeechInstallComplete(root string) bool {
	required := []string{
		filepath.Join(root, "active", "runtime", "bin", "sherpa-onnx"),
		filepath.Join(root, "active", "runtime", "lib", "libonnxruntime.so"),
		filepath.Join(root, "active", "runtime", "lib", "libsherpa-onnx-c-api.so"),
		filepath.Join(root, "active", "runtime", "lib", "libsherpa-onnx-cxx-api.so"),
		filepath.Join(root, "active", "model", localSpeechModelName, "model.int8.onnx"),
		filepath.Join(root, "active", "model", localSpeechModelName, "bbpe.model"),
		filepath.Join(root, "active", "model", localSpeechModelName, "tokens.txt"),
	}
	for _, path := range required {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
			return false
		}
	}
	return true
}

func localSpeechHost() (localSpeechHostSpec, error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return localSpeechHostSpec{}, fmt.Errorf("local speech runtime is currently available for linux/amd64 only")
	}
	return localSpeechHostSpec{
		runtimeRoot: "sherpa-onnx-v1.13.5-linux-x64-shared-no-tts",
		binaryPath:  filepath.Join("bin", "sherpa-onnx"),
		libraryDir:  "lib",
	}, nil
}

func (r *localSpeechRuntime) setProgress(phase string, progress int, lastError string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phase = phase
	r.progress = progress
	r.lastError = lastError
}

func (r *localSpeechRuntime) status() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	data := map[string]any{
		"phase":    r.phase,
		"progress": r.progress,
		"ready":    r.ready,
		"model":    localSpeechModelName,
		"runtime":  localSpeechRuntimeVersion,
		"provider": localSpeechProvider,
	}
	if r.lastError != "" {
		data["error"] = r.lastError
	}
	return data
}

func (s *Server) handleLocalSpeechStatus(w http.ResponseWriter, request *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if request.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeWebSpeechError(w, newWebSpeechError(http.StatusMethodNotAllowed, "STT_METHOD_NOT_ALLOWED", "local speech status requires GET"))
		return
	}
	userID := strings.TrimSpace(request.Header.Get("X-Synon-User-Id"))
	if userID == "" {
		writeWebSpeechError(w, newWebSpeechError(http.StatusUnauthorized, "STT_AUTH_REQUIRED", "authentication is required"))
		return
	}
	config, configErr := s.resolveWebSpeechRuntimeConfig(userID)
	if configErr != nil {
		writeWebSpeechError(w, configErr)
		return
	}
	if config.Provider != localSpeechProvider {
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"success": true, "data": map[string]any{
			"enabled":  true,
			"phase":    "not_required",
			"progress": 100,
			"provider": config.Provider,
			"ready":    true,
		}})
		return
	}
	if s == nil || s.localSpeechRuntime == nil {
		writeWebSpeechError(w, newWebSpeechError(http.StatusServiceUnavailable, "STT_LOCAL_UNAVAILABLE", "local speech runtime is unavailable"))
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"success": true, "data": s.localSpeechRuntime.status()})
}

func (s *Server) handleLocalSpeechPrepare(w http.ResponseWriter, request *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if request.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeWebSpeechError(w, newWebSpeechError(http.StatusMethodNotAllowed, "STT_METHOD_NOT_ALLOWED", "local speech preparation requires POST"))
		return
	}
	userID := strings.TrimSpace(request.Header.Get("X-Synon-User-Id"))
	if userID == "" {
		writeWebSpeechError(w, newWebSpeechError(http.StatusUnauthorized, "STT_AUTH_REQUIRED", "authentication is required"))
		return
	}
	config, configErr := s.resolveWebSpeechRuntimeConfig(userID)
	if configErr != nil {
		writeWebSpeechError(w, configErr)
		return
	}
	if config.Provider != localSpeechProvider {
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"success": true, "data": map[string]any{
			"enabled":  true,
			"phase":    "not_required",
			"progress": 100,
			"provider": config.Provider,
			"ready":    true,
		}})
		return
	}
	if s == nil || s.localSpeechRuntime == nil {
		writeWebSpeechError(w, newWebSpeechError(http.StatusServiceUnavailable, "STT_LOCAL_UNAVAILABLE", "local speech runtime is unavailable"))
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), localSpeechInstallTimeout)
	defer cancel()
	if err := s.localSpeechRuntime.ensureReady(ctx); err != nil {
		status := http.StatusBadGateway
		code := "STT_LOCAL_INSTALL_FAILED"
		if errors.Is(err, errLocalSpeechTempCleanup) {
			status = http.StatusInternalServerError
			code = "STT_LOCAL_CLEANUP_FAILED"
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
			code = "STT_LOCAL_INSTALL_TIMEOUT"
		}
		writeWebSpeechError(w, newWebSpeechError(status, code, "local speech runtime preparation failed"))
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"success": true, "data": s.localSpeechRuntime.status()})
}

func (s *Server) transcribeLocalSpeech(
	ctx context.Context,
	config *webLocalSpeechConfig,
	input *webSpeechAudioInput,
) (webSpeechResult, *webSpeechAPIError) {
	if s == nil || s.localSpeechRuntime == nil {
		return webSpeechResult{}, newWebSpeechError(http.StatusServiceUnavailable, "STT_LOCAL_UNAVAILABLE", "local speech runtime is unavailable")
	}
	if err := validateLocalSpeechConfig(config); err != nil {
		return webSpeechResult{}, newWebSpeechError(http.StatusBadRequest, "STT_INVALID_CONFIG", "local speech configuration is invalid")
	}
	text, err := s.localSpeechRuntime.transcribe(ctx, input)
	if err != nil {
		switch {
		case errors.Is(err, errLocalSpeechTempCleanup):
			return webSpeechResult{}, newWebSpeechError(http.StatusInternalServerError, "STT_LOCAL_CLEANUP_FAILED", "local speech temporary audio cleanup failed")
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return webSpeechResult{}, newWebSpeechError(http.StatusGatewayTimeout, "STT_LOCAL_TIMEOUT", "local speech transcription timed out")
		case errors.Is(err, errLocalSpeechDependencyMissing):
			return webSpeechResult{}, newWebSpeechError(http.StatusServiceUnavailable, "STT_LOCAL_DEPENDENCY_MISSING", "local speech audio dependency is unavailable")
		case errors.Is(err, errLocalSpeechAudioConversion):
			return webSpeechResult{}, newWebSpeechError(http.StatusUnprocessableEntity, "STT_AUDIO_CONVERSION_FAILED", "audio conversion failed")
		default:
			return webSpeechResult{}, newWebSpeechError(http.StatusBadGateway, "STT_LOCAL_RUNTIME_FAILED", "local speech transcription failed")
		}
	}
	language := "zh-CN"
	if config != nil && strings.TrimSpace(config.Language) != "" {
		language = strings.TrimSpace(config.Language)
	} else if input != nil && strings.TrimSpace(input.LanguageHint) != "" {
		language = strings.TrimSpace(input.LanguageHint)
	}
	return webSpeechResult{
		Language: language, Model: localSpeechModelName, Provider: localSpeechProvider, Text: text,
	}, nil
}

// cleanupOrphanedTempDirs removes only request directories that are not active
// in this runtime instance. A process crash can skip the per-request defer;
// the next preparation or transcription therefore gets a second cleanup gate.
// The installed runtime and model live outside this directory and are never
// touched by this cleanup.
func (r *localSpeechRuntime) cleanupOrphanedTempDirs() error {
	tmpRoot := filepath.Join(r.root, localSpeechTempDirName)
	entries, err := os.ReadDir(tmpRoot)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read local speech temporary directory: %w", err)
	}

	var cleanupErr error
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), localSpeechRequestPrefix) {
			continue
		}
		path := filepath.Join(tmpRoot, entry.Name())
		r.tempMu.Lock()
		_, active := r.activeTempDirs[path]
		r.tempMu.Unlock()
		if active {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove %s: %w", entry.Name(), err))
		}
	}
	return cleanupErr
}

func (r *localSpeechRuntime) registerTempDir(path string) {
	r.tempMu.Lock()
	defer r.tempMu.Unlock()
	if r.activeTempDirs == nil {
		r.activeTempDirs = make(map[string]struct{})
	}
	r.activeTempDirs[path] = struct{}{}
}

func (r *localSpeechRuntime) unregisterTempDir(path string) {
	r.tempMu.Lock()
	defer r.tempMu.Unlock()
	delete(r.activeTempDirs, path)
}

func (r *localSpeechRuntime) ensureReady(ctx context.Context) error {
	if err := r.cleanupOrphanedTempDirs(); err != nil {
		return fmt.Errorf("%w: %v", errLocalSpeechTempCleanup, err)
	}
	r.mu.Lock()
	if r.ready && localSpeechInstallComplete(r.root) {
		r.mu.Unlock()
		return nil
	}
	if r.installWait != nil {
		wait := r.installWait
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wait:
		}
		r.mu.Lock()
		err := r.installErr
		ready := r.ready
		r.mu.Unlock()
		if err != nil {
			return err
		}
		if !ready {
			return errLocalSpeechRuntimeFailed
		}
		return nil
	}
	r.installWait = make(chan struct{})
	wait := r.installWait
	r.installErr = nil
	r.phase = "preparing"
	r.progress = 1
	r.lastError = ""
	r.mu.Unlock()

	err := r.install(ctx)

	r.mu.Lock()
	r.installErr = err
	r.ready = err == nil
	if err == nil {
		r.phase = "ready"
		r.progress = 100
		r.lastError = ""
	} else {
		r.phase = "error"
		r.progress = 0
		r.lastError = "local speech runtime installation failed"
	}
	close(wait)
	r.installWait = nil
	r.mu.Unlock()
	return err
}

func (r *localSpeechRuntime) install(ctx context.Context) error {
	host, err := localSpeechHost()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(r.root, 0o700); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(r.root, ".staging-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	r.setProgress("downloading_runtime", 8, "")
	runtimeArchive := filepath.Join(staging, "runtime.tar.bz2")
	if err := downloadLocalSpeechArtifact(ctx, r.httpClient, localSpeechArtifact{
		URL: localSpeechRuntimeURL, SHA256: localSpeechRuntimeSHA, ByteSize: localSpeechRuntimeSize,
	}, runtimeArchive); err != nil {
		return err
	}
	r.setProgress("downloading_model", 45, "")
	modelArchive := filepath.Join(staging, "model.tar.bz2")
	if err := downloadLocalSpeechArtifact(ctx, r.httpClient, localSpeechArtifact{
		URL: localSpeechModelURL, SHA256: localSpeechModelSHA, ByteSize: localSpeechModelSize,
	}, modelArchive); err != nil {
		return err
	}

	r.setProgress("extracting", 65, "")
	runtimeExtracted := filepath.Join(staging, "runtime-extracted")
	modelExtracted := filepath.Join(staging, "model-extracted")
	if err := extractLocalSpeechTarBz2(runtimeArchive, runtimeExtracted); err != nil {
		return err
	}
	if err := extractLocalSpeechTarBz2(modelArchive, modelExtracted); err != nil {
		return err
	}

	r.setProgress("finalizing", 82, "")
	active := filepath.Join(staging, "active")
	runtimeDir := filepath.Join(active, "runtime")
	modelDir := filepath.Join(active, "model", localSpeechModelName)
	if err := os.MkdirAll(filepath.Join(runtimeDir, "bin"), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(runtimeDir, "lib"), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(modelDir, 0o700); err != nil {
		return err
	}

	runtimeSourceRoot := filepath.Join(runtimeExtracted, host.runtimeRoot)
	if err := copyLocalSpeechFile(filepath.Join(runtimeSourceRoot, host.binaryPath), filepath.Join(runtimeDir, host.binaryPath), 0o700); err != nil {
		return err
	}
	for _, library := range []string{"libonnxruntime.so", "libsherpa-onnx-c-api.so", "libsherpa-onnx-cxx-api.so"} {
		if err := copyLocalSpeechFile(filepath.Join(runtimeSourceRoot, host.libraryDir, library), filepath.Join(runtimeDir, host.libraryDir, library), 0o700); err != nil {
			return err
		}
	}
	modelSourceRoot := filepath.Join(modelExtracted, localSpeechModelName)
	for _, modelFile := range []string{"model.int8.onnx", "bbpe.model", "tokens.txt"} {
		if err := copyLocalSpeechFile(filepath.Join(modelSourceRoot, modelFile), filepath.Join(modelDir, modelFile), 0o600); err != nil {
			return err
		}
	}

	activePath := filepath.Join(r.root, "active")
	previousPath := filepath.Join(r.root, "active.previous")
	_ = os.RemoveAll(previousPath)
	if _, err := os.Stat(activePath); err == nil {
		if err := os.Rename(activePath, previousPath); err != nil {
			return err
		}
	}
	if err := os.Rename(active, activePath); err != nil {
		_ = os.Rename(previousPath, activePath)
		return err
	}
	_ = os.RemoveAll(previousPath)
	r.setProgress("ready", 100, "")
	return nil
}

func downloadLocalSpeechArtifact(ctx context.Context, client *http.Client, artifact localSpeechArtifact, destination string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.URL, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("local speech asset download returned HTTP %d", response.StatusCode)
	}

	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	reader := io.Reader(response.Body)
	if artifact.ByteSize > 0 {
		reader = io.LimitReader(response.Body, artifact.ByteSize+1)
	}
	written, copyErr := io.Copy(io.MultiWriter(file, hash), reader)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if artifact.ByteSize > 0 && written != artifact.ByteSize {
		return fmt.Errorf("local speech asset size mismatch: got %d, want %d", written, artifact.ByteSize)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, artifact.SHA256) {
		return errors.New("local speech asset checksum mismatch")
	}
	return nil
}

func extractLocalSpeechTarBz2(archivePath, destination string) error {
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := tar.NewReader(bzip2.NewReader(file))
	var total int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		relative, err := localSpeechTarPath(header.Name)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if !localSpeechPathWithin(destination, target) {
			return errors.New("local speech archive path escapes staging directory")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > localSpeechMaxArchiveBytes || total > localSpeechMaxArchiveBytes-header.Size {
				return errors.New("local speech archive is too large")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(output, reader, header.Size)
			closeErr := output.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			total += header.Size
		default:
			return errors.New("local speech archive contains an unsupported entry")
		}
	}
}

func localSpeechTarPath(name string) (string, error) {
	name = filepath.ToSlash(strings.TrimSpace(name))
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsRune(name, 0) {
		return "", errors.New("local speech archive path is invalid")
	}
	parts := make([]string, 0, 8)
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." {
			continue
		}
		if part == ".." || strings.Contains(part, "\\") {
			return "", errors.New("local speech archive path is invalid")
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return "", errors.New("local speech archive path is empty")
	}
	return filepath.Join(parts...), nil
}

func localSpeechPathWithin(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." {
		return false
	}
	return !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func copyLocalSpeechFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		if err == nil {
			err = errors.New("local speech source is not a regular file")
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func (r *localSpeechRuntime) transcribe(ctx context.Context, input *webSpeechAudioInput) (text string, err error) {
	if err := r.ensureReady(ctx); err != nil {
		return "", err
	}
	tmpRoot := filepath.Join(r.root, localSpeechTempDirName)
	if err := os.MkdirAll(tmpRoot, 0o700); err != nil {
		return "", err
	}
	temporary, err := os.MkdirTemp(tmpRoot, "request-")
	if err != nil {
		return "", err
	}
	r.registerTempDir(temporary)
	defer func() {
		cleanupErr := os.RemoveAll(temporary)
		r.unregisterTempDir(temporary)
		if cleanupErr != nil {
			text = ""
			err = errors.Join(err, fmt.Errorf("%w: %v", errLocalSpeechTempCleanup, cleanupErr))
		}
	}()

	inputPath := filepath.Join(temporary, "input.audio")
	audioFile, err := os.OpenFile(inputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(audioFile, io.LimitReader(input.File, maxWebSpeechAudioBytes+1))
	closeErr := audioFile.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}

	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return "", fmt.Errorf("%w: ffmpeg", errLocalSpeechDependencyMissing)
	}
	wavePath := filepath.Join(temporary, "input.wav")
	conversionStderr := &boundedLocalSpeechBuffer{limit: localSpeechMaxOutputBytes}
	conversion := exec.CommandContext(
		ctx,
		ffmpeg,
		"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-i", inputPath,
		"-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le",
		wavePath,
	)
	conversion.Stdout = io.Discard
	conversion.Stderr = conversionStderr
	if err := conversion.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", fmt.Errorf("%w: %s", errLocalSpeechAudioConversion, strings.TrimSpace(conversionStderr.String()))
	}
	if info, err := os.Stat(wavePath); err != nil || !info.Mode().IsRegular() || info.Size() <= 44 {
		return "", errLocalSpeechAudioConversion
	}

	runtimeDir := filepath.Join(r.root, "active", "runtime")
	modelDir := filepath.Join(r.root, "active", "model", localSpeechModelName)
	binaryPath := filepath.Join(runtimeDir, "bin", "sherpa-onnx")
	modelPath := filepath.Join(modelDir, "model.int8.onnx")
	tokensPath := filepath.Join(modelDir, "tokens.txt")
	stdout := &boundedLocalSpeechBuffer{limit: localSpeechMaxOutputBytes}
	stderr := &boundedLocalSpeechBuffer{limit: localSpeechMaxOutputBytes}
	recognizer := exec.CommandContext(
		ctx,
		binaryPath,
		"--zipformer2-ctc-model="+modelPath,
		"--tokens="+tokensPath,
		"--num-threads=1",
		wavePath,
	)
	recognizer.Env = append(os.Environ(),
		"LD_LIBRARY_PATH="+filepath.Join(runtimeDir, "lib"),
		"OMP_NUM_THREADS=1",
		"OPENBLAS_NUM_THREADS=1",
		"MKL_NUM_THREADS=1",
	)
	recognizer.Stdout = stdout
	recognizer.Stderr = stderr
	if err := recognizer.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		diagnostic := strings.TrimSpace(stderr.String())
		if diagnostic == "" {
			diagnostic = strings.TrimSpace(stdout.String())
		}
		return "", fmt.Errorf("%w: %s", errLocalSpeechRuntimeFailed, diagnostic)
	}
	text, err = localSpeechTranscript(stderr.String())
	if err != nil {
		text, err = localSpeechTranscript(stdout.String())
	}
	if err != nil {
		return "", err
	}
	return text, nil
}

func localSpeechTranscript(output string) (string, error) {
	lines := strings.Split(output, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var result struct {
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(line), &result) == nil {
			if text := strings.TrimSpace(result.Text); text != "" {
				return text, nil
			}
		}
	}
	return "", errors.New("local speech runtime returned no transcript")
}
