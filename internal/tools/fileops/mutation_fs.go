package fileops

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"synon-go/internal/tools/fileevents"
)

// mutationFS keeps actual I/O anchored to an opened directory, even if a path
// component changes after resolution. Resolution preserves absolute in-root
// symlinks; it is not the security boundary for subsequent filesystem calls.
type mutationFS struct {
	*os.Root
	base      string
	canonical string
}

func openMutationFS(root string) (*mutationFS, error) {
	base, _, err := resolvePath(root, ".")
	if err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(base)
	if err != nil {
		return nil, err
	}
	handle, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	return &mutationFS{Root: handle, base: base, canonical: canonical}, nil
}

func (f *mutationFS) resolve(requested string) (string, string, error) {
	_, display, err := resolvePath(f.base, requested)
	if err != nil {
		return "", "", err
	}
	pending := strings.Split(display, string(filepath.Separator))
	resolved, links := ".", 0
	for len(pending) != 0 {
		candidate := filepath.Join(resolved, pending[0])
		pending = pending[1:]
		info, err := f.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			return filepath.Join(append([]string{candidate}, pending...)...), display, nil
		}
		if err != nil {
			return "", "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			resolved = candidate
			continue
		}
		links++
		if links > 40 {
			return "", "", fmt.Errorf("too many symbolic links: %s", display)
		}
		link, err := f.Readlink(candidate)
		if err != nil {
			return "", "", err
		}
		if filepath.IsAbs(link) {
			raw := link
			_, link, err = resolvePath(f.base, raw)
			if err != nil {
				// A configured root may itself be a symlink.
				_, link, err = resolvePath(f.canonical, raw)
			}
		} else {
			link = filepath.Join(resolved, link)
		}
		if err != nil || !filepath.IsLocal(link) {
			return "", "", fmt.Errorf("path escapes file root: %s", display)
		}
		pending = append(strings.Split(link, string(filepath.Separator)), pending...)
		resolved = "."
	}
	return resolved, display, nil
}

func (f *mutationFS) exists(path string) bool {
	_, err := f.Lstat(path)
	return err == nil
}

func (f *mutationFS) ensureWritable(path string, overwrite bool) error {
	if overwrite {
		return nil
	}
	if _, err := f.Lstat(path); err == nil {
		return fmt.Errorf("target path already exists: %s", filepath.ToSlash(path))
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (f *mutationFS) ensureEntryAbsent(requested string) error {
	parent, _, err := f.resolve(filepath.Dir(requested))
	if err != nil {
		return err
	}
	return f.ensureWritable(filepath.Join(parent, filepath.Base(requested)), false)
}

func (f *mutationFS) createFile(path string, data []byte) error {
	file, err := f.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func (f *mutationFS) notify(paths ...string) {
	absolute := make([]string, 0, len(paths))
	for _, path := range paths {
		absolute = append(absolute, filepath.Join(f.base, path))
		if f.canonical != f.base {
			absolute = append(absolute, filepath.Join(f.canonical, path))
		}
	}
	fileevents.NotifyChanged(absolute...)
}
