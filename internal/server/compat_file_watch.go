package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	compatFileWatchLimit    = 64
	compatFileWatchInterval = 250 * time.Millisecond
)

type compatFileWatchSet struct {
	ctx     context.Context
	root    string
	output  chan<- map[string]any
	mu      sync.Mutex
	watches map[string]context.CancelFunc
	closed  bool
}

type compatFileSnapshot struct {
	exists  bool
	size    int64
	mtimeNS int64
	mode    os.FileMode
}

func newCompatFileWatchSet(ctx context.Context, root string, output chan<- map[string]any) *compatFileWatchSet {
	if ctx == nil {
		ctx = context.Background()
	}
	return &compatFileWatchSet{
		ctx: ctx, root: strings.TrimSpace(root), output: output,
		watches: make(map[string]context.CancelFunc),
	}
}

func (w *compatFileWatchSet) Control(message map[string]any) map[string]any {
	switch stringValue(message["type"]) {
	case "file_watch":
		path, err := resolveCompatWatchPath(w.root, stringValue(message["path"]))
		if err != nil {
			return map[string]any{"type": "error", "code": "FILE_WATCH_REJECTED", "detail": err.Error()}
		}
		if err := w.add(path); err != nil {
			return map[string]any{"type": "error", "code": "FILE_WATCH_REJECTED", "detail": err.Error()}
		}
		return nil
	case "file_unwatch":
		path, err := resolveCompatWatchPath(w.root, stringValue(message["path"]))
		if err != nil {
			return nil
		}
		w.remove(path)
		return nil
	default:
		return nil
	}
}

func (w *compatFileWatchSet) add(path string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return errors.New("file watch connection is closed")
	}
	if _, found := w.watches[path]; found {
		return nil
	}
	if len(w.watches) >= compatFileWatchLimit {
		return errors.New("file watch limit reached")
	}
	initial := statCompatWatchPath(path)
	ctx, cancel := context.WithCancel(w.ctx)
	w.watches[path] = cancel
	go w.run(ctx, path, initial)
	return nil
}

func (w *compatFileWatchSet) remove(path string) {
	w.mu.Lock()
	cancel := w.watches[path]
	delete(w.watches, path)
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (w *compatFileWatchSet) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	cancels := make([]context.CancelFunc, 0, len(w.watches))
	for _, cancel := range w.watches {
		cancels = append(cancels, cancel)
	}
	clear(w.watches)
	w.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (w *compatFileWatchSet) run(ctx context.Context, path string, previous compatFileSnapshot) {
	ticker := time.NewTicker(compatFileWatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current := statCompatWatchPath(path)
			message := compatFileWatchMessage(path, previous, current)
			previous = current
			if message == nil {
				continue
			}
			select {
			case w.output <- message:
			case <-ctx.Done():
				return
			}
		}
	}
}

func statCompatWatchPath(path string) compatFileSnapshot {
	info, err := os.Lstat(path)
	if err != nil {
		return compatFileSnapshot{}
	}
	return compatFileSnapshot{
		exists: true, size: info.Size(),
		mtimeNS: info.ModTime().UnixNano(), mode: info.Mode(),
	}
}

func compatFileWatchMessage(path string, before, after compatFileSnapshot) map[string]any {
	if before.exists && !after.exists {
		return map[string]any{"type": "file_deleted", "host": "local", "path": path}
	}
	if !after.exists {
		return nil
	}
	if before.exists && before.size == after.size && before.mtimeNS == after.mtimeNS && before.mode == after.mode {
		return nil
	}
	return map[string]any{
		"type": "file_changed", "host": "local", "path": path,
		"mtime": float64(after.mtimeNS) / float64(time.Millisecond), "size": after.size,
	}
}

func resolveCompatWatchPath(root, requested string) (string, error) {
	root = strings.TrimSpace(root)
	requested = strings.TrimSpace(requested)
	if root == "" {
		return "", errors.New("file root is not configured")
	}
	if requested == "" {
		return "", errors.New("file watch path is required")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", errors.New("file root cannot be resolved")
	}
	realRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return "", errors.New("file root cannot be inspected")
	}
	path := requested
	if !filepath.IsAbs(path) {
		path = filepath.Join(realRoot, path)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", errors.New("file watch path cannot be resolved")
	}
	if !compatPathWithin(realRoot, path) {
		return "", errors.New("file watch path escapes the configured root")
	}
	probe := path
	for {
		resolved, resolveErr := filepath.EvalSymlinks(probe)
		if resolveErr == nil {
			if !compatPathWithin(realRoot, resolved) {
				return "", errors.New("file watch path resolves outside the configured root")
			}
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", errors.New("file watch path cannot be inspected")
		}
		probe = parent
	}
	return filepath.Clean(path), nil
}

func compatPathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
