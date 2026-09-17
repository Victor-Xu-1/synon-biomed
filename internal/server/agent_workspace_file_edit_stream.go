package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"
)

// normalizeAgentWorkspaceQuotedOldString repairs one provider-common argument
// serialization mistake without weakening exact-edit semantics. Some models
// send a literal Python triple-quoted representation as old_string even though
// the quote delimiters are not part of the file. The representation is safe to
// unwrap only when it does not exist verbatim and its payload occurs exactly
// once. Real triple-quoted source and ambiguous payloads remain untouched and
// are handled by the canonical replacement errors.
func normalizeAgentWorkspaceQuotedOldString(
	ctx context.Context,
	current *os.File,
	oldString string,
) (string, error) {
	candidate, wrapped := unwrapAgentWorkspaceTripleQuotedString(oldString)
	if !wrapped || current == nil || candidate == "" {
		return oldString, nil
	}
	exactCount, err := countAgentWorkspaceFileOccurrences(ctx, current, []byte(oldString), 1)
	if err != nil {
		return "", err
	}
	if exactCount != 0 {
		return oldString, nil
	}
	candidateCount, err := countAgentWorkspaceFileOccurrences(ctx, current, []byte(candidate), 2)
	if err != nil {
		return "", err
	}
	if candidateCount == 1 {
		return candidate, nil
	}
	return oldString, nil
}

func unwrapAgentWorkspaceTripleQuotedString(value string) (string, bool) {
	for _, delimiter := range []string{`"""`, `'''`} {
		if len(value) >= 2*len(delimiter) && strings.HasPrefix(value, delimiter) && strings.HasSuffix(value, delimiter) {
			return value[len(delimiter) : len(value)-len(delimiter)], true
		}
	}
	return value, false
}

