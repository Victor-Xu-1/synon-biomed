package fileops

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"synon-go/internal/tools/fileevents"
	"time"
)

func Search(root string, requestedPath string, query string, limit int64) (SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return SearchResult{}, errors.New("file_search.query is required")
	}
	target, _, err := resolveExisting(root, requestedPath)
	if err != nil {
		return SearchResult{}, err
	}
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	scanLimit := limit + 1
	if scanLimit <= limit {
		scanLimit = limit
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return SearchResult{}, err
	}
	matches := make([]SearchMatch, 0)
	addFile := func(path string) error {
		if int64(len(matches)) >= scanLimit {
			return nil
		}
		evaluated, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil
		}
		if err := ensureInside(rootAbs, evaluated); err != nil {
			return nil
		}
		info, err := os.Stat(evaluated)
		if err != nil || info.IsDir() {
			return nil
		}
		file, err := os.Open(evaluated)
		if err != nil {
			return err
		}
		defer file.Close()
		rel, err := filepath.Rel(rootAbs, evaluated)
		if err != nil {
			return err
		}
		scanner := bufio.NewScanner(file)
		line := 0
		for scanner.Scan() {
			line++
			text := scanner.Text()
			if strings.Contains(text, query) {
				matches = append(matches, SearchMatch{Path: filepath.ToSlash(rel), Line: line, Text: text})
				if int64(len(matches)) >= scanLimit {
					break
				}
			}
		}
		return scanner.Err()
	}
	info, err := os.Stat(target)
	if err != nil {
		return SearchResult{}, err
	}
	if !info.IsDir() {
		if err := addFile(target); err != nil {
			return SearchResult{}, err
		}
		truncated := int64(len(matches)) > limit
		if truncated {
			matches = matches[:limit]
		}
		return SearchResult{Query: query, Matches: matches, AppliedLimit: limit, Truncated: truncated}, nil
	}
	err = filepath.WalkDir(target, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || int64(len(matches)) >= scanLimit {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		return addFile(path)
	})
	if err != nil {
		return SearchResult{}, err
	}
	truncated := int64(len(matches)) > limit
	if truncated {
		matches = matches[:limit]
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Path != matches[j].Path {
			return matches[i].Path < matches[j].Path
		}
		return matches[i].Line < matches[j].Line
	})
	return SearchResult{Query: query, Matches: matches, AppliedLimit: limit, Truncated: truncated}, nil
}

func Glob(root string, requestedPath string, pattern string, limit int64) (GlobResult, error) {
	started := time.Now()
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return GlobResult{}, errors.New("Glob.pattern is required")
	}
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	target, _, err := resolveExisting(root, requestedPath)
	if err != nil {
		return GlobResult{}, err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return GlobResult{}, err
	}
	matcher, err := newGlobMatcher(pattern)
	if err != nil {
		return GlobResult{}, err
	}
	filenames := make([]string, 0)
	truncated := false
	addFile := func(path string) error {
		evaluated, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil
		}
		if err := ensureInside(rootAbs, evaluated); err != nil {
			return nil
		}
		info, err := os.Stat(evaluated)
		if err != nil || info.IsDir() {
			return nil
		}
		rootRel, err := filepath.Rel(rootAbs, evaluated)
		if err != nil {
			return err
		}
		targetRel, err := filepath.Rel(target, evaluated)
		if err != nil {
			targetRel = rootRel
		}
		rootRel = filepath.ToSlash(rootRel)
		targetRel = filepath.ToSlash(targetRel)
		if !matcher.match(rootRel) && !matcher.match(targetRel) {
			return nil
		}
		if int64(len(filenames)) >= limit {
			truncated = true
			return nil
		}
		filenames = append(filenames, rootRel)
		return nil
	}
	info, err := os.Stat(target)
	if err != nil {
		return GlobResult{}, err
	}
	if !info.IsDir() {
		if err := addFile(target); err != nil {
			return GlobResult{}, err
		}
	} else {
		err = filepath.WalkDir(target, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if entry.IsDir() {
				if shouldSkipCodeIndexDir(entry.Name()) && path != target {
					return filepath.SkipDir
				}
				return nil
			}
			return addFile(path)
		})
		if err != nil {
			return GlobResult{}, err
		}
	}
	sort.Strings(filenames)
	return GlobResult{
		DurationMs: time.Since(started).Milliseconds(),
		NumFiles:   len(filenames),
		Filenames:  filenames,
		Truncated:  truncated,
	}, nil
}

