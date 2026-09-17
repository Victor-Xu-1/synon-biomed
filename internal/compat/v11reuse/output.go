package v11reuse

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var ErrManifestDrift = errors.New("reuse manifest drift")

func WriteOutput(target string, data []byte, check bool) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return errors.New("output target is required")
	}
	absolute, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("resolve output target: %w", err)
	}

	if info, statErr := os.Lstat(absolute); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: output target is a symlink: %s", ErrPathEscape, absolute)
		}
		if info.IsDir() {
			return fmt.Errorf("output target is a directory: %s", absolute)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect output target: %w", statErr)
	}

	if check {
		existing, readErr := os.ReadFile(absolute)
		if readErr != nil {
			return fmt.Errorf("%w: read %s: %v", ErrManifestDrift, absolute, readErr)
		}
		if !bytes.Equal(existing, data) {
			return fmt.Errorf(
				"%w: %s has sha256 %s, generated sha256 %s",
				ErrManifestDrift,
				absolute,
				bytesSHA256(existing),
				bytesSHA256(data),
			)
		}
		return nil
	}

	parent := filepath.Dir(absolute)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	temporary, err := os.CreateTemp(parent, "."+filepath.Base(absolute)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary output: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set output permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary output: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary output: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary output: %w", err)
	}
	if err := os.Rename(temporaryPath, absolute); err != nil {
		return fmt.Errorf("activate output: %w", err)
	}
	removeTemporary = false

	if directory, openErr := os.Open(parent); openErr == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

func ReviewMarkdown(manifest Manifest) []byte {
	var output strings.Builder
	output.WriteString("# SynonBiomed v1.1 Reuse Review\n\n")
	output.WriteString("This file is generated from the deterministic reuse manifest. It is a review aid, not a substitute for runtime evidence.\n\n")
	output.WriteString("## Baseline\n\n")
	output.WriteString("- Schema version: " + strconv.Itoa(manifest.SchemaVersion) + "\n")
	output.WriteString("- Baseline: " + markdownCell(manifest.BaselineName) + "\n")
	if manifest.SourceCommit != "" {
		output.WriteString("- Source commit: " + markdownCell(manifest.SourceCommit) + "\n")
	}
	output.WriteString("- Source tree SHA-256: " + manifest.SourceTreeSHA256 + "\n")
	output.WriteString("- Entries: " + strconv.Itoa(manifest.Summary.Entries) + "\n")
	output.WriteString("- Files: " + strconv.Itoa(manifest.Summary.Files) + "\n")
	output.WriteString("- Symlinks: " + strconv.Itoa(manifest.Summary.Symlinks) + "\n")
	output.WriteString("- Bytes: " + strconv.FormatInt(manifest.Summary.TotalBytes, 10) + "\n")
	output.WriteString("- Release-required entries: " + strconv.Itoa(manifest.Summary.ReleaseEntries) + "\n")
	output.WriteString("- Main-runtime-wired entries: " + strconv.Itoa(manifest.Summary.WiredEntries) + "\n\n")

	output.WriteString("## Dispositions\n\n")
	output.WriteString("| ID | Source | Category | Decision | Target | Wired | Release | Files | Bytes | SHA-256 |\n")
	output.WriteString("| --- | --- | --- | --- | --- | ---: | ---: | ---: | ---: | --- |\n")
	for _, entry := range manifest.Entries {
		output.WriteString("| " + markdownCell(entry.ID))
		output.WriteString(" | " + markdownCell(entry.SourcePath))
		output.WriteString(" | " + markdownCell(entry.Category))
		output.WriteString(" | " + markdownCell(string(entry.Decision)))
		output.WriteString(" | " + markdownCell(entry.TargetPath))
		output.WriteString(" | " + strconv.FormatBool(entry.MainRuntimeWired))
		output.WriteString(" | " + strconv.FormatBool(entry.ReleaseRequired))
		output.WriteString(" | " + strconv.Itoa(entry.FileCount))
		output.WriteString(" | " + strconv.FormatInt(entry.TotalBytes, 10))
		output.WriteString(" | " + entry.SHA256 + " |\n")
	}

	output.WriteString("\n## Review Rules\n\n")
	output.WriteString("- direct_copy still requires license and dependency review before release.\n")
	output.WriteString("- thin_adapter and upstream_integration require pinned managed-runtime manifests and process tests.\n")
	output.WriteString("- mechanical_port and black_box_reimplementation require differential behavioral evidence.\n")
	output.WriteString("- frontend_owned covers visual implementation only; backend contracts remain in runtime scope.\n")
	output.WriteString("- runtime_state_excluded must never be copied into source or release archives.\n")
	return []byte(output.String())
}

func bytesSHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func markdownCell(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}
