package server

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	webFSSnapshotModeGit   = "git-repo"
	webFSSnapshotModeFiles = "snapshot"
)

type webFSSnapshotFile struct {
	Size   int64
	SHA256 [sha256.Size]byte
}

type webFSSnapshot struct {
	mu           sync.Mutex
	UserID       string
	Workspace    string
	Mode         string
	Branch       string
	GitRoot      string
	GitPrefix    string
	BaselineRoot string
	Files        map[string]webFSSnapshotFile
}

type webFSSnapshotChange struct {
	FilePath     string `json:"file_path"`
	RelativePath string `json:"relative_path"`
	Operation    string `json:"operation"`
}

type webFSSnapshotCompare struct {
	Staged   []webFSSnapshotChange `json:"staged"`
	Unstaged []webFSSnapshotChange `json:"unstaged"`
}

func (s *Server) handleWebFSSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWebFSError(w, fmt.Errorf("%w: POST required", errWebFSMethod))
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWebFSRequestBytes)
	var input struct {
		Workspace string `json:"workspace"`
		FilePath  string `json:"file_path,omitempty"`
		Operation string `json:"operation,omitempty"`
	}
	if err := decodeWebFSJSON(r, &input); err != nil {
		writeWebFSError(w, err)
		return
	}
	if strings.TrimSpace(input.Workspace) == "" {
		writeWebFSError(w, fmt.Errorf("%w: workspace is required", errWebFSInvalid))
		return
	}
	mutation := webFSSnapshotMutationRoute(r.URL.Path)
	allowLocalRead := (s.synonLinkAuth == nil || !s.synonLinkAuth.enabled) && isLoopbackRequest(r)
	access, err := s.resolveWebFSPath(
		userID, input.Workspace, input.Workspace, mutation, true, allowLocalRead,
	)
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	workspaceRoot := access.Target

	if r.URL.Path == "/api/fs/snapshot/dispose" {
		snapshot := s.removeWebFSSnapshot(userID, workspaceRoot)
		if snapshot != nil {
			snapshot.mu.Lock()
			if snapshot.BaselineRoot != "" {
				err = os.RemoveAll(snapshot.BaselineRoot)
			}
			snapshot.mu.Unlock()
		}
		if err != nil {
			writeWebFSError(w, err)
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, nil)
		return
	}

	snapshot, err := s.ensureWebFSSnapshot(r.Context(), userID, workspaceRoot)
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	snapshot.mu.Lock()
	defer snapshot.mu.Unlock()

	switch r.URL.Path {
	case "/api/fs/snapshot/init", "/api/fs/snapshot/info":
		if snapshot.Mode == webFSSnapshotModeGit {
			if branch, branchErr := webFSGitBranch(r.Context(), snapshot.Workspace); branchErr == nil {
				snapshot.Branch = branch
			}
		}
		writeWorkspaceJSON(w, http.StatusOK, webFSSnapshotInfo(snapshot))
	case "/api/fs/snapshot/compare":
		var result webFSSnapshotCompare
		if snapshot.Mode == webFSSnapshotModeGit {
			result, err = compareWebFSGitSnapshot(r.Context(), snapshot)
		} else {
			result, err = compareWebFSFileSnapshot(r.Context(), snapshot)
		}
		if err != nil {
			writeWebFSError(w, err)
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, result)
	case "/api/fs/snapshot/baseline":
		if strings.TrimSpace(input.FilePath) == "" {
			writeWebFSError(w, fmt.Errorf("%w: file_path is required", errWebFSInvalid))
			return
		}
		var content *string
		if snapshot.Mode == webFSSnapshotModeGit {
			content, err = readWebFSGitBaseline(r.Context(), snapshot, input.FilePath)
		} else {
			content, err = readWebFSFileBaseline(snapshot, input.FilePath)
		}
		if err != nil {
			writeWebFSError(w, err)
			return
		}
		if content == nil {
			writeWorkspaceJSON(w, http.StatusOK, nil)
		} else {
			writeWorkspaceJSON(w, http.StatusOK, *content)
		}
	case "/api/fs/snapshot/branches":
		branches := []string{}
		if snapshot.Mode == webFSSnapshotModeGit {
			branches, err = listWebFSGitBranches(r.Context(), snapshot.Workspace)
		}
		if err != nil {
			writeWebFSError(w, err)
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, branches)
	case "/api/fs/snapshot/stage":
		err = requireWebFSGitSnapshot(snapshot)
		if err == nil {
			err = stageWebFSGitPath(r.Context(), snapshot, input.FilePath)
		}
		writeWebFSSnapshotMutationResult(w, err)
	case "/api/fs/snapshot/stage-all":
		err = requireWebFSGitSnapshot(snapshot)
		if err == nil {
			_, err = runWebFSGit(r.Context(), snapshot.Workspace, 1<<20, "add", "-A", "--", ".")
		}
		writeWebFSSnapshotMutationResult(w, err)
	case "/api/fs/snapshot/unstage":
		err = requireWebFSGitSnapshot(snapshot)
		if err == nil {
			err = unstageWebFSGitPath(r.Context(), snapshot, input.FilePath)
		}
		writeWebFSSnapshotMutationResult(w, err)
	case "/api/fs/snapshot/unstage-all":
		err = requireWebFSGitSnapshot(snapshot)
		if err == nil {
			err = unstageAllWebFSGit(r.Context(), snapshot)
		}
		writeWebFSSnapshotMutationResult(w, err)
	case "/api/fs/snapshot/discard":
		if err = validateWebFSSnapshotMutationInput(input.FilePath, input.Operation); err == nil {
			if snapshot.Mode == webFSSnapshotModeGit {
				err = discardWebFSGitPath(r.Context(), snapshot, input.FilePath, input.Operation)
			} else {
				err = resetWebFSFileSnapshot(snapshot, input.FilePath)
			}
		}
		writeWebFSSnapshotMutationResult(w, err)
	case "/api/fs/snapshot/reset":
		if err = validateWebFSSnapshotMutationInput(input.FilePath, input.Operation); err == nil {
			if snapshot.Mode == webFSSnapshotModeGit {
				err = resetWebFSGitPath(r.Context(), snapshot, input.FilePath)
			} else {
				err = resetWebFSFileSnapshot(snapshot, input.FilePath)
			}
		}
		writeWebFSSnapshotMutationResult(w, err)
	default:
		writeWebFSError(w, os.ErrNotExist)
	}
}