func Grep(root string, requestedPath string, pattern string, options GrepOptions) (GrepResult, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return GrepResult{}, errors.New("Grep.pattern is required")
	}
	options = normalizeGrepOptions(options)
	expression := pattern
	if options.CaseInsensitive {
		expression = "(?i)" + expression
	}
	compiled, err := regexp.Compile(expression)
	if err != nil {
		return GrepResult{}, fmt.Errorf("compile grep pattern: %w", err)
	}
	files, err := collectGrepFiles(root, requestedPath, options)
	if err != nil {
		return GrepResult{}, err
	}
	switch options.OutputMode {
	case "files_with_matches":
		return grepFilesWithMatches(files, compiled, options)
	case "content":
		return grepContent(files, compiled, options)
	case "count":
		return grepCount(files, compiled, options)
	default:
		return GrepResult{}, fmt.Errorf("unsupported grep output_mode: %s", options.OutputMode)
	}
}

func Replace(root string, requestedPath string, oldText string, newText string) (ReplaceResult, error) {
	if oldText == "" {
		return ReplaceResult{}, errors.New("file_replace.old is required")
	}
	target, rel, err := resolveExisting(root, requestedPath)
	if err != nil {
		return ReplaceResult{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return ReplaceResult{}, err
	}
	if info.IsDir() {
		return ReplaceResult{}, fmt.Errorf("%s is a directory", requestedPath)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		return ReplaceResult{}, err
	}
	content := string(raw)
	replacements := strings.Count(content, oldText)
	if replacements == 0 {
		return ReplaceResult{Path: filepath.ToSlash(rel), Replacements: 0, Bytes: len(raw)}, nil
	}
	updated := strings.ReplaceAll(content, oldText, newText)
	if err := os.WriteFile(target, []byte(updated), 0o600); err != nil {
		return ReplaceResult{}, err
	}
	fileevents.NotifyChanged(target)
	return ReplaceResult{Path: filepath.ToSlash(rel), Replacements: replacements, Bytes: len(updated)}, nil
}

func Patch(root string, requestedPath string, operations []PatchOperation) (PatchResult, error) {
	if len(operations) == 0 {
		return PatchResult{}, errors.New("file_patch.operations is required")
	}
	target, rel, err := resolveExisting(root, requestedPath)
	if err != nil {
		return PatchResult{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return PatchResult{}, err
	}
	if info.IsDir() {
		return PatchResult{}, fmt.Errorf("%s is a directory", requestedPath)
	}
	raw, err := os.ReadFile(target)
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
	if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
		return PatchResult{}, err
	}
	fileevents.NotifyChanged(target)
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
	currentFiles := make(map[string]string, len(parsed))
	resolvedSources := make(map[string]string, len(parsed))
	resolvedTargets := make(map[string]string, len(parsed))
	for _, file := range parsed {
		switch file.ChangeType {
		case "create":
			target, _, err := resolveForWrite(root, file.NewPath)
			if err != nil {
				return OriginalPatchResult{}, err
			}
			if pathExists(target) {
				return OriginalPatchResult{}, fmt.Errorf("Patch would create a file that already exists: %s", file.NewPath)
			}
			resolvedTargets[file.FilePath] = target
			currentFiles[file.FilePath] = ""
		case "delete", "modify":
			source, _, err := resolveExisting(root, file.OldPath)
			if err != nil {
				return OriginalPatchResult{}, err
			}
			raw, err := os.ReadFile(source)
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
				if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
					return OriginalPatchResult{}, err
				}
				if err := os.WriteFile(target, []byte(applied.UpdatedFiles[file.FilePath]), 0o600); err != nil {
					return OriginalPatchResult{}, err
				}
				fileevents.NotifyChanged(target)
			case "delete":
				source := resolvedSources[file.FilePath]
				if source == "" {
					return OriginalPatchResult{}, fmt.Errorf("missing patch source for %s", file.FilePath)
				}
				if err := os.Remove(source); err != nil {
					return OriginalPatchResult{}, err
				}
				fileevents.NotifyChanged(source)
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
	target, rel, err := resolveExisting(root, requestedPath)
	if err != nil {
		return JSONPatchResult{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return JSONPatchResult{}, err
	}
	if info.IsDir() {
		return JSONPatchResult{}, fmt.Errorf("%s is a directory", requestedPath)
	}
	raw, err := os.ReadFile(target)
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
	if err := os.WriteFile(target, encoded, 0o600); err != nil {
		return JSONPatchResult{}, err
	}
	fileevents.NotifyChanged(target)
	return JSONPatchResult{Path: filepath.ToSlash(rel), Operations: len(operations), Bytes: len(encoded)}, nil
}

func CodeIndex(root string, requestedPath string, options CodeIndexOptions) (CodeIndexResult, error) {
	target, rel, err := resolveExisting(root, requestedPath)
	if err != nil {
		return CodeIndexResult{}, err
	}
	options = normalizeCodeIndexOptions(options)
	if options.Limit <= 0 {
		options.Limit = 200
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return CodeIndexResult{}, err
	}
	files := make([]CodeFile, 0)
	totalSymbols := int64(0)
	addFile := func(path string) error {
		if int64(len(files)) >= options.Limit || !isCodeIndexFile(path, options.Extensions) || codeIndexSymbolLimitReached(totalSymbols, options.SymbolLimit) {
			return nil
		}
		evaluated, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil
		}
		if err := ensureInside(rootAbs, evaluated); err != nil {
			return nil
		}
		info, err := os.Stat(evaluated)
		if err != nil || info.IsDir() {
			return nil
		}
		symbols, err := indexSymbols(evaluated)
		if err != nil {
			return nil
		}
		symbols = filterCodeSymbols(symbols, options, &totalSymbols)
		if len(symbols) == 0 {
			return nil
		}
		fileRel, err := filepath.Rel(rootAbs, evaluated)
		if err != nil {
			return err
		}
		files = append(files, CodeFile{Path: filepath.ToSlash(fileRel), Symbols: symbols})
		return nil
	}
	info, err := os.Stat(target)
	if err != nil {
		return CodeIndexResult{}, err
	}
	if !info.IsDir() {
		if err := addFile(target); err != nil {
			return CodeIndexResult{}, err
		}
		return CodeIndexResult{Path: filepath.ToSlash(rel), Files: files}, nil
	}
	err = filepath.WalkDir(target, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || int64(len(files)) >= options.Limit || codeIndexSymbolLimitReached(totalSymbols, options.SymbolLimit) {
			return nil
		}
		if entry.IsDir() {
			if shouldSkipCodeIndexDir(entry.Name()) && path != target {
				return filepath.SkipDir
			}
			return nil
		}
		return addFile(path)
	})
	if err != nil {
		return CodeIndexResult{}, err
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
	return CodeIndexResult{Path: filepath.ToSlash(rel), Files: files}, nil
}

func CodeReferences(root string, requestedPath string, symbol string, options CodeReferencesOptions) (CodeReferencesResult, error) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return CodeReferencesResult{}, errors.New("code_references.symbol is required")
	}
	target, rel, err := resolveExisting(root, requestedPath)
	if err != nil {
		return CodeReferencesResult{}, err
	}
	options = normalizeCodeReferencesOptions(options)
	if options.Limit <= 0 {
		options.Limit = 100
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return CodeReferencesResult{}, err
	}
	pattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(symbol) + `\b`)
	references := make([]CodeReference, 0)
	truncated := false
	addFile := func(path string) error {
		if int64(len(references)) >= options.Limit || !isCodeIndexFile(path, options.Extensions) {
			return nil
		}
		evaluated, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil
		}
		if err := ensureInside(rootAbs, evaluated); err != nil {
			return nil
		}
		info, err := os.Stat(evaluated)
		if err != nil || info.IsDir() {
			return nil
		}
		file, err := os.Open(evaluated)
		if err != nil {
			return err
		}
		defer file.Close()
		fileRel, err := filepath.Rel(rootAbs, evaluated)
		if err != nil {
			return err
		}
		lines := make([]string, 0)
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			return err
		}
		contextLines := int(options.ContextLines)
		for index, text := range lines {
			if !pattern.MatchString(text) {
				continue
			}
			if int64(len(references)) >= options.Limit {
				truncated = true
				break
			}
			before, after := codeReferenceContext(lines, index, contextLines)
			references = append(references, CodeReference{
				Path:   filepath.ToSlash(fileRel),
				Line:   index + 1,
				Text:   text,
				Before: before,
				After:  after,
			})
		}
		return nil
	}
	info, err := os.Stat(target)
	if err != nil {
		return CodeReferencesResult{}, err
	}
	if !info.IsDir() {
		if err := addFile(target); err != nil {
			return CodeReferencesResult{}, err
		}
		return CodeReferencesResult{Path: filepath.ToSlash(rel), Symbol: symbol, References: references, Truncated: truncated}, nil
	}
	err = filepath.WalkDir(target, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || int64(len(references)) >= options.Limit {
			if int64(len(references)) >= options.Limit {
				truncated = true
			}
			return nil
		}
		if entry.IsDir() {
			if shouldSkipCodeIndexDir(entry.Name()) && path != target {
				return filepath.SkipDir
			}
			return nil
		}
		return addFile(path)
	})
	if err != nil {
		return CodeReferencesResult{}, err
	}
	sort.Slice(references, func(i, j int) bool {
		if references[i].Path != references[j].Path {
			return references[i].Path < references[j].Path
		}
		return references[i].Line < references[j].Line
	})
	return CodeReferencesResult{Path: filepath.ToSlash(rel), Symbol: symbol, References: references, Truncated: truncated}, nil
}
