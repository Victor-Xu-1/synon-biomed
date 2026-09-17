package compute

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
)

const (
	RemoteDirectoryLimit   = 1000
	RemoteImportMaxBytes   = 50 << 20
	RemoteDownloadMaxBytes = 2 << 30
)

type RemoteError struct {
	Kind    string `json:"remoteKind"`
	Message string `json:"detail"`
}

func (e *RemoteError) Error() string { return e.Message }

type RemoteEntry struct {
	Name        string `json:"name"`
	IsDirectory bool   `json:"isDirectory"`
	Size        int64  `json:"size"`
	MTime       int64  `json:"mtime,omitempty"`
}

type RemoteDirectory struct {
	Entries      []RemoteEntry  `json:"entries"`
	Truncated    bool           `json:"truncated"`
	Roots        map[string]any `json:"roots"`
	ResolvedPath string         `json:"resolvedPath"`
}

type RemoteDownload struct {
	Path     string
	Filename string
	Size     int64
}

func (d RemoteDownload) Cleanup() error {
	if strings.TrimSpace(d.Path) == "" {
		return nil
	}
	return os.RemoveAll(path.Dir(d.Path))
}

type RemoteFileClient interface {
	RealPath(string) (string, error)
	ReadDir(string) ([]os.FileInfo, error)
	Stat(string) (os.FileInfo, error)
	Open(string) (io.ReadCloser, error)
	Close() error
}

type RemoteDialer interface {
	Dial(context.Context, string) (RemoteFileClient, error)
}

type OpenSSHRemoteDialer struct {
	Executable string
	Timeout    time.Duration
	ConfigPath string
}

func (d OpenSSHRemoteDialer) Dial(ctx context.Context, alias string) (RemoteFileClient, error) {
	if !sshAliasPattern.MatchString(alias) || strings.HasPrefix(alias, "-") {
		return nil, remoteError("outside_roots", "invalid SSH provider alias", nil)
	}
	executable := strings.TrimSpace(d.Executable)
	if executable == "" {
		executable = "ssh"
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	commandContext, cancel := context.WithCancel(ctx)
	arguments := []string{
		"-o", "BatchMode=yes",
		"-o", fmt.Sprintf("ConnectTimeout=%d", max(1, int(timeout.Seconds()))),
	}
	if configPath := strings.TrimSpace(d.ConfigPath); configPath != "" {
		arguments = append(arguments, "-F", configPath)
	}
	arguments = append(arguments, "-s", alias, "sftp")
	command := exec.CommandContext(commandContext, executable, arguments...)
	stdin, err := command.StdinPipe()
	if err != nil {
		cancel()
		return nil, remoteError("connection", "could not open SSH stdin", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		return nil, remoteError("connection", "could not open SSH stdout", err)
	}
	stderr := &boundedRemoteBuffer{limit: 16 << 10}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		cancel()
		return nil, remoteError("connection", "could not start SSH", err)
	}
	client, err := sftp.NewClientPipe(stdout, stdin)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		cancel()
		return nil, remoteError("connection", "could not establish SFTP: "+stderr.String(), err)
	}
	return &openSSHRemoteClient{Client: client, stdin: stdin, command: command, cancel: cancel, stderr: stderr}, nil
}

type openSSHRemoteClient struct {
	*sftp.Client
	stdin   io.WriteCloser
	command *exec.Cmd
	cancel  context.CancelFunc
	stderr  *boundedRemoteBuffer
	once    sync.Once
}

func (c *openSSHRemoteClient) Open(name string) (io.ReadCloser, error) {
	return c.Client.Open(name)
}

func (c *openSSHRemoteClient) Close() error {
	var result error
	c.once.Do(func() {
		clientErr := c.Client.Close()
		_ = c.stdin.Close()
		waitErr := c.command.Wait()
		c.cancel()
		if clientErr != nil {
			result = clientErr
		} else if waitErr != nil {
			result = fmt.Errorf("SSH transport exited: %w: %s", waitErr, c.stderr.String())
		}
	})
	return result
}

type boundedRemoteBuffer struct {
	mu    sync.Mutex
	data  bytes.Buffer
	limit int
}

func (b *boundedRemoteBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	original := len(value)
	if remaining := b.limit - b.data.Len(); remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = b.data.Write(value)
	}
	return original, nil
}

func (b *boundedRemoteBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(b.data.String())
}

