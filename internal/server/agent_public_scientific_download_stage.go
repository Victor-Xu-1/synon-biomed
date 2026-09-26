package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"synon-go/internal/networkpolicy"
	"synon-go/internal/tools/securefetch"
)

const (
	agentPublicScientificDownloadStageVersion = 1
)

type agentPublicScientificDownloadStageState struct {
	Version        int       `json:"version"`
	RequestSHA256  string    `json:"request_sha256"`
	Filename       string    `json:"filename"`
	Validator      string    `json:"validator,omitempty"`
	ContentType    string    `json:"content_type,omitempty"`
	ExpectedTotal  int64     `json:"expected_total"`
	RetryNotBefore time.Time `json:"retry_not_before,omitzero"`
}

type agentPublicScientificDownloadStage struct {
	directory        string
	payload          string
	metadata         string
	file             *os.File
	state            agentPublicScientificDownloadStageState
	transferBytes    int64
	transferDuration time.Duration
}

type agentPublicScientificStagedFile struct {
	file           *os.File
	directory      string
	sizeBytes      int64
	contentSHA     string
	bytesPerSecond *float64
	unlock         func()
}

func (stage *agentPublicScientificDownloadStage) recordTransfer(bytes int64, duration time.Duration) {
	if stage == nil || bytes <= 0 || duration <= 0 {
		return
	}
	stage.transferBytes += bytes
	stage.transferDuration += duration
}

func (stage *agentPublicScientificDownloadStage) transferRate() *float64 {
	if stage == nil || stage.transferBytes <= 0 || stage.transferDuration <= 0 {
		return nil
	}
	seconds := stage.transferDuration.Seconds()
	if seconds <= 0 {
		return nil
	}
	rate := float64(stage.transferBytes) / seconds
	return &rate
}

func (staged *agentPublicScientificStagedFile) close() {
	if staged == nil {
		return
	}
	if staged.file != nil {
		_ = staged.file.Close()
		staged.file = nil
	}
	if staged.unlock != nil {
		staged.unlock()
		staged.unlock = nil
	}
}

func (staged *agentPublicScientificStagedFile) discard() {
	if staged == nil {
		return
	}
	if staged.file != nil {
		_ = staged.file.Close()
		staged.file = nil
	}
	if staged.directory != "" {
		_ = os.RemoveAll(staged.directory)
		staged.directory = ""
	}
	if staged.unlock != nil {
		staged.unlock()
		staged.unlock = nil
	}
}

type agentPublicScientificStageLock struct {
	gate chan struct{}
	refs int
}

var agentPublicScientificStageLocks = struct {
	mu    sync.Mutex
	locks map[string]*agentPublicScientificStageLock
}{locks: map[string]*agentPublicScientificStageLock{}}

func acquireAgentPublicScientificStageLock(ctx context.Context, key string) (func(), error) {
	agentPublicScientificStageLocks.mu.Lock()
	lock := agentPublicScientificStageLocks.locks[key]
	if lock == nil {
		lock = &agentPublicScientificStageLock{gate: make(chan struct{}, 1)}
		agentPublicScientificStageLocks.locks[key] = lock
	}
	lock.refs++
	agentPublicScientificStageLocks.mu.Unlock()
	releaseRef := func() {
		agentPublicScientificStageLocks.mu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(agentPublicScientificStageLocks.locks, key)
		}
		agentPublicScientificStageLocks.mu.Unlock()
	}
	select {
	case lock.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-lock.gate
			releaseRef()
			return nil, err
		}
		return func() { <-lock.gate; releaseRef() }, nil
	case <-ctx.Done():
		releaseRef()
		return nil, ctx.Err()
	}
}

