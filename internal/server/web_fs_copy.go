package server

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func copyWebFSAtomic(source, target string, state *webFSCopyState) error {
	if filepath.Clean(source) == filepath.Clean(target) {
		return fmt.Errorf("%w: source and destination are identical", errWebFSConflict)
	}
	if _, err := os.Lstat(target); err == nil {
		return errWebFSConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errWebFSUnsupported
	}
	if info.IsDir() && webFSPathWithin(source, target) {
		return fmt.Errorf("%w: destination cannot be inside source", errWebFSInvalid)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	if info.IsDir() {
		temp, err := os.MkdirTemp(filepath.Dir(target), ".synon-copy-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(temp)
		if err := copyWebFSDirectory(source, temp, state); err != nil {
			return err
		}
		return os.Rename(temp, target)
	}
	if !info.Mode().IsRegular() {
		return errWebFSUnsupported
	}
	state.entries++
	if state.entries > maxWebFSCopyEntries {
		return errWebFSTooMany
	}
	temp, err := os.CreateTemp(filepath.Dir(target), ".synon-copy-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := copyWebFSRegularFile(source, temp, info.Size(), state); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, target)
}

func copyWebFSDirectory(source, target string, state *webFSCopyState) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errWebFSUnsupported
		}
		state.entries++
		if state.entries > maxWebFSCopyEntries {
			return errWebFSTooMany
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.MkdirAll(destination, 0o700)
		}
		if !info.Mode().IsRegular() {
			return errWebFSUnsupported
		}
		file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if err := copyWebFSRegularFile(path, file, info.Size(), state); err != nil {
			_ = file.Close()
			return err
		}
		return file.Close()
	})
}

func copyWebFSRegularFile(source string, destination *os.File, size int64, state *webFSCopyState) error {
	if size < 0 || size > maxWebFSCopyBytes-state.bytes {
		return errWebFSTooLarge
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	written, err := io.Copy(destination, io.LimitReader(input, size+1))
	if err != nil {
		return err
	}
	if written != size || written > maxWebFSCopyBytes-state.bytes {
		return errWebFSTooLarge
	}
	state.bytes += written
	return nil
}
