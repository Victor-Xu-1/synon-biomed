package fileops

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

func (f *mutationFS) copyFile(source, target string, overwrite bool) error {
	input, err := f.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	if other, err := f.Stat(target); err == nil && os.SameFile(info, other) {
		return errors.New("source and target paths must be different")
	}
	flags := os.O_CREATE | os.O_WRONLY
	if overwrite {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	output, err := f.OpenFile(target, flags, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

func (f *mutationFS) copyDirectory(source, target string, overwrite bool) error {
	if overwrite {
		if err := f.RemoveAll(target); err != nil {
			return err
		}
	}
	return fs.WalkDir(f.FS(), filepath.ToSlash(source), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, filepath.FromSlash(path))
		if err != nil {
			return err
		}
		if rel == "." {
			return f.MkdirAll(target, 0o700)
		}
		targetPath := filepath.Join(target, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return f.MkdirAll(targetPath, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := f.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
			return err
		}
		return f.copyFile(filepath.FromSlash(path), targetPath, false)
	})
}
