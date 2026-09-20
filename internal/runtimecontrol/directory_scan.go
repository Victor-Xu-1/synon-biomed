package runtimecontrol

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// scanGate bounds each scanner to one scan while allowing abandoned HTTP
// requests to stop waiting without launching another filesystem traversal.
type scanGate struct {
	once sync.Once
	busy chan struct{}
}

func (g *scanGate) lock(ctx context.Context) error {
	g.once.Do(func() { g.busy = make(chan struct{}, 1) })
	select {
	case g.busy <- struct{}{}:
		if err := ctx.Err(); err != nil {
			g.unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *scanGate) unlock() { <-g.busy }

type fileIdentity struct{ volume, high, low uint64 }

// directoryBytes is deliberately per-directory-entry: the migration copier
// writes each entry separately, so its capacity estimate must remain conservative.
func directoryBytes(root string) (int64, error) {
	return scanDirectoryBytes(context.Background(), false, root)
}

func scanDirectoryBytes(ctx context.Context, unique bool, roots ...string) (int64, error) {
	var total int64
	var firstError error
	var failedEntries int
	seen := make(map[fileIdentity]struct{})
	recordError := func(err error) {
		failedEntries++
		if firstError == nil {
			firstError = err
		}
	}
	for _, root := range independentRoots(roots) {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				if path != root || !errors.Is(walkErr, os.ErrNotExist) {
					recordError(walkErr)
				}
				return nil
			}
			// Never follow links out of the requested storage roots.
			if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				recordError(err)
				return nil
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			if unique {
				key, linked, identityErr := linkedFileIdentity(path, info)
				if identityErr != nil {
					recordError(identityErr)
					return nil
				}
				if linked {
					if _, found := seen[key]; found {
						return nil
					}
					seen[key] = struct{}{}
				}
			}
			if info.Size() < 0 || total > math.MaxInt64-info.Size() {
				return errors.New("storage size exceeds supported range")
			}
			total += info.Size()
			return nil
		})
		if err != nil {
			return total, err
		}
	}
	if firstError != nil {
		return total, fmt.Errorf("%d storage entries could not be measured: %w", failedEntries, firstError)
	}
	return total, nil
}

// One category can own the main runtime root plus an externally configured
// environment root. Walk nested roots only once, without resolving symlinks.
func independentRoots(roots []string) []string {
	result := make([]string, 0, len(roots))
	for index, value := range roots {
		if strings.TrimSpace(value) == "" {
			continue
		}
		root := filepath.Clean(value)
		nested := false
		for otherIndex, other := range roots {
			if index == otherIndex || strings.TrimSpace(other) == "" {
				continue
			}
			rel, err := filepath.Rel(filepath.Clean(other), root)
			if err == nil && ((rel == "." && otherIndex < index) ||
				(rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
				nested = true
				break
			}
		}
		if !nested {
			result = append(result, root)
		}
	}
	return result
}