func countAgentWorkspaceFileOccurrences(
	ctx context.Context,
	file *os.File,
	needle []byte,
	stopAt int,
) (int, error) {
	if len(needle) == 0 || stopAt <= 0 {
		return 0, nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	prefix := make([]int, len(needle))
	for index, matched := 1, 0; index < len(needle); index++ {
		for matched > 0 && needle[index] != needle[matched] {
			matched = prefix[matched-1]
		}
		if needle[index] == needle[matched] {
			matched++
		}
		prefix[index] = matched
	}
	buffer := make([]byte, 64<<10)
	matches, matched := 0, 0
	for {
		if err := context.Cause(ctx); err != nil {
			return 0, err
		}
		count, readErr := file.Read(buffer)
		for _, value := range buffer[:count] {
			for matched > 0 && value != needle[matched] {
				matched = prefix[matched-1]
			}
			if value == needle[matched] {
				matched++
			}
			if matched == len(needle) {
				matches++
				if matches >= stopAt {
					_, seekErr := file.Seek(0, io.SeekStart)
					return matches, seekErr
				}
				matched = 0
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return 0, readErr
		}
		if count == 0 {
			return 0, io.ErrNoProgress
		}
	}
	_, err := file.Seek(0, io.SeekStart)
	return matches, err
}

func protectedAgentWorkspaceEditPath(path string) bool {
	home, _ := os.UserHomeDir()
	return protectedAgentWorkspaceEditPathWithContext(path, runtime.GOOS, home)
}

func protectedAgentWorkspaceEditPathWithContext(path, goos, home string) bool {
	goos = strings.ToLower(strings.TrimSpace(goos))
	if goos == "linux" || goos == "darwin" {
		if strings.TrimSpace(home) == "" {
			return true
		}
		canonicalHome, err := canonicalHostDirectory(home)
		if err != nil {
			return true
		}
		home = canonicalHome
	}
	parts := agentWorkspaceAbsolutePathParts(path)
	if len(parts) == 0 {
		return true
	}
	normalized := make([]string, len(parts))
	for index, part := range parts {
		normalized[index] = strings.ToLower(part)
	}
	base := normalized[len(normalized)-1]
	for _, protectedBase := range []string{
		".gitconfig", ".gitmodules", ".bashrc", ".bash_profile", ".bash_login", ".zshrc", ".zprofile",
		".zshenv", ".zlogin", ".profile", ".ripgreprc", ".mcp.json",
	} {
		if base == protectedBase {
			return true
		}
	}
	for _, protected := range [][]string{
		{".ssh"}, {".aws"}, {".gcloud"}, {".vscode"}, {".idea"},
		{".synon", "runtime", "skills"},
		{".git", "hooks"}, {".git", "modules"}, {".git", "worktrees"},
	} {
		if containsAgentWorkspacePathSequence(normalized, protected) {
			return true
		}
	}
	if containsAgentWorkspacePathSequence(normalized, []string{".git", "config"}) ||
		containsAgentWorkspacePathSequence(normalized, []string{".git", "config.worktree"}) ||
		containsAgentWorkspacePathSequence(normalized, []string{".git", "commondir"}) ||
		containsAgentWorkspacePathSequence(normalized, []string{".git", "gitdir"}) {
		return true
	}
	if homeParts, ok := agentWorkspaceRelativePathParts(home, path); ok {
		switch goos {
		case "darwin":
			for _, prefix := range [][]string{
				{"library", "launchagents"}, {"library", "launchdaemons"}, {"library", "preferences"},
				{"library", "application scripts"}, {"applications"},
			} {
				if hasAgentWorkspacePathPrefix(homeParts, prefix) {
					return true
				}
			}
		case "linux":
			for _, prefix := range [][]string{
				{".config", "systemd", "user"}, {".config", "autostart"}, {".config", "environment.d"},
				{".config", "git"}, {".local", "share", "systemd", "user"},
			} {
				if hasAgentWorkspacePathPrefix(homeParts, prefix) {
					return true
				}
			}
		}
	}
	if goos == "darwin" {
		root := string(filepath.Separator)
		if rootParts, ok := agentWorkspaceRelativePathParts(root, path); ok {
			for _, prefix := range [][]string{
				{"library", "launchagents"}, {"library", "launchdaemons"}, {"library", "preferences"},
				{"library", "application scripts"}, {"applications"},
			} {
				if hasAgentWorkspacePathPrefix(rootParts, prefix) {
					return true
				}
			}
		}
	}
	return false
}

func agentWorkspaceRelativePathParts(root, path string) ([]string, bool) {
	root = strings.TrimSpace(root)
	if root == "" || !filepath.IsAbs(root) || !filepath.IsAbs(path) {
		return nil, false
	}
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, false
	}
	parts := strings.Split(filepath.Clean(relative), string(filepath.Separator))
	for index := range parts {
		parts[index] = strings.ToLower(parts[index])
	}
	return parts, true
}

func hasAgentWorkspacePathPrefix(parts, prefix []string) bool {
	if len(parts) < len(prefix) {
		return false
	}
	for index := range prefix {
		if parts[index] != prefix[index] {
			return false
		}
	}
	return true
}

func containsAgentWorkspacePathSequence(parts, sequence []string) bool {
	if len(sequence) == 0 || len(parts) < len(sequence) {
		return false
	}
	for start := 0; start <= len(parts)-len(sequence); start++ {
		matched := true
		for offset := range sequence {
			if parts[start+offset] != sequence[offset] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func readAgentWorkspaceBounded(ctx context.Context, reader io.Reader, maxBytes int64) ([]byte, error) {
	var content bytes.Buffer
	buffer := make([]byte, 32<<10)
	remaining := maxBytes
	for remaining > 0 {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		chunk := int64(len(buffer))
		if chunk > remaining {
			chunk = remaining
		}
		count, err := reader.Read(buffer[:chunk])
		if count > 0 {
			_, _ = content.Write(buffer[:count])
			remaining -= int64(count)
		}
		if errors.Is(err, io.EOF) {
			return content.Bytes(), nil
		}
		if err != nil {
			return nil, err
		}
		if count == 0 {
			return nil, io.ErrNoProgress
		}
	}
	return content.Bytes(), nil
}

func writeAgentWorkspaceBytes(ctx context.Context, writer io.Writer, content []byte) (int64, error) {
	written := int64(0)
	for len(content) > 0 {
		if err := context.Cause(ctx); err != nil {
			return written, err
		}
		chunk := content
		if len(chunk) > 32<<10 {
			chunk = chunk[:32<<10]
		}
		count, err := writer.Write(chunk)
		written += int64(count)
		content = content[count:]
		if err != nil {
			return written, err
		}
		if count == 0 {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

type agentWorkspaceUTF8Validator struct {
	pending []byte
}

func (validator *agentWorkspaceUTF8Validator) add(content []byte) error {
	combined := make([]byte, 0, len(validator.pending)+len(content))
	combined = append(combined, validator.pending...)
	combined = append(combined, content...)
	validator.pending = validator.pending[:0]
	for len(combined) > 0 {
		if !utf8.FullRune(combined) {
			validator.pending = append(validator.pending, combined...)
			return nil
		}
		r, size := utf8.DecodeRune(combined)
		if r == utf8.RuneError && size == 1 {
			return errors.New("edit_file target is not valid UTF-8")
		}
		combined = combined[size:]
	}
	return nil
}

func (validator *agentWorkspaceUTF8Validator) finish() error {
	if len(validator.pending) != 0 {
		return errors.New("edit_file target is not valid UTF-8")
	}
	return nil
}

func streamAgentWorkspaceReplacement(
	ctx context.Context,
	reader io.Reader,
	writer io.Writer,
	oldString []byte,
	newString []byte,
) (int64, [sha256.Size]byte, error) {
	if len(oldString) == 0 {
		return 0, [sha256.Size]byte{}, errors.New("edit_file replacement source is required")
	}
	hasher := sha256.New()
	validator := agentWorkspaceUTF8Validator{}
	pending := make([]byte, 0, len(oldString)+(64<<10))
	buffer := make([]byte, 64<<10)
	written, matches := int64(0), 0
	write := func(content []byte) error {
		count, err := writeAgentWorkspaceBytes(ctx, writer, content)
		written += count
		return err
	}
	consume := func(final bool) error {
		for len(pending) > 0 {
			index := bytes.Index(pending, oldString)
			if index >= 0 {
				if err := write(pending[:index]); err != nil {
					return err
				}
				if err := write(newString); err != nil {
					return err
				}
				matches++
				pending = pending[index+len(oldString):]
				continue
			}
			if final {
				if err := write(pending); err != nil {
					return err
				}
				pending = pending[:0]
				return nil
			}
			safe := len(pending) - len(oldString) + 1
			if safe <= 0 {
				return nil
			}
			if err := write(pending[:safe]); err != nil {
				return err
			}
			pending = append(pending[:0], pending[safe:]...)
		}
		return nil
	}
	for {
		if err := context.Cause(ctx); err != nil {
			return 0, [sha256.Size]byte{}, err
		}
		count, err := reader.Read(buffer)
		if count > 0 {
			chunk := buffer[:count]
			_, _ = hasher.Write(chunk)
			if err := validator.add(chunk); err != nil {
				return 0, [sha256.Size]byte{}, err
			}
			pending = append(pending, chunk...)
			if err := consume(false); err != nil {
				return 0, [sha256.Size]byte{}, err
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, [sha256.Size]byte{}, err
		}
		if count == 0 {
			return 0, [sha256.Size]byte{}, io.ErrNoProgress
		}
	}
	if err := validator.finish(); err != nil {
		return 0, [sha256.Size]byte{}, err
	}
	if err := consume(true); err != nil {
		return 0, [sha256.Size]byte{}, err
	}
	if matches == 0 {
		return 0, [sha256.Size]byte{}, errors.New("edit_file old_string was not found")
	}
	if matches != 1 {
		return 0, [sha256.Size]byte{}, errors.New("edit_file old_string must occur exactly once")
	}
	var digest [sha256.Size]byte
	copy(digest[:], hasher.Sum(nil))
	return written, digest, nil
}

func verifyAgentWorkspaceCurrentDigest(
	ctx context.Context,
	authority *agentWorkspaceEditAuthority,
	expected [sha256.Size]byte,
) (bool, error) {
	current, _, found, err := authority.openCurrent()
	if err != nil || !found {
		return false, err
	}
	defer current.Close()
	hasher := sha256.New()
	buffer := make([]byte, 64<<10)
	for {
		if err := context.Cause(ctx); err != nil {
			return false, err
		}
		count, readErr := current.Read(buffer)
		if count > 0 {
			_, _ = hasher.Write(buffer[:count])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return false, readErr
		}
		if count == 0 {
			return false, io.ErrNoProgress
		}
	}
	return bytes.Equal(hasher.Sum(nil), expected[:]), nil
}

func digestAgentWorkspaceCurrent(ctx context.Context, authority *agentWorkspaceEditAuthority) (string, error) {
	digest, found, err := agentWorkspaceCurrentDigestState(ctx, authority)
	if err != nil || !found {
		return "", firstAgentWorkspaceError(err, errors.New("workspace file is unavailable after edit"))
	}
	return digest, nil
}

func agentWorkspaceCurrentDigestState(
	ctx context.Context,
	authority *agentWorkspaceEditAuthority,
) (string, bool, error) {
	current, _, found, err := authority.openCurrent()
	if err != nil || !found {
		return "", found, err
	}
	defer current.Close()
	digest, err := digestAgentWorkspaceReader(ctx, current)
	return digest, true, err
}

func digestAgentWorkspaceReader(ctx context.Context, reader io.Reader) (string, error) {
	hasher := sha256.New()
	buffer := make([]byte, 64<<10)
	for {
		if err := context.Cause(ctx); err != nil {
			return "", err
		}
		count, readErr := reader.Read(buffer)
		if count > 0 {
			_, _ = hasher.Write(buffer[:count])
		}
		if errors.Is(readErr, io.EOF) {
			return hex.EncodeToString(hasher.Sum(nil)), nil
		}
		if readErr != nil {
			return "", readErr
		}
		if count == 0 {
			return "", io.ErrNoProgress
		}
	}
}
