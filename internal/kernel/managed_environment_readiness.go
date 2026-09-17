package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const managedEnvironmentValidationRevision = 1

// A receipt from an installer that did not enforce link-script outcomes must
// not authorize reuse of packages whose setup depends on those scripts. Keep
// the old immutable generation for running workers and evidence; preparation
// creates a new verified generation through the existing publication path.
var ErrManagedEnvironmentRebuildRequired = errors.New("managed environment requires preparation with the current installation verification contract")

func managedEnvironmentNeedsRebuild(prefix string, marker managedEnvironmentMarker) (bool, error) {
	if marker.Kind == "path-venv" || marker.ValidationRevision == managedEnvironmentValidationRevision {
		return false, nil
	}
	root, err := os.OpenRoot(prefix)
	if err != nil {
		return false, err
	}
	defer root.Close()
	for _, directory := range []string{"bin", "Scripts"} {
		entries, err := readManagedInventoryDirectory(root, directory, 16384)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		for _, entry := range entries {
			if managedInstallationHookPath(directory + "/" + entry.Name()) {
				return true, nil
			}
		}
	}
	// Some installers remove hook files after linking. Their durable package
	// records still declare that installation depended on a hook outcome.
	entries, err := readManagedInventoryDirectory(root, "conda-meta", 4096)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var record struct {
			Files []string `json:"files"`
		}
		if err := readManagedPackageMetadata(root, filepath.Join("conda-meta", entry.Name()), &record); err != nil {
			return false, err
		}
		for _, item := range record.Files {
			if managedInstallationHookPath(item) {
				return true, nil
			}
		}
	}
	return false, nil
}

func managedInstallationHookPath(item string) bool {
	item = strings.ReplaceAll(item, "\\", "/")
	if path.Clean(item) != item || !(strings.HasPrefix(item, "bin/.") || strings.HasPrefix(item, "Scripts/.")) {
		return false
	}
	for _, suffix := range []string{"-pre-link.sh", "-post-link.sh", "-pre-link.bat", "-post-link.bat"} {
		if strings.HasSuffix(item, suffix) {
			return true
		}
	}
	return false
}

// Derive R runtime witnesses from package-manager records, not task text or a
// list of scientific engines. DESCRIPTION paths preserve namespace case.
// Data-only R distributions declare their namespace through the packaging
// namespace and install it in a post-link hook, rather than shipping files.
func managedRPackageWitnesses(ctx context.Context, prefix string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(prefix)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	entries, err := readManagedInventoryDirectory(root, "conda-meta", 4096)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var record struct {
			Name          string   `json:"name"`
			RequestedSpec string   `json:"requested_spec"`
			Files         []string `json:"files"`
		}
		if err := readManagedPackageMetadata(root, filepath.Join("conda-meta", entry.Name()), &record); err != nil {
			return nil, fmt.Errorf("managed package metadata is invalid: %w", err)
		}
		if !managedPackageName.MatchString(record.Name) || len(record.Files) > 100000 {
			return nil, errors.New("managed package runtime inventory is invalid")
		}
		declared := map[string]bool{}
		files := make(map[string]bool, len(record.Files))
		for _, name := range record.Files {
			files[strings.ReplaceAll(name, "\\", "/")] = true
		}
		hasHook := false
		for _, raw := range record.Files {
			name := strings.ReplaceAll(raw, "\\", "/")
			if path.Clean(name) != name || path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
				return nil, errors.New("managed package runtime path escapes its environment")
			}
			if name == "bin/."+record.Name+"-post-link.sh" || name == "Scripts/."+record.Name+"-post-link.bat" {
				hasHook = true
			}
			for _, library := range []string{"lib/R/library/", "Library/lib/R/library/"} {
				if !strings.HasPrefix(name, library) || !strings.HasSuffix(name, "/DESCRIPTION") {
					continue
				}
				namespace := strings.TrimSuffix(strings.TrimPrefix(name, library), "/DESCRIPTION")
				if strings.Contains(namespace, "/") || !managedImportName.MatchString(namespace) {
					continue
				}
				info, err := root.Stat(filepath.FromSlash(name))
				if err != nil || !info.Mode().IsRegular() {
					return nil, fmt.Errorf("managed R package payload is missing: %s", namespace)
				}
				// Runtime distributions also ship resource trees with DESCRIPTION
				// files. Installed package metadata, rather than a directory name,
				// identifies a loadable package (including base/legacy namespaces).
				metadata := path.Join(path.Dir(name), "Meta/package.rds")
				if !files[metadata] {
					continue
				}
				info, err = root.Stat(filepath.FromSlash(metadata))
				if err != nil || !info.Mode().IsRegular() {
					return nil, fmt.Errorf("managed R installed-package metadata is missing: %s", namespace)
				}
				declared[namespace] = true
			}
		}
		if hasHook && len(declared) == 0 {
			for _, namespace := range []string{"r-", "bioconductor-"} {
				if strings.HasPrefix(record.Name, namespace) {
					name := strings.TrimPrefix(record.Name, namespace)
					if managedImportName.MatchString(name) {
						names[name] = true
					}
				}
			}
		}
		if record.RequestedSpec != "" {
			for name := range declared {
				names[name] = true
			}
		}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

func readManagedInventoryDirectory(root *os.Root, name string, limit int) ([]os.DirEntry, error) {
	directory, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > limit {
		return nil, errors.New("managed environment inventory exceeds the supported limit")
	}
	return entries, nil
}

func readManagedPackageMetadata(root *os.Root, name string, record any) error {
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	const maxRecordBytes = 8 * 1024 * 1024
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxRecordBytes {
		return errors.New("package record is outside the supported size")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxRecordBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxRecordBytes {
		return errors.New("package record is outside the supported size")
	}
	// External package metadata may add fields independently of our consumer.
	// Standard JSON decoding still rejects malformed/trailing documents.
	return json.Unmarshal(raw, record)
}

func validateManagedRPackageInventory(ctx context.Context, prefix string) error {
	names, err := managedRPackageWitnesses(ctx, prefix)
	if err != nil {
		return err
	}
	// The internal transitive inventory may exceed the public request's count
	// bound. Validate every namespace in bounded batches, without dropping any.
	for len(names) > 0 {
		count := min(len(names), maxManagedEnvironmentPackages)
		if err := validateManagedEnvironmentImports(ctx, "r", prefix, names[:count]); err != nil {
			return err
		}
		names = names[count:]
	}
	return nil
}
