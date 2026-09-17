package secrets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	lockFileName  = "vault.lock"
	vaultTempBase = ".vault.enc.tmp-"
	keyTempBase   = ".master.key.tmp-"
)

type processLock struct {
	file *os.File
}

func (s *Store) begin() (func(), error) {
	s.mu.Lock()
	lock, err := s.lockProcess()
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	return func() {
		_ = lock.Close()
		s.mu.Unlock()
	}, nil
}

func (s *Store) lockProcess() (*processLock, error) {
	if err := ensureSecureDirectory(s.dir); err != nil {
		return nil, err
	}
	before, err := os.Stat(s.dir)
	if err != nil {
		return nil, err
	}
	lockPath := filepath.Join(s.dir, lockFileName)
	file, err := openAndLock(lockPath)
	if err != nil {
		return nil, fmt.Errorf("lock secret vault: %w", err)
	}
	lock := &processLock{file: file}
	if err := protectFile(file); err != nil {
		_ = lock.Close()
		return nil, err
	}
	after, err := os.Stat(s.dir)
	if err != nil || !os.SameFile(before, after) {
		_ = lock.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("secret vault directory changed while acquiring lock")
	}
	if err := ensureSecureDirectory(s.dir); err != nil {
		_ = lock.Close()
		return nil, err
	}
	if err := cleanupTemporaryFiles(s.dir); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return lock, nil
}

func (l *processLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := unlockAndClose(l.file)
	l.file = nil
	return err
}

func ensureSecureDirectory(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(absolute)
	current := volume
	rest := strings.TrimPrefix(absolute, volume)
	if filepath.IsAbs(absolute) {
		current += string(os.PathSeparator)
		rest = strings.TrimLeft(rest, string(os.PathSeparator))
	}
	for _, part := range strings.Split(rest, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link", current)
		}
		reparse, err := pathIsReparsePoint(current)
		if err != nil {
			return err
		}
		if reparse {
			return fmt.Errorf("%s is a reparse point", current)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", current)
		}
	}
	return protectPath(absolute, true)
}

func cleanupTemporaryFiles(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, vaultTempBase) && !strings.HasPrefix(name, keyTempBase) {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
			return fmt.Errorf("temporary vault path %s is a directory", name)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale secret vault temporary file %s: %w", name, err)
		}
	}
	return nil
}
