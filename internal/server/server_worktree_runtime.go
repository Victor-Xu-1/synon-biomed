package server

import (
	"context"

	"encoding/json"
	"errors"
	"fmt"

	"os"
	osexec "os/exec"
	"path/filepath"

	"strconv"
	"strings"

	"time"
)

const worktreeRuntimeNamespace = "worktrees"

type worktreeSessionRecord struct {
	SessionID          string `json:"sessionId"`
	OriginalCwd        string `json:"originalCwd"`
	RepoRoot           string `json:"repoRoot"`
	WorktreePath       string `json:"worktreePath"`
	WorktreeBranch     string `json:"worktreeBranch,omitempty"`
	OriginalHeadCommit string `json:"originalHeadCommit,omitempty"`
	CreatedAt          string `json:"createdAt"`
}

type worktreeChangeSummary struct {
	ChangedFiles int `json:"changedFiles"`
	Commits      int `json:"commits"`
}

func (s *Server) executeWorktreeTool(ctx context.Context, toolName string, input map[string]any) (any, error) {
	if err := s.validateRegisteredTool(toolName, input); err != nil {
		return nil, err
	}
	if s.runtimeStore == nil {
		return nil, errors.New("worktree runtime store is not configured")
	}
	switch toolName {
	case "EnterWorktree":
		return s.enterWorktree(ctx, input)
	case "ExitWorktree":
		return s.exitWorktree(ctx, input)
	default:
		return nil, fmt.Errorf("unknown worktree tool: %s", toolName)
	}
}

