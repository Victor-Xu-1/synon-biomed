package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"synon-go/internal/tools/fileevents"
)

func detectWebFSGitSnapshot(
	ctx context.Context,
	workspaceRoot string,
) (gitRoot, gitPrefix, branch string, isGit bool, err error) {
	rawRoot, err := runWebFSGit(ctx, workspaceRoot, 1<<20, "rev-parse", "--show-toplevel")
	if err != nil {
		var exitError *osexec.ExitError
		if errors.As(err, &exitError) {
			return "", "", "", false, nil
		}
		return "", "", "", false, err
	}
	gitRoot, err = canonicalWebFSDirectory(strings.TrimSpace(string(rawRoot)))
	if err != nil {
		return "", "", "", false, err
	}
	if !webFSPathWithin(gitRoot, workspaceRoot) {
		return "", "", "", false, fmt.Errorf("%w: Git root does not contain workspace", errWebFSForbidden)
	}
	relative, err := filepath.Rel(gitRoot, workspaceRoot)
	if err != nil {
		return "", "", "", false, err
	}
	if relative != "." {
		gitPrefix = filepath.ToSlash(relative) + "/"
	}
	branch, err = webFSGitBranch(ctx, workspaceRoot)
	if err != nil {
		return "", "", "", false, err
	}
	return gitRoot, gitPrefix, branch, true, nil
}

func webFSGitBranch(ctx context.Context, workspaceRoot string) (string, error) {
	raw, err := runWebFSGit(ctx, workspaceRoot, 1<<20, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		var exitError *osexec.ExitError
		if errors.As(err, &exitError) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

func compareWebFSGitSnapshot(ctx context.Context, snapshot *webFSSnapshot) (webFSSnapshotCompare, error) {
	raw, err := runWebFSGit(
		ctx, snapshot.Workspace, 8<<20,
		"status", "--porcelain=v1", "-z", "--untracked-files=all", "--", ".",
	)
	if err != nil {
		return webFSSnapshotCompare{}, err
	}
	result := webFSSnapshotCompare{
		Staged: []webFSSnapshotChange{}, Unstaged: []webFSSnapshotChange{},
	}
	records := bytes.Split(raw, []byte{0})
	for index := 0; index < len(records); index++ {
		record := records[index]
		if len(record) < 4 || record[2] != ' ' {
			continue
		}
		x, y := record[0], record[1]
		relative := filepath.ToSlash(string(record[3:]))
		if (x == 'R' || x == 'C') && index+1 < len(records) {
			index++
		}
		target, normalized, pathErr := snapshotRelativePath(snapshot.Workspace, relative)
		if pathErr != nil {
			return webFSSnapshotCompare{}, pathErr
		}
		if x == '?' && y == '?' {
			result.Unstaged = append(result.Unstaged, webFSSnapshotChange{
				FilePath: target, RelativePath: normalized, Operation: "create",
			})
			continue
		}
		if x != ' ' && x != '!' {
			result.Staged = append(result.Staged, webFSSnapshotChange{
				FilePath: target, RelativePath: normalized, Operation: webFSGitOperation(x),
			})
		}
		if y != ' ' && y != '!' {
			result.Unstaged = append(result.Unstaged, webFSSnapshotChange{
				FilePath: target, RelativePath: normalized, Operation: webFSGitOperation(y),
			})
		}
	}
	sortWebFSSnapshotChanges(result.Staged)
	sortWebFSSnapshotChanges(result.Unstaged)
	return result, nil
}

func webFSGitOperation(status byte) string {
	switch status {
	case 'A', '?':
		return "create"
	case 'D':
		return "delete"
	default:
		return "modify"
	}
}

func readWebFSGitBaseline(
	ctx context.Context,
	snapshot *webFSSnapshot,
	filePath string,
) (*string, error) {
	_, relative, err := snapshotRelativePath(snapshot.Workspace, filePath)
	if err != nil {
		return nil, err
	}
	if !webFSGitHasHead(ctx, snapshot.Workspace) {
		return nil, nil
	}
	repositoryPath := snapshot.GitPrefix + relative
	revisionPath := "HEAD:" + repositoryPath
	if _, err := runWebFSGit(ctx, snapshot.Workspace, 1<<20, "cat-file", "-e", revisionPath); err != nil {
		var exitError *osexec.ExitError
		if errors.As(err, &exitError) {
			return nil, nil
		}
		return nil, err
	}
	raw, err := runWebFSGit(ctx, snapshot.Workspace, maxWebFSReadBytes+1, "show", revisionPath)
	if err != nil {
		if strings.Contains(err.Error(), "command output limit exceeded") {
			return nil, errWebFSTooLarge
		}
		return nil, err
	}
	if len(raw) > maxWebFSReadBytes {
		return nil, errWebFSTooLarge
	}
	content := string(raw)
	return &content, nil
}

func listWebFSGitBranches(ctx context.Context, workspaceRoot string) ([]string, error) {
	raw, err := runWebFSGit(
		ctx, workspaceRoot, 4<<20,
		"for-each-ref", "--format=%(refname:short)", "refs/heads",
	)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	branches := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			branches = append(branches, line)
		}
	}
	sort.Strings(branches)
	return branches, nil
}

