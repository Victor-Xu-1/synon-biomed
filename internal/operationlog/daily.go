// Package operationlog provides the bounded daily operational log used by the
// local runtime. Task history stays in SQLite; this log is only for service
// diagnostics and is deleted after the reference seven-day window.
package operationlog

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const Retention = 7 * 24 * time.Hour

type DailyWriter struct {
	mu          sync.Mutex
	root        string
	now         func() time.Time
	currentDate string
	file        *os.File
}

func Open(root string) (*DailyWriter, error) {
	return openWithClock(root, time.Now)
}

func openWithClock(root string, now func() time.Time) (*DailyWriter, error) {
	root = strings.TrimSpace(root)
	if root == "" || now == nil {
		return nil, errors.New("operational log root and clock are required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve operational log root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("prepare operational log root: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, fmt.Errorf("inspect operational log root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("operational log root must be a directory")
	}
	if err := os.Chmod(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("secure operational log root: %w", err)
	}
	writer := &DailyWriter{root: absolute, now: now}
	if err := writer.removeExpired(); err != nil {
		return nil, err
	}
	return writer, nil
}

func (w *DailyWriter) Write(payload []byte) (int, error) {
	if w == nil {
		return 0, errors.New("operational log writer is closed")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	date := w.now().UTC().Format("20060102")
	if w.file == nil || w.currentDate != date {
		if err := w.rotate(date); err != nil {
			return 0, err
		}
	}
	return w.file.Write(payload)
}

func (w *DailyWriter) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	w.currentDate = ""
	return err
}

func (w *DailyWriter) rotate(date string) error {
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return err
		}
		w.file = nil
	}
	path := filepath.Join(w.root, "server-"+date+".log")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("operational log target must be a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open daily operational log: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("secure daily operational log: %w", err)
	}
	w.file = file
	w.currentDate = date
	return nil
}

func (w *DailyWriter) removeExpired() error {
	entries, err := os.ReadDir(w.root)
	if err != nil {
		return fmt.Errorf("list operational logs: %w", err)
	}
	cutoff := w.now().UTC().Add(-Retention)
	for _, entry := range entries {
		path := filepath.Join(w.root, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect operational log %q: %w", entry.Name(), err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove expired operational log %q: %w", entry.Name(), err)
		}
	}
	return nil
}

var _ io.Writer = (*DailyWriter)(nil)