func (s *Server) enterWorktree(ctx context.Context, input map[string]any) (map[string]any, error) {
	sessionID := worktreeSessionID(input)
	key := worktreeSessionKey(sessionID)
	if existing, ok, err := s.runtimeStore.Get(worktreeRuntimeNamespace, key); err != nil {
		return nil, err
	} else if ok {
		if record, ok := worktreeRecordValue(existing.Value); ok {
			if _, statErr := os.Stat(record.WorktreePath); statErr == nil {
				return nil, errors.New("Already in a worktree session")
			}
		}
		_, _ = s.runtimeStore.Delete(worktreeRuntimeNamespace, key)
	}
	repoPath, err := resolveWorktreeRepoPath(s.fileRoot, stringValue(input["path"]))
	if err != nil {
		return nil, err
	}
	repoRoot, err := findGitRoot(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	if err := ensurePathWithinRoot(s.fileRoot, repoRoot, "git repo"); err != nil {
		return nil, err
	}
	slug := strings.TrimSpace(stringValue(input["name"]))
	if slug == "" {
		slug = fmt.Sprintf("worktree-%d", time.Now().UTC().UnixNano())
	}
	if err := validateWorktreeSlug(slug); err != nil {
		return nil, err
	}
	branch := "synon/" + slug
	targetRoot := managedWorktreeRoot(s.fileRoot, repoRoot)
	target := filepath.Join(targetRoot, filepath.Base(repoRoot), filepath.FromSlash(slug))
	if err := ensurePathWithinRoot(targetRoot, target, "worktree path"); err != nil {
		return nil, err
	}
	if _, err := os.Stat(target); err == nil {
		return nil, fmt.Errorf("worktree path already exists: %s", target)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return nil, err
	}
	head, err := gitOutput(ctx, repoRoot, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("resolve git HEAD: %w", err)
	}
	if _, err := gitOutput(ctx, repoRoot, "worktree", "add", "-b", branch, target, "HEAD"); err != nil {
		return nil, fmt.Errorf("create git worktree: %w", err)
	}
	record := worktreeSessionRecord{
		SessionID:          sessionID,
		OriginalCwd:        repoRoot,
		RepoRoot:           repoRoot,
		WorktreePath:       target,
		WorktreeBranch:     branch,
		OriginalHeadCommit: strings.TrimSpace(head),
		CreatedAt:          time.Now().UTC().Format(time.RFC3339Nano),
	}
	if _, err := s.runtimeStore.Set(worktreeRuntimeNamespace, key, record); err != nil {
		return nil, err
	}
	return map[string]any{
		"worktreePath":   record.WorktreePath,
		"worktreeBranch": record.WorktreeBranch,
		"originalCwd":    record.OriginalCwd,
		"sessionId":      record.SessionID,
		"message":        fmt.Sprintf("Created worktree at %s on branch %s. The session is now working in the worktree. Use ExitWorktree to leave mid-session, or exit the session to be prompted.", record.WorktreePath, record.WorktreeBranch),
	}, nil
}

func (s *Server) exitWorktree(ctx context.Context, input map[string]any) (map[string]any, error) {
	action := strings.TrimSpace(stringValue(input["action"]))
	if action != "keep" && action != "remove" {
		return nil, fmt.Errorf("ExitWorktree.action must be keep or remove, got %q", action)
	}
	sessionID := worktreeSessionID(input)
	key := worktreeSessionKey(sessionID)
	entry, ok, err := s.runtimeStore.Get(worktreeRuntimeNamespace, key)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("No-op: there is no active EnterWorktree session to exit")
	}
	record, ok := worktreeRecordValue(entry.Value)
	if !ok {
		return nil, errors.New("stored worktree session is invalid")
	}
	summary, err := countWorktreeChanges(ctx, record.WorktreePath, record.OriginalHeadCommit)
	if err != nil {
		return nil, fmt.Errorf("Could not verify worktree state at %s. Refusing to remove without explicit confirmation: %w", record.WorktreePath, err)
	}
	if action == "remove" && !boolValue(input["discard_changes"], false) && (summary.ChangedFiles > 0 || summary.Commits > 0) {
		parts := []string{}
		if summary.ChangedFiles > 0 {
			parts = append(parts, fmt.Sprintf("%d uncommitted files", summary.ChangedFiles))
		}
		if summary.Commits > 0 {
			parts = append(parts, fmt.Sprintf("%d commits", summary.Commits))
		}
		return nil, fmt.Errorf("Worktree has %s. Removing will discard this work permanently. Confirm with the user, then re-invoke with discard_changes: true or use action: \"keep\" to preserve the worktree.", strings.Join(parts, " and "))
	}
	if action == "remove" {
		args := []string{"worktree", "remove"}
		if boolValue(input["discard_changes"], false) {
			args = append(args, "--force")
		}
		args = append(args, record.WorktreePath)
		if _, err := gitOutput(ctx, record.RepoRoot, args...); err != nil {
			return nil, fmt.Errorf("remove git worktree: %w", err)
		}
	}
	if _, err := s.runtimeStore.Delete(worktreeRuntimeNamespace, key); err != nil {
		return nil, err
	}
	return map[string]any{
		"action":           action,
		"originalCwd":      record.OriginalCwd,
		"worktreePath":     record.WorktreePath,
		"worktreeBranch":   record.WorktreeBranch,
		"discardedFiles":   summary.ChangedFiles,
		"discardedCommits": summary.Commits,
		"message":          fmt.Sprintf("Exited worktree session %s with action %s.", sessionID, action),
	}, nil
}

func worktreeSessionID(input map[string]any) string {
	sessionID := strings.TrimSpace(stringValue(input["sessionId"]))
	if sessionID == "" {
		return "default"
	}
	return sessionID
}

func worktreeSessionKey(sessionID string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '_' || r == '-' || r == '.':
			return r
		default:
			return '-'
		}
	}, strings.TrimSpace(sessionID))
	if cleaned == "" {
		cleaned = "default"
	}
	return cleaned
}

func worktreeRecordValue(value any) (worktreeSessionRecord, bool) {
	raw, err := json.Marshal(value)
	if err != nil {
		return worktreeSessionRecord{}, false
	}
	var record worktreeSessionRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return worktreeSessionRecord{}, false
	}
	return record, strings.TrimSpace(record.WorktreePath) != "" && strings.TrimSpace(record.RepoRoot) != ""
}