func (s *Server) fetchAndStageAgentPublicScientificFile(
	ctx context.Context,
	workspaceDir string,
	request agentPublicScientificFileRequest,
) (*agentPublicScientificStagedFile, string, error) {
	maximumBytes := request.maximumBytes()
	stageKey, err := agentPublicScientificDownloadStageKey(workspaceDir, request)
	if err != nil {
		return nil, "", errAgentPublicScientificFileAuthority
	}
	unlock, err := acquireAgentPublicScientificStageLock(ctx, stageKey)
	if err != nil {
		return nil, "", err
	}
	transferredLock := false
	defer func() {
		if !transferredLock {
			unlock()
		}
	}()
	stage, err := s.openAgentPublicScientificDownloadStage(stageKey, request)
	if err != nil {
		return nil, "", err
	}
	keepOpen := false
	defer func() {
		if !keepOpen && stage.file != nil {
			_ = stage.file.Close()
			stage.file = nil
		}
	}()
	offset, err := stage.size()
	if err != nil {
		return nil, "", errAgentPublicScientificFileAuthority
	}
	if delay := time.Until(stage.state.RetryNotBefore); delay > 0 {
		return nil, "", &agentPublicScientificTransferInterrupted{BytesRetained: offset, Resumable: offset > 0 && stage.state.Validator != "",
			RetryNotBefore: stage.state.RetryNotBefore}
	}
	if offset == 0 {
		adopted, adoptErr := s.adoptExistingAgentPublicScientificPartial(ctx, workspaceDir, request, stage)
		if adoptErr != nil {
			return nil, "", adoptErr
		}
		if adopted {
			offset, err = stage.size()
			if err != nil {
				return nil, "", errAgentPublicScientificFileAuthority
			}
		}
	}
	if offset > 0 && stage.state.Validator == "" {
		if err := stage.reset(request); err != nil {
			return nil, "", errAgentPublicScientificFileAuthority
		}
		offset = 0
	}
	if stage.state.ExpectedTotal > 0 && offset > stage.state.ExpectedTotal {
		if err := stage.reset(request); err != nil {
			return nil, "", errAgentPublicScientificFileAuthority
		}
		offset = 0
	}
	if offset == 0 || stage.state.ExpectedTotal <= 0 || offset < stage.state.ExpectedTotal {
		if err := s.continueAgentPublicScientificDownload(ctx, workspaceDir, request, stage, &offset); err != nil {
			return nil, "", err
		}
	}
	if stage.state.ExpectedTotal > 0 && offset != stage.state.ExpectedTotal {
		return nil, "", io.ErrUnexpectedEOF
	}
	if err := stage.file.Sync(); err != nil {
		return nil, "", errors.New("public scientific file download staging failed")
	}
	if _, err := stage.file.Seek(0, io.SeekStart); err != nil {
		return nil, "", errors.New("public scientific file download staging failed")
	}
	bytesPerSecond := stage.transferRate()
	reportAgentPublicScientificDownloadProgress(ctx, "verifying_download", offset, offset, bytesPerSecond)
	hasher := sha256.New()
	sizeBytes, err := io.Copy(hasher, io.LimitReader(stage.file, maximumBytes+1))
	if err != nil || sizeBytes <= 0 || sizeBytes > maximumBytes || sizeBytes != offset {
		return nil, "", errors.New("public scientific file download staging failed")
	}
	if _, err := stage.file.Seek(0, io.SeekStart); err != nil {
		return nil, "", errors.New("public scientific file download staging failed")
	}
	reportAgentPublicScientificDownloadProgress(ctx, "download_verified", sizeBytes, sizeBytes, bytesPerSecond)
	keepOpen, transferredLock = true, true
	return &agentPublicScientificStagedFile{
		file: stage.file, directory: stage.directory, sizeBytes: sizeBytes,
		contentSHA: hex.EncodeToString(hasher.Sum(nil)), bytesPerSecond: bytesPerSecond, unlock: unlock,
	}, stage.state.ContentType, nil
}

