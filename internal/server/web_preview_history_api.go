package server

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const maxWebPreviewSnapshots = 100

type webPreviewHistoryTarget struct {
	ContentType      string `json:"content_type,omitempty"`
	ContentTypeCamel string `json:"contentType,omitempty"`
	FilePath         string `json:"file_path,omitempty"`
	Workspace        string `json:"workspace,omitempty"`
	FileName         string `json:"file_name,omitempty"`
	Title            string `json:"title,omitempty"`
	Language         string `json:"language,omitempty"`
	ConversationID   string `json:"conversation_id,omitempty"`
	ArtifactID       string `json:"artifact_id,omitempty"`
	VersionID        string `json:"version_id,omitempty"`
	ContentURL       string `json:"content_url,omitempty"`
}

type webPreviewSnapshot struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	CreatedAt   int64  `json:"created_at"`
	Size        int64  `json:"size"`
	ContentType string `json:"contentType"`
	FileName    string `json:"file_name,omitempty"`
	FilePath    string `json:"file_path,omitempty"`
}

type webPreviewHistoryInput struct {
	Target          webPreviewHistoryTarget `json:"target"`
	Content         string                  `json:"content,omitempty"`
	SnapshotID      string                  `json:"snapshot_id,omitempty"`
	SnapshotIDCamel string                  `json:"snapshotId,omitempty"`
}

func (s *Server) handleWebPreviewHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWebFSError(w, fmt.Errorf("%w: POST required", errWebFSMethod))
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWebFSRequestBytes)
	var input webPreviewHistoryInput
	if err := decodeWebFSJSON(r, &input); err != nil {
		writeWebFSError(w, err)
		return
	}
	target, err := s.normalizeWebPreviewTarget(userID, input.Target, r)
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	directory, err := s.webPreviewTargetDirectory(userID, target)
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	s.webPreviewHistoryMu.Lock()
	defer s.webPreviewHistoryMu.Unlock()
	switch r.URL.Path {
	case "/api/preview-history/list":
		s.handleWebPreviewHistoryList(w, directory)
	case "/api/preview-history/save":
		s.handleWebPreviewHistorySave(w, directory, target, input.Content)
	case "/api/preview-history/get-content":
		s.handleWebPreviewHistoryContent(
			w, directory, firstNonEmpty(input.SnapshotID, input.SnapshotIDCamel),
		)
	default:
		writeWebFSError(w, os.ErrNotExist)
	}
}

func (s *Server) normalizeWebPreviewTarget(
	userID string,
	target webPreviewHistoryTarget,
	r *http.Request,
) (webPreviewHistoryTarget, error) {
	target.ContentType = strings.TrimSpace(firstNonEmpty(target.ContentType, target.ContentTypeCamel))
	target.ContentTypeCamel = ""
	if !validWebPreviewContentType(target.ContentType) {
		return target, fmt.Errorf("%w: target content_type is invalid", errWebFSInvalid)
	}
	fields := []*string{
		&target.FilePath, &target.Workspace, &target.FileName, &target.Title,
		&target.Language, &target.ConversationID, &target.ArtifactID,
		&target.VersionID, &target.ContentURL,
	}
	identity := false
	for _, field := range fields {
		*field = strings.TrimSpace(*field)
		if len(*field) > 8192 || strings.ContainsRune(*field, 0) {
			return target, fmt.Errorf("%w: preview target field is invalid", errWebFSInvalid)
		}
		identity = identity || *field != ""
	}
	if !identity {
		return target, fmt.Errorf("%w: preview target identity is required", errWebFSInvalid)
	}
	allowLocalRead := (s.synonLinkAuth == nil || !s.synonLinkAuth.enabled) && isLoopbackRequest(r)
	if target.FilePath != "" {
		access, err := s.resolveWebFSPath(
			userID, target.FilePath, target.Workspace, false, false, allowLocalRead,
		)
		if err != nil {
			return target, err
		}
		target.FilePath = access.Target
		if target.Workspace != "" {
			target.Workspace = access.Root
		}
	} else if target.Workspace != "" {
		access, err := s.resolveWebFSPath(
			userID, target.Workspace, target.Workspace, false, true, allowLocalRead,
		)
		if err != nil {
			return target, err
		}
		target.Workspace = access.Target
	}
	return target, nil
}

func (s *Server) webPreviewTargetDirectory(
	userID string,
	target webPreviewHistoryTarget,
) (string, error) {
	raw, err := json.Marshal(target)
	if err != nil {
		return "", err
	}
	userHash := sha256.Sum256([]byte(strings.TrimSpace(userID)))
	targetHash := sha256.Sum256(raw)
	base := firstNonEmpty(strings.TrimSpace(s.fileRoot), filepath.Join(os.TempDir(), "synon-go"))
	return filepath.Join(base, "preview-history", fmt.Sprintf("%x", userHash[:8]), fmt.Sprintf("%x", targetHash[:])), nil
}

func validWebPreviewContentType(value string) bool {
	switch value {
	case "markdown", "diff", "code", "html", "pdf", "ppt", "word", "excel",
		"image", "structure", "molecule", "table", "msa", "genome", "sequence",
		"notebook", "hdf5", "latex", "url":
		return true
	default:
		return false
	}
}

