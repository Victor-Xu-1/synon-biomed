package runtimecontrol

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Environment storage includes retained immutable generations, not just the
// active runtime. Scan physical directories without following activation links.
func environmentUsageRoots(root string) (map[string][]string, []string) {
	roots := make(map[string][]string)
	warnings := []string{}
	readDirectories := func(parent string, add func(string)) {
		info, err := os.Lstat(parent)
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if err != nil {
			warnings = append(warnings, err.Error())
			return
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			warnings = append(warnings, "environment storage directory cannot be scanned safely")
			return
		}
		entries, err := os.ReadDir(parent)
		if err != nil {
			warnings = append(warnings, err.Error())
			return
		}
		for _, entry := range entries {
			if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 &&
				!strings.HasPrefix(entry.Name(), ".") && condaEnvironmentName.MatchString(entry.Name()) {
				add(entry.Name())
			}
		}
	}
	readDirectories(root, func(name string) { roots[name] = append(roots[name], filepath.Join(root, name)) })
	readDirectories(filepath.Join(root, ".generations"), func(name string) {
		roots[name] = append(roots[name], filepath.Join(root, ".generations", name))
	})
	return roots, warnings
}

func boundedEnvironmentNames(roots map[string][]string) ([]string, bool) {
	names := make([]string, 0, len(roots))
	for name := range roots {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > maxCondaEnvironments {
		return names[:maxCondaEnvironments], true
	}
	return names, false
}