func ListRemoteDirectory(ctx context.Context, dialer RemoteDialer, alias, requestedPath, scratchRoot, home string) (RemoteDirectory, error) {
	if dialer == nil {
		return RemoteDirectory{}, remoteError("connection", "remote file transport is unavailable", nil)
	}
	if err := validateRemotePath(requestedPath); err != nil {
		return RemoteDirectory{}, err
	}
	client, err := dialer.Dial(ctx, alias)
	if err != nil {
		return RemoteDirectory{}, classifyRemoteError(err, "Could not connect to SSH host")
	}
	defer client.Close()
	resolved, err := client.RealPath(requestedPath)
	if err != nil {
		return RemoteDirectory{}, classifyRemoteError(err, "Can't resolve "+requestedPath)
	}
	items, err := client.ReadDir(resolved)
	if err != nil {
		return RemoteDirectory{}, classifyRemoteError(err, "Couldn't list "+requestedPath)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].IsDir() != items[j].IsDir() {
			return items[i].IsDir()
		}
		return items[i].Name() < items[j].Name()
	})
	truncated := len(items) > RemoteDirectoryLimit
	if truncated {
		items = items[:RemoteDirectoryLimit]
	}
	entries := make([]RemoteEntry, 0, len(items))
	for _, item := range items {
		size := item.Size()
		if item.IsDir() {
			size = 0
		}
		entries = append(entries, RemoteEntry{
			Name: item.Name(), IsDirectory: item.IsDir(), Size: size, MTime: item.ModTime().UnixMilli(),
		})
	}
	roots := map[string]any{}
	if scratchRoot != "" {
		roots["scratch"] = scratchRoot
	}
	if home != "" {
		roots["home"] = home
	}
	return RemoteDirectory{Entries: entries, Truncated: truncated, Roots: roots, ResolvedPath: resolved}, nil
}

func FetchRemoteFile(ctx context.Context, dialer RemoteDialer, alias, requestedPath string, maxBytes int64) (RemoteDownload, error) {
	if dialer == nil {
		return RemoteDownload{}, remoteError("connection", "remote file transport is unavailable", nil)
	}
	if err := validateRemotePath(requestedPath); err != nil {
		return RemoteDownload{}, err
	}
	if maxBytes <= 0 {
		return RemoteDownload{}, remoteError("too_large", "remote transfer limit must be positive", nil)
	}
	client, err := dialer.Dial(ctx, alias)
	if err != nil {
		return RemoteDownload{}, classifyRemoteError(err, "Could not connect to SSH host")
	}
	defer client.Close()
	resolved, err := client.RealPath(requestedPath)
	if err != nil {
		return RemoteDownload{}, classifyRemoteError(err, "Can't resolve "+requestedPath)
	}
	info, err := client.Stat(resolved)
	if err != nil {
		return RemoteDownload{}, classifyRemoteError(err, "Can't stat "+requestedPath)
	}
	if !info.Mode().IsRegular() {
		return RemoteDownload{}, remoteError("not_a_file", path.Base(requestedPath)+" isn't a regular file", nil)
	}
	if info.Size() == 0 {
		return RemoteDownload{}, remoteError("not_a_file", path.Base(requestedPath)+" is empty", nil)
	}
	if info.Size() > maxBytes {
		return RemoteDownload{}, remoteError("too_large", fmt.Sprintf("file exceeds the %d byte transfer limit", maxBytes), nil)
	}
	source, err := client.Open(resolved)
	if err != nil {
		return RemoteDownload{}, classifyRemoteError(err, "Download failed")
	}
	defer source.Close()
	directory, err := os.MkdirTemp("", "synon-ssh-download-")
	if err != nil {
		return RemoteDownload{}, remoteError("other", "could not create transfer directory", err)
	}
	targetPath := path.Join(directory, "download")
	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = os.RemoveAll(directory)
		return RemoteDownload{}, remoteError("other", "could not create transfer file", err)
	}
	written, copyErr := io.Copy(target, io.LimitReader(source, maxBytes+1))
	closeErr := target.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.RemoveAll(directory)
		return RemoteDownload{}, classifyRemoteError(errors.Join(copyErr, closeErr), "Download failed")
	}
	if written > maxBytes {
		_ = os.RemoveAll(directory)
		return RemoteDownload{}, remoteError("too_large", "file grew past the transfer limit", nil)
	}
	return RemoteDownload{Path: targetPath, Filename: safeRemoteFilename(path.Base(resolved)), Size: written}, nil
}

func validateRemotePath(value string) error {
	if !path.IsAbs(value) {
		return remoteError("outside_roots", "Path must be absolute", nil)
	}
	if strings.ContainsAny(value, "\x00\n\r\\") {
		return remoteError("outside_roots", "Path contains unsupported control characters or backslashes", nil)
	}
	return nil
}

func safeRemoteFilename(value string) string {
	value = strings.TrimSpace(path.Base(value))
	if value == "" || value == "." || value == "/" {
		return "download"
	}
	return strings.Map(func(character rune) rune {
		if character < 32 || character == 127 || character == '/' || character == '\\' {
			return -1
		}
		return character
	}, value)
}

func classifyRemoteError(err error, prefix string) error {
	var remote *RemoteError
	if errors.As(err, &remote) {
		return remote
	}
	if os.IsNotExist(err) {
		return remoteError("not_found", prefix, err)
	}
	if os.IsPermission(err) {
		return remoteError("permission", prefix, err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "not a directory") {
		return remoteError("not_a_directory", prefix, err)
	}
	var status *sftp.StatusError
	if errors.As(err, &status) {
		switch status.Code {
		case 2, 10:
			return remoteError("not_found", prefix, err)
		case 3:
			return remoteError("permission", prefix, err)
		case 19:
			return remoteError("not_a_directory", prefix, err)
		case 6, 7:
			return remoteError("connection", prefix, err)
		}
	}
	return remoteError("other", prefix, err)
}

func remoteError(kind, message string, cause error) error {
	if cause != nil {
		message += ": " + cause.Error()
	}
	return &RemoteError{Kind: kind, Message: message}
}