func (s *Server) adoptExistingAgentPublicScientificPartial(
	ctx context.Context,
	workspaceDir string,
	request agentPublicScientificFileRequest,
	stage *agentPublicScientificDownloadStage,
) (bool, error) {
	maximumBytes := request.maximumBytes()
	workspaceRoot, err := canonicalHostDirectory(workspaceDir)
	if err != nil || filepath.Base(request.Filename) != request.Filename || stage == nil || stage.file == nil {
		return false, errAgentPublicScientificFileAuthority
	}
	target := filepath.Join(workspaceRoot, request.Filename)
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() <= 0 || info.Size() > maximumBytes {
		return false, errAgentPublicScientificFileConflict
	}
	local, err := os.Open(target)
	if err != nil {
		return false, errAgentPublicScientificFileConflict
	}
	defer local.Close()
	policy := s.agentPublicScientificTransferPolicy(request)
	policy.PrefixBytes = info.Size()
	response, err := s.publicScientificFiles.Fetch(ctx, request.DownloadURL, policy)
	if err != nil {
		return false, err
	}
	if response == nil || response.Body == nil || response.FinalURL == nil ||
		!agentPublicScientificResponseHostAllowed(request, response.FinalURL.Hostname()) {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return false, errAgentPublicScientificFileAuthority
	}
	defer response.Body.Close()
	remoteTotal := response.ContentLength
	if response.StatusCode == http.StatusPartialContent {
		remoteTotal = response.ContentRangeTotal
	}
	validator := securefetch.ResumeValidator(response)
	if remoteTotal < info.Size() || remoteTotal > maximumBytes || validator == "" {
		return false, errAgentPublicScientificFileConflict
	}
	localHash, remoteHash := sha256.New(), sha256.New()
	localBytes, localErr := io.Copy(localHash, io.LimitReader(local, info.Size()+1))
	remoteBytes, remoteErr := io.Copy(remoteHash, io.LimitReader(response.Body, info.Size()+1))
	if localErr != nil || remoteErr != nil || localBytes != info.Size() || remoteBytes != info.Size() ||
		!bytes.Equal(localHash.Sum(nil), remoteHash.Sum(nil)) {
		return false, errAgentPublicScientificFileConflict
	}
	if err := ensureAgentPublicScientificDiskSpace(workspaceDir, s.fileRoot, remoteTotal, maximumBytes); err != nil {
		return false, err
	}
	if _, err := local.Seek(0, io.SeekStart); err != nil {
		return false, errAgentPublicScientificFileAuthority
	}
	if err := stage.file.Truncate(0); err != nil {
		return false, errAgentPublicScientificFileAuthority
	}
	if _, err := stage.file.Seek(0, io.SeekStart); err != nil {
		return false, errAgentPublicScientificFileAuthority
	}
	written, err := io.Copy(stage.file, io.LimitReader(local, info.Size()+1))
	if err != nil || written != info.Size() || stage.file.Sync() != nil {
		return false, errAgentPublicScientificFileAuthority
	}
	stage.state.Validator = validator
	stage.state.ContentType = agentPublicScientificReportedContentType(response)
	stage.state.ExpectedTotal = remoteTotal
	if err := stage.persist(); err != nil {
		return false, errAgentPublicScientificFileAuthority
	}
	if err := local.Close(); err != nil {
		return false, errAgentPublicScientificFileAuthority
	}
	if err := os.Remove(target); err != nil {
		return false, errAgentPublicScientificFileAuthority
	}
	return true, nil
}

func agentPublicScientificAllowedHosts(request agentPublicScientificFileRequest) []string {
	return appendUniqueFolded([]string{request.SourceHost}, request.RedirectHosts...)
}

func agentPublicScientificResponseHostAllowed(
	request agentPublicScientificFileRequest,
	host string,
) bool {
	for _, allowed := range agentPublicScientificAllowedHosts(request) {
		if strings.EqualFold(strings.TrimSpace(allowed), strings.TrimSpace(host)) {
			return true
		}
	}
	if request.AllowPublicRedirects {
		normalized, err := networkpolicy.NormalizePattern(host)
		return err == nil && !networkpolicy.PrivateOrReserved(normalized)
	}
	return false
}

