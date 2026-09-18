package fileops

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

func Replace(root string, requestedPath string, oldText string, newText string) (ReplaceResult, error) {
	if oldText == "" {
		return ReplaceResult{}, errors.New("file_replace.old is required")
	}
	fs, err := openMutationFS(root)
	if err != nil {
		return ReplaceResult{}, err
	}
	defer fs.Close()
	target, rel, err := fs.resolve(requestedPath)
	if err != nil {
		return ReplaceResult{}, err
	}
	info, err := fs.Stat(target)
	if err != nil {
		return ReplaceResult{}, err
	}
	if info.IsDir() {
		return ReplaceResult{}, fmt.Errorf("%s is a directory", requestedPath)
	}
	raw, err := fs.ReadFile(target)
	if err != nil {
		return ReplaceResult{}, err
	}
	content := string(raw)
	replacements := strings.Count(content, oldText)
	if replacements == 0 {
		return ReplaceResult{Path: filepath.ToSlash(rel), Replacements: 0, Bytes: len(raw)}, nil
	}
	updated := strings.ReplaceAll(content, oldText, newText)
	if err := fs.WriteFile(target, []byte(updated), 0o600); err != nil {
		return ReplaceResult{}, err
	}
	fs.notify(target, rel)
	return ReplaceResult{Path: filepath.ToSlash(rel), Replacements: replacements, Bytes: len(updated)}, nil
}

func Patch(root string, requestedPath string, operations []PatchOperation) (PatchResult, error) {
	if len(operations) == 0 {
		return PatchResult{}, errors.New("file_patch.operations is required")
	}
	fs, err := openMutationFS(root)
	if err != nil {
		return PatchResult{}, err
	}
	defer fs.Close()
	target, rel, err := fs.resolve(requestedPath)
	if err != nil {
		return PatchResult{}, err
	}
	info, err := fs.Stat(target)
	if err != nil {
		return PatchResult{}, err
	}
	if info.IsDir() {
		return PatchResult{}, fmt.Errorf("%s is a directory", requestedPath)
	}
	raw, err := fs.ReadFile(target)
	if err != nil {
		return PatchResult{}, err
	}
	lines := splitPatchLines(string(raw))
	for index, operation := range operations {
		updated, err := applyPatchOperation(lines, operation)
		if err != nil {
			return PatchResult{}, fmt.Errorf("operation %d: %w", index+1, err)
		}
		lines = updated
	}
	content := strings.Join(lines, "")
	if err := fs.WriteFile(target, []byte(content), 0o600); err != nil {
		return PatchResult{}, err
	}
	fs.notify(target, rel)
	return PatchResult{Path: filepath.ToSlash(rel), Operations: len(operations), Bytes: len(content)}, nil
}

func OriginalPatch(root string, patchText string, dryRun bool) (OriginalPatchResult, error) {
	if strings.TrimSpace(patchText) == "" {
		return OriginalPatchResult{}, errors.New("Patch.patch is required")
	}
	parsed, err := parseUnifiedPatch(patchText)
	if err != nil {
		return OriginalPatchResult{}, err
	}
	fs, err := openMutationFS(root)
	if err != nil {
		return OriginalPatchResult{}, err
	}
	defer fs.Close()
	currentFiles := make(map[string]string, len(parsed))
	resolvedSources := make(map[string]string, len(parsed))
	resolvedTargets := make(map[string]string, len(parsed))
	for _, file := range parsed {
		switch file.ChangeType {
		case "create":
			target, _, err := fs.resolve(file.NewPath)
			if err != nil {
				return OriginalPatchResult{}, err
			}
			if fs.exists(target) {
				return OriginalPatchResult{}, fmt.Errorf("Patch would create a file that already exists: %s", file.NewPath)
			}
			resolvedTargets[file.FilePath] = target
			currentFiles[file.FilePath] = ""
		case "delete", "modify":
			source, _, err := fs.resolve(file.OldPath)
			if err != nil {
				return OriginalPatchResult{}, err
			}
			raw, err := fs.ReadFile(source)
			if err != nil {
				return OriginalPatchResult{}, err
			}
			if bytes.IndexByte(raw, 0) >= 0 {
				return OriginalPatchResult{}, fmt.Errorf("Patch refuses binary file: %s", file.OldPath)
			}
			resolvedSources[file.FilePath] = source
			currentFiles[file.FilePath] = string(raw)
			if file.ChangeType == "modify" {
				resolvedTargets[file.FilePath] = source
			}
		default:
			return OriginalPatchResult{}, fmt.Errorf("unsupported patch change type: %s", file.ChangeType)
		}
	}
	applied, err := applyUnifiedPatch(parsed, currentFiles)
	if err != nil {
		return OriginalPatchResult{}, err
	}
	if !dryRun {
		for _, file := range parsed {
			switch file.ChangeType {
			case "create", "modify":
				target := resolvedTargets[file.FilePath]
				if target == "" {
					return OriginalPatchResult{}, fmt.Errorf("missing patch target for %s", file.FilePath)
				}
				if err := fs.MkdirAll(filepath.Dir(target), 0o700); err != nil {
					return OriginalPatchResult{}, err
				}
				data := []byte(applied.UpdatedFiles[file.FilePath])
				if file.ChangeType == "create" {
					err = fs.createFile(target, data)
				} else {
					err = fs.WriteFile(target, data, 0o600)
				}
				if err != nil {
					return OriginalPatchResult{}, err
				}
				fs.notify(target, file.FilePath)
			case "delete":
				source := resolvedSources[file.FilePath]
				if source == "" {
					return OriginalPatchResult{}, fmt.Errorf("missing patch source for %s", file.FilePath)
				}
				if err := fs.Remove(source); err != nil {
					return OriginalPatchResult{}, err
				}
				fs.notify(source, file.FilePath)
			}
		}
	}
	return OriginalPatchResult{
		Applied:      !dryRun,
		DryRun:       dryRun,
		Files:        applied.Files,
		DeletedFiles: applied.DeletedFiles,
		FinalDiff:    patchText,
	}, nil
}

func JSONPatch(root string, requestedPath string, operations []JSONPatchOperation) (JSONPatchResult, error) {
	if len(operations) == 0 {
		return JSONPatchResult{}, errors.New("json_patch.operations is required")
	}
	fs, err := openMutationFS(root)
	if err != nil {
		return JSONPatchResult{}, err
	}
	defer fs.Close()
	target, rel, err := fs.resolve(requestedPath)
	if err != nil {
		return JSONPatchResult{}, err
	}
	info, err := fs.Stat(target)
	if err != nil {
		return JSONPatchResult{}, err
	}
	if info.IsDir() {
		return JSONPatchResult{}, fmt.Errorf("%s is a directory", requestedPath)
	}
	raw, err := fs.ReadFile(target)
	if err != nil {
		return JSONPatchResult{}, err
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return JSONPatchResult{}, fmt.Errorf("parse JSON %s: %w", filepath.ToSlash(rel), err)
	}
	for index, operation := range operations {
		updated, err := applyJSONPatchOperation(document, operation)
		if err != nil {
			return JSONPatchResult{}, fmt.Errorf("operation %d: %w", index+1, err)
		}
		document = updated
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return JSONPatchResult{}, err
	}
	encoded = append(encoded, '\n')
	if err := fs.WriteFile(target, encoded, 0o600); err != nil {
		return JSONPatchResult{}, err
	}
	fs.notify(target, rel)
	return JSONPatchResult{Path: filepath.ToSlash(rel), Operations: len(operations), Bytes: len(encoded)}, nil
}