func resolveWorktreeRepoPath(root string, requestedPath string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("file root is not configured")
	}
	requestedPath = strings.TrimSpace(requestedPath)
	if requestedPath == "" {
		requestedPath = "."
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if evaluatedRoot, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootAbs = evaluatedRoot
	}
	var target string
	if filepath.IsAbs(requestedPath) {
		target, err = filepath.Abs(requestedPath)
	} else {
		target, err = filepath.Abs(filepath.Join(rootAbs, requestedPath))
	}
	if err != nil {
		return "", err
	}
	target, err = filepath.EvalSymlinks(target)
	if err != nil {
		return "", err
	}
	if err := ensurePathWithinRoot(rootAbs, target, "worktree repo path"); err != nil {
		return "", err
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("worktree repo path is not a directory: %s", requestedPath)
	}
	return target, nil
}

func managedWorktreeRoot(fileRoot string, repoRoot string) string {
	fileRootAbs, err := filepath.Abs(fileRoot)
	if err != nil {
		return filepath.Join(repoRoot, ".synon-worktrees")
	}
	if evaluatedRoot, err := filepath.EvalSymlinks(fileRootAbs); err == nil {
		fileRootAbs = evaluatedRoot
	}
	if samePath(fileRootAbs, repoRoot) {
		return filepath.Join(filepath.Dir(repoRoot), ".synon-worktrees")
	}
	return filepath.Join(fileRootAbs, ".synon-worktrees")
}

func samePath(left string, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return left == right
	}
	if evaluatedLeft, err := filepath.EvalSymlinks(leftAbs); err == nil {
		leftAbs = evaluatedLeft
	}
	if evaluatedRight, err := filepath.EvalSymlinks(rightAbs); err == nil {
		rightAbs = evaluatedRight
	}
	return leftAbs == rightAbs
}

func ensurePathWithinRoot(root string, target string, label string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s escapes root: %s", label, filepath.ToSlash(rel))
	}
	return nil
}

func findGitRoot(ctx context.Context, path string) (string, error) {
	root, err := gitOutput(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("resolve git root: %w", err)
	}
	return strings.TrimSpace(root), nil
}

func countWorktreeChanges(ctx context.Context, worktreePath string, originalHeadCommit string) (worktreeChangeSummary, error) {
	status, err := gitOutput(ctx, worktreePath, "status", "--porcelain")
	if err != nil {
		return worktreeChangeSummary{}, err
	}
	changedFiles := 0
	for _, line := range strings.Split(status, "\n") {
		if strings.TrimSpace(line) != "" {
			changedFiles++
		}
	}
	if strings.TrimSpace(originalHeadCommit) == "" {
		return worktreeChangeSummary{}, errors.New("missing original head commit")
	}
	revList, err := gitOutput(ctx, worktreePath, "rev-list", "--count", strings.TrimSpace(originalHeadCommit)+"..HEAD")
	if err != nil {
		return worktreeChangeSummary{}, err
	}
	commits, err := strconv.Atoi(strings.TrimSpace(revList))
	if err != nil {
		return worktreeChangeSummary{}, err
	}
	return worktreeChangeSummary{ChangedFiles: changedFiles, Commits: commits}, nil
}

func validateWorktreeSlug(slug string) error {
	if slug == "" {
		return errors.New("worktree name is required")
	}
	if len(slug) > 64 {
		return fmt.Errorf("worktree name must be at most 64 characters: %s", slug)
	}
	for _, segment := range strings.Split(slug, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("invalid worktree name segment: %s", slug)
		}
		for _, r := range segment {
			switch {
			case r >= 'a' && r <= 'z':
				continue
			case r >= 'A' && r <= 'Z':
				continue
			case r >= '0' && r <= '9':
				continue
			case r == '.' || r == '_' || r == '-':
				continue
			default:
				return fmt.Errorf("invalid worktree name %q: segments may contain only letters, digits, dots, underscores, and dashes", slug)
			}
		}
	}
	return nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	gitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := osexec.CommandContext(gitCtx, "git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	if gitCtx.Err() != nil {
		return "", gitCtx.Err()
	}
	if err != nil {
		return "", fmt.Errorf("git -C %s %s failed: %w: %s", dir, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}