func (s *Server) handleWebPreviewHistoryList(w http.ResponseWriter, directory string) {
	snapshots, err := readWebPreviewSnapshots(directory)
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, snapshots)
}

func (s *Server) handleWebPreviewHistorySave(
	w http.ResponseWriter,
	directory string,
	target webPreviewHistoryTarget,
	content string,
) {
	if len(content) > maxWebFSReadBytes {
		writeWebFSError(w, errWebFSTooLarge)
		return
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		writeWebFSError(w, err)
		return
	}
	now := time.Now().UTC()
	snapshot := webPreviewSnapshot{
		ID: uuid.NewString(), Label: "Snapshot " + now.Format("2006-01-02 15:04:05 UTC"),
		CreatedAt: now.UnixMilli(), Size: int64(len(content)), ContentType: target.ContentType,
		FileName: target.FileName, FilePath: target.FilePath,
	}
	contentPath := filepath.Join(directory, snapshot.ID+".content")
	metadataPath := filepath.Join(directory, snapshot.ID+".json")
	if err := writeWebPreviewFileAtomic(contentPath, []byte(content)); err != nil {
		writeWebFSError(w, err)
		return
	}
	metadata, err := json.Marshal(snapshot)
	if err == nil {
		err = writeWebPreviewFileAtomic(metadataPath, metadata)
	}
	if err != nil {
		_ = os.Remove(contentPath)
		writeWebFSError(w, err)
		return
	}
	snapshots, err := readWebPreviewSnapshots(directory)
	if err == nil && len(snapshots) > maxWebPreviewSnapshots {
		for _, stale := range snapshots[maxWebPreviewSnapshots:] {
			_ = os.Remove(filepath.Join(directory, stale.ID+".json"))
			_ = os.Remove(filepath.Join(directory, stale.ID+".content"))
		}
	}
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, snapshot)
}

func (s *Server) handleWebPreviewHistoryContent(
	w http.ResponseWriter,
	directory string,
	snapshotID string,
) {
	snapshotID = strings.TrimSpace(snapshotID)
	parsedID, err := uuid.Parse(snapshotID)
	if err != nil || parsedID.String() != snapshotID {
		writeWebFSError(w, fmt.Errorf("%w: snapshot_id is invalid", errWebFSInvalid))
		return
	}
	metadata, err := readWebPreviewSnapshot(filepath.Join(directory, snapshotID+".json"))
	if errors.Is(err, os.ErrNotExist) {
		writeWorkspaceJSON(w, http.StatusOK, nil)
		return
	}
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	content, err := readWebFSFile(filepath.Join(directory, snapshotID+".content"), maxWebFSReadBytes)
	if errors.Is(err, os.ErrNotExist) {
		writeWorkspaceJSON(w, http.StatusOK, nil)
		return
	}
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	if int64(len(content)) != metadata.Size {
		writeWebFSError(w, errors.New("preview snapshot content size does not match metadata"))
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"snapshot": metadata, "content": string(content),
	})
}

func readWebPreviewSnapshots(directory string) ([]webPreviewSnapshot, error) {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []webPreviewSnapshot{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(entries) > maxWebPreviewSnapshots*4 {
		return nil, errWebFSTooMany
	}
	snapshots := make([]webPreviewSnapshot, 0, len(entries)/2)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		parsedID, err := uuid.Parse(id)
		if err != nil || parsedID.String() != id {
			return nil, fmt.Errorf("%w: invalid preview snapshot filename", errWebFSUnsupported)
		}
		snapshot, err := readWebPreviewSnapshot(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		if snapshot.ID != id {
			return nil, fmt.Errorf("%w: preview snapshot identity mismatch", errWebFSUnsupported)
		}
		snapshots = append(snapshots, snapshot)
	}
	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].CreatedAt != snapshots[j].CreatedAt {
			return snapshots[i].CreatedAt > snapshots[j].CreatedAt
		}
		return snapshots[i].ID > snapshots[j].ID
	})
	return snapshots, nil
}

func readWebPreviewSnapshot(path string) (webPreviewSnapshot, error) {
	var snapshot webPreviewSnapshot
	raw, err := readWebFSFile(path, 64<<10)
	if err != nil {
		return snapshot, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return snapshot, fmt.Errorf("decode preview snapshot metadata: %w", err)
	}
	if snapshot.ID == "" || snapshot.Label == "" || snapshot.CreatedAt <= 0 ||
		snapshot.Size < 0 || snapshot.Size > maxWebFSReadBytes ||
		!validWebPreviewContentType(snapshot.ContentType) {
		return snapshot, fmt.Errorf("%w: preview snapshot metadata is invalid", errWebFSUnsupported)
	}
	return snapshot, nil
}

func writeWebPreviewFileAtomic(target string, data []byte) error {
	if len(data) > maxWebFSReadBytes {
		return errWebFSTooLarge
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".synon-preview-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if _, err := os.Lstat(target); err == nil {
		return errWebFSConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(temporaryPath, target)
}