func (s *Server) openAgentPublicScientificDownloadStage(
	key string,
	request agentPublicScientificFileRequest,
) (*agentPublicScientificDownloadStage, error) {
	if s == nil || s.fileRoot == "" {
		return nil, errAgentPublicScientificFileAuthority
	}
	root := filepath.Join(s.fileRoot, "workspace", "public-scientific-downloads")
	if err := ensureAgentPublicScientificPrivateDirectory(root); err != nil {
		return nil, errAgentPublicScientificFileAuthority
	}
	directory := filepath.Join(root, key)
	if err := ensureAgentPublicScientificPrivateDirectory(directory); err != nil {
		return nil, errAgentPublicScientificFileAuthority
	}
	stage := &agentPublicScientificDownloadStage{
		directory: directory, payload: filepath.Join(directory, "content.part"),
		metadata: filepath.Join(directory, "state.json"),
		state: agentPublicScientificDownloadStageState{
			Version: agentPublicScientificDownloadStageVersion, RequestSHA256: agentPublicScientificDownloadRequestDigest(request),
			Filename: request.Filename, ExpectedTotal: -1,
		},
	}
	if raw, err := os.ReadFile(stage.metadata); err == nil {
		var persisted agentPublicScientificDownloadStageState
		if json.Unmarshal(raw, &persisted) != nil || !persisted.matches(request) {
			if err := stage.resetFiles(); err != nil {
				return nil, errAgentPublicScientificFileAuthority
			}
		} else {
			stage.state = persisted
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errAgentPublicScientificFileAuthority
	}
	if info, err := os.Lstat(stage.payload); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > request.maximumBytes() {
			return nil, errAgentPublicScientificFileAuthority
		}
		// A process may stop after validation but before atomic publication.
		// Staging lives in a private 0700 directory, so restore owner-write here
		// and let the checksum gate decide whether the completed bytes are reusable.
		if info.Mode().Perm() != 0o600 && os.Chmod(stage.payload, 0o600) != nil {
			return nil, errAgentPublicScientificFileAuthority
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errAgentPublicScientificFileAuthority
	}
	file, err := os.OpenFile(stage.payload, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, errAgentPublicScientificFileAuthority
	}
	stage.file = file
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, errAgentPublicScientificFileAuthority
	}
	info, err := file.Stat()
	if err != nil || info.Size() > request.maximumBytes() ||
		(stage.state.ExpectedTotal > 0 && info.Size() > stage.state.ExpectedTotal) {
		_ = file.Close()
		return nil, errAgentPublicScientificFileAuthority
	}
	if info.Size() > 0 {
		if _, err := os.Stat(stage.metadata); err != nil {
			if err := stage.reset(request); err != nil {
				_ = file.Close()
				return nil, errAgentPublicScientificFileAuthority
			}
		}
	} else if err := stage.persist(); err != nil {
		_ = file.Close()
		return nil, errAgentPublicScientificFileAuthority
	}
	return stage, nil
}

func agentPublicScientificDownloadStageKey(workspaceDir string, request agentPublicScientificFileRequest) (string, error) {
	workspaceRoot, err := canonicalHostDirectory(workspaceDir)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte("synon-public-scientific-download-v1\x00" + workspaceRoot + "\x00" +
		request.SourceURL + "\x00" + request.DownloadURL + "\x00" + request.Filename))
	return hex.EncodeToString(digest[:]), nil
}

func (state agentPublicScientificDownloadStageState) matches(request agentPublicScientificFileRequest) bool {
	return state.Version == agentPublicScientificDownloadStageVersion &&
		state.RequestSHA256 == agentPublicScientificDownloadRequestDigest(request) &&
		state.Filename == request.Filename && state.ExpectedTotal >= -1
}

func agentPublicScientificDownloadRequestDigest(request agentPublicScientificFileRequest) string {
	digest := sha256.Sum256([]byte(request.SourceURL + "\x00" + request.DownloadURL + "\x00" + request.Filename))
	return hex.EncodeToString(digest[:])
}

func (stage *agentPublicScientificDownloadStage) size() (int64, error) {
	if stage == nil || stage.file == nil {
		return 0, errAgentPublicScientificFileAuthority
	}
	info, err := stage.file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return 0, errAgentPublicScientificFileAuthority
	}
	return info.Size(), nil
}

func (stage *agentPublicScientificDownloadStage) reset(request agentPublicScientificFileRequest) error {
	if stage == nil || stage.file == nil {
		return errAgentPublicScientificFileAuthority
	}
	if err := stage.file.Truncate(0); err != nil {
		return err
	}
	if _, err := stage.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	stage.state = agentPublicScientificDownloadStageState{
		Version: agentPublicScientificDownloadStageVersion, RequestSHA256: agentPublicScientificDownloadRequestDigest(request),
		Filename: request.Filename, ExpectedTotal: -1,
	}
	return stage.persist()
}

func (stage *agentPublicScientificDownloadStage) resetFiles() error {
	for _, path := range []string{stage.payload, stage.metadata} {
		if info, err := os.Lstat(path); err == nil {
			if info.Mode()&os.ModeSymlink != 0 || (!info.Mode().IsRegular() && path == stage.payload) {
				return errAgentPublicScientificFileAuthority
			}
			if err := os.Remove(path); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (stage *agentPublicScientificDownloadStage) persist() error {
	raw, err := json.Marshal(stage.state)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(stage.directory, ".state-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	ok := false
	defer func() {
		_ = temporary.Close()
		if !ok {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, stage.metadata); err != nil {
		return err
	}
	ok = true
	return nil
}

func ensureAgentPublicScientificPrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errAgentPublicScientificFileAuthority
	}
	if info.Mode().Perm()&0o077 != 0 {
		return os.Chmod(path, 0o700)
	}
	return nil
}
