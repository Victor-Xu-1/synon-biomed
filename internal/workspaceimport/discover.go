package workspaceimport

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

var dataRootDatabaseCandidates = []string{
	"operon.db",
	"operon-cli.db",
	filepath.FromSlash(WorkspaceDatabaseRelative),
	"synonbiomed-v1.1.sqlite",
}

func discoverSources(sourcePath string) ([]discoveredSource, error) {
	canonical, info, err := canonicalExistingPath(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("resolve import source: %w", err)
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return nil, errors.New("import source is neither a directory nor a regular database file")
		}
		return []discoveredSource{{database: canonical}}, nil
	}

	candidates := make([]discoveredSource, 0, 4)
	if database := firstDataRootDatabase(canonical); database != "" {
		candidates = append(candidates, discoveredSource{database: database, dataRoot: canonical})
	}
	orgsRoot := filepath.Join(canonical, "orgs")
	entries, readErr := os.ReadDir(orgsRoot)
	if readErr == nil {
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
				continue
			}
			root := filepath.Join(orgsRoot, entry.Name())
			if database := firstDataRootDatabase(root); database != "" {
				candidates = append(candidates, discoveredSource{database: database, dataRoot: root})
			}
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return nil, fmt.Errorf("read import organization directory: %w", readErr)
	}

	seen := make(map[string]struct{}, len(candidates))
	result := make([]discoveredSource, 0, len(candidates))
	for _, candidate := range candidates {
		database, databaseInfo, canonicalErr := canonicalExistingPath(candidate.database)
		if canonicalErr != nil {
			return nil, fmt.Errorf("resolve import database: %w", canonicalErr)
		}
		if !databaseInfo.Mode().IsRegular() {
			return nil, errors.New("import database is not a regular file")
		}
		if _, duplicate := seen[database]; duplicate {
			continue
		}
		seen[database] = struct{}{}
		candidate.database = database
		result = append(result, candidate)
	}
	if len(result) == 0 {
		return nil, errors.New("no importable database found")
	}
	sort.Slice(result, func(i, j int) bool { return result[i].database < result[j].database })
	return result, nil
}

func firstDataRootDatabase(root string) string {
	for _, relative := range dataRootDatabaseCandidates {
		candidate := filepath.Join(root, relative)
		info, err := os.Lstat(candidate)
		if err == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	return ""
}

func canonicalExistingPath(path string) (string, os.FileInfo, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", nil, err
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(absolute))
	if err != nil {
		return "", nil, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", nil, err
	}
	return filepath.Clean(resolved), info, nil
}