func stageWebFSGitPath(ctx context.Context, snapshot *webFSSnapshot, filePath string) error {
	_, relative, err := snapshotRelativePath(snapshot.Workspace, filePath)
	if err != nil {
		return err
	}
	_, err = runWebFSGit(ctx, snapshot.Workspace, 1<<20, "add", "-A", "--", filepath.FromSlash(relative))
	return err
}

func unstageWebFSGitPath(ctx context.Context, snapshot *webFSSnapshot, filePath string) error {
	_, relative, err := snapshotRelativePath(snapshot.Workspace, filePath)
	if err != nil {
		return err
	}
	path := filepath.FromSlash(relative)
	if webFSGitHasHead(ctx, snapshot.Workspace) {
		_, err = runWebFSGit(ctx, snapshot.Workspace, 1<<20, "restore", "--staged", "--", path)
	} else {
		_, err = runWebFSGit(ctx, snapshot.Workspace, 1<<20, "rm", "--cached", "-r", "--ignore-unmatch", "--", path)
	}
	return err
}

func unstageAllWebFSGit(ctx context.Context, snapshot *webFSSnapshot) error {
	var err error
	if webFSGitHasHead(ctx, snapshot.Workspace) {
		_, err = runWebFSGit(ctx, snapshot.Workspace, 1<<20, "restore", "--staged", "--", ".")
	} else {
		_, err = runWebFSGit(ctx, snapshot.Workspace, 1<<20, "rm", "--cached", "-r", "--ignore-unmatch", "--", ".")
	}
	return err
}

func discardWebFSGitPath(
	ctx context.Context,
	snapshot *webFSSnapshot,
	filePath string,
	operation string,
) error {
	target, relative, err := snapshotRelativePath(snapshot.Workspace, filePath)
	if err != nil {
		return err
	}
	path := filepath.FromSlash(relative)
	if operation == "create" && !webFSGitPathTracked(ctx, snapshot.Workspace, path) {
		info, statErr := os.Lstat(target)
		if errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errWebFSUnsupported
		}
		if info.IsDir() {
			err = os.RemoveAll(target)
		} else {
			err = os.Remove(target)
		}
	} else {
		_, err = runWebFSGit(ctx, snapshot.Workspace, 1<<20, "restore", "--worktree", "--", path)
	}
	if err == nil {
		fileevents.NotifyChanged(target)
	}
	return err
}

func resetWebFSGitPath(ctx context.Context, snapshot *webFSSnapshot, filePath string) error {
	target, relative, err := snapshotRelativePath(snapshot.Workspace, filePath)
	if err != nil {
		return err
	}
	path := filepath.FromSlash(relative)
	if webFSGitHasHead(ctx, snapshot.Workspace) {
		_, err = runWebFSGit(
			ctx, snapshot.Workspace, 1<<20,
			"restore", "--source=HEAD", "--staged", "--worktree", "--", path,
		)
	} else {
		if info, statErr := os.Lstat(target); statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return errWebFSUnsupported
			}
			if info.IsDir() {
				err = os.RemoveAll(target)
			} else {
				err = os.Remove(target)
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		if err == nil {
			_, err = runWebFSGit(ctx, snapshot.Workspace, 1<<20, "rm", "--cached", "-r", "--ignore-unmatch", "--", path)
		}
	}
	if err == nil {
		fileevents.NotifyChanged(target)
	}
	return err
}

func webFSGitPathTracked(ctx context.Context, workspaceRoot, relative string) bool {
	_, err := runWebFSGit(ctx, workspaceRoot, 1<<20, "ls-files", "--error-unmatch", "--", relative)
	return err == nil
}

func webFSGitHasHead(ctx context.Context, workspaceRoot string) bool {
	_, err := runWebFSGit(ctx, workspaceRoot, 1<<20, "rev-parse", "--verify", "HEAD")
	return err == nil
}

func runWebFSGit(
	parent context.Context,
	workspaceRoot string,
	outputLimit int,
	args ...string,
) ([]byte, error) {
	gitPath, err := osexec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("Git is required for workspace snapshots: %w", err)
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	commandArgs := append([]string{"-C", workspaceRoot, "--literal-pathspecs"}, args...)
	command := osexec.CommandContext(ctx, gitPath, commandArgs...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	stdout := &cappedCommandBuffer{max: outputLimit}
	stderr := &cappedCommandBuffer{max: 64 << 10}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if parent.Err() != nil {
			return nil, errWebFSCanceled
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errors.New("Git workspace operation timed out")
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("git %s failed: %s: %w", firstNonEmpty(firstArg(args), "command"), detail, err)
	}
	return []byte(stdout.String()), nil
}

func firstArg(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