func (s *Server) ensureWebFSSnapshot(
	ctx context.Context,
	userID string,
	workspaceRoot string,
) (*webFSSnapshot, error) {
	key := webFSSnapshotKey(userID, workspaceRoot)
	s.webSnapshotMu.Lock()
	existing := s.webSnapshots[key]
	s.webSnapshotMu.Unlock()
	if existing != nil {
		return existing, nil
	}

	gitRoot, gitPrefix, branch, isGit, err := detectWebFSGitSnapshot(ctx, workspaceRoot)
	if err != nil {
		return nil, err
	}
	snapshot := &webFSSnapshot{
		UserID: userID, Workspace: workspaceRoot, Mode: webFSSnapshotModeGit,
		Branch: branch, GitRoot: gitRoot, GitPrefix: gitPrefix,
	}
	if !isGit {
		snapshot.Mode = webFSSnapshotModeFiles
		snapshot.GitRoot = ""
		snapshot.GitPrefix = ""
		snapshot.Branch = ""
		snapshot.BaselineRoot, err = s.newWebFSSnapshotBaselineRoot(key, workspaceRoot)
		if err != nil {
			return nil, err
		}
		snapshot.Files, err = captureWebFSFileSnapshot(ctx, workspaceRoot, snapshot.BaselineRoot)
		if err != nil {
			_ = os.RemoveAll(snapshot.BaselineRoot)
			return nil, err
		}
	}

	s.webSnapshotMu.Lock()
	if existing = s.webSnapshots[key]; existing == nil {
		s.webSnapshots[key] = snapshot
		existing = snapshot
	}
	s.webSnapshotMu.Unlock()
	if existing != snapshot && snapshot.BaselineRoot != "" {
		_ = os.RemoveAll(snapshot.BaselineRoot)
	}
	return existing, nil
}

func (s *Server) newWebFSSnapshotBaselineRoot(key, workspaceRoot string) (string, error) {
	base := filepath.Join(firstNonEmpty(strings.TrimSpace(s.fileRoot), os.TempDir()), "web-fs-snapshots")
	if absolute, err := filepath.Abs(base); err == nil && webFSPathWithin(workspaceRoot, absolute) {
		base = filepath.Join(os.TempDir(), "synon-go-web-fs-snapshots")
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(base, key[:16]+"-")
}

func (s *Server) removeWebFSSnapshot(userID, workspaceRoot string) *webFSSnapshot {
	key := webFSSnapshotKey(userID, workspaceRoot)
	s.webSnapshotMu.Lock()
	snapshot := s.webSnapshots[key]
	delete(s.webSnapshots, key)
	s.webSnapshotMu.Unlock()
	return snapshot
}

func webFSSnapshotKey(userID, workspaceRoot string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(userID) + "\x00" + filepath.Clean(workspaceRoot)))
	return fmt.Sprintf("%x", sum[:])
}

func webFSSnapshotInfo(snapshot *webFSSnapshot) map[string]any {
	var branch any
	if snapshot.Branch != "" {
		branch = snapshot.Branch
	}
	return map[string]any{"mode": snapshot.Mode, "branch": branch}
}

func webFSSnapshotMutationRoute(route string) bool {
	switch route {
	case "/api/fs/snapshot/stage", "/api/fs/snapshot/stage-all",
		"/api/fs/snapshot/unstage", "/api/fs/snapshot/unstage-all",
		"/api/fs/snapshot/discard", "/api/fs/snapshot/reset":
		return true
	default:
		return false
	}
}

func requireWebFSGitSnapshot(snapshot *webFSSnapshot) error {
	if snapshot.Mode != webFSSnapshotModeGit {
		return fmt.Errorf("%w: staging requires a Git workspace", errWebFSInvalid)
	}
	return nil
}

func validateWebFSSnapshotMutationInput(filePath, operation string) error {
	if strings.TrimSpace(filePath) == "" {
		return fmt.Errorf("%w: file_path is required", errWebFSInvalid)
	}
	switch operation {
	case "create", "modify", "delete":
		return nil
	default:
		return fmt.Errorf("%w: operation must be create, modify, or delete", errWebFSInvalid)
	}
}

func writeWebFSSnapshotMutationResult(w http.ResponseWriter, err error) {
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, nil)
}

func snapshotRelativePath(workspaceRoot, filePath string) (string, string, error) {
	target, err := resolveWebFSTarget(workspaceRoot, filePath, false)
	if err != nil {
		return "", "", err
	}
	relative, err := filepath.Rel(workspaceRoot, target)
	if err != nil {
		return "", "", err
	}
	if relative == "." {
		return "", "", fmt.Errorf("%w: file_path must reference a file", errWebFSInvalid)
	}
	return target, filepath.ToSlash(relative), nil
}

func isWebFSSnapshotNotFound(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}
