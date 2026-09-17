package fileops

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func newGlobMatcher(pattern string) (globMatcher, error) {
	normalized := normalizeSlashPath(pattern)
	full, err := regexp.Compile("^" + globPatternRegexp(normalized) + "$")
	if err != nil {
		return globMatcher{}, err
	}
	var basename *regexp.Regexp
	if !strings.Contains(normalized, "/") {
		basename, err = regexp.Compile("^" + globPatternRegexp(normalized) + "$")
		if err != nil {
			return globMatcher{}, err
		}
	}
	return globMatcher{full: full, basename: basename}, nil
}

func (matcher globMatcher) match(path string) bool {
	path = normalizeSlashPath(path)
	if matcher.full.MatchString(path) {
		return true
	}
	if matcher.basename != nil && matcher.basename.MatchString(filepath.Base(path)) {
		return true
	}
	return false
}

func globPatternRegexp(pattern string) string {
	var builder strings.Builder
	for index := 0; index < len(pattern); {
		switch pattern[index] {
		case '*':
			if strings.HasPrefix(pattern[index:], "**/") {
				builder.WriteString("(?:.*/)?")
				index += 3
			} else if strings.HasPrefix(pattern[index:], "**") {
				builder.WriteString(".*")
				index += 2
			} else {
				builder.WriteString("[^/]*")
				index++
			}
		case '?':
			builder.WriteString("[^/]")
			index++
		case '{':
			end := strings.IndexByte(pattern[index+1:], '}')
			if end < 0 {
				builder.WriteString(regexp.QuoteMeta(pattern[index : index+1]))
				index++
				continue
			}
			body := pattern[index+1 : index+1+end]
			parts := strings.Split(body, ",")
			builder.WriteByte('(')
			for partIndex, part := range parts {
				if partIndex > 0 {
					builder.WriteByte('|')
				}
				builder.WriteString(regexp.QuoteMeta(part))
			}
			builder.WriteByte(')')
			index += end + 2
		default:
			builder.WriteString(regexp.QuoteMeta(pattern[index : index+1]))
			index++
		}
	}
	return builder.String()
}

func normalizeSlashPath(path string) string {
	path = filepath.ToSlash(strings.TrimSpace(path))
	return strings.TrimPrefix(path, "./")
}

func normalizeGrepOptions(options GrepOptions) GrepOptions {
	options.OutputMode = strings.TrimSpace(options.OutputMode)
	if options.OutputMode == "" {
		options.OutputMode = "files_with_matches"
	}
	if options.Context > 0 {
		options.Before = options.Context
		options.After = options.Context
	}
	if options.Before < 0 {
		options.Before = 0
	}
	if options.After < 0 {
		options.After = 0
	}
	if options.Before > 20 {
		options.Before = 20
	}
	if options.After > 20 {
		options.After = 20
	}
	if options.Offset < 0 {
		options.Offset = 0
	}
	options.Glob = strings.TrimSpace(options.Glob)
	options.Type = strings.TrimSpace(options.Type)
	return options
}

func collectGrepFiles(root string, requestedPath string, options GrepOptions) ([]grepFile, error) {
	target, _, err := resolveExisting(root, requestedPath)
	if err != nil {
		return nil, err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	var matcher globMatcher
	if options.Glob != "" {
		matcher, err = newGlobMatcher(options.Glob)
		if err != nil {
			return nil, err
		}
	}
	files := make([]grepFile, 0)
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
		rel, err := filepath.Rel(rootAbs, evaluated)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if options.Glob != "" && !matcher.match(rel) && !matcher.match(filepath.Base(rel)) {
			return nil
		}
		if !grepTypeMatches(evaluated, options.Type) || !looksTextFile(evaluated) {
			return nil
		}
		files = append(files, grepFile{AbsPath: evaluated, RelPath: rel})
		return nil
	}
	info, err := os.Stat(target)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		if err := addFile(target); err != nil {
			return nil, err
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
			return nil, err
		}
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].RelPath < files[j].RelPath
	})
	return files, nil
}

func grepFilesWithMatches(files []grepFile, pattern *regexp.Regexp, options GrepOptions) (GrepResult, error) {
	filenames := make([]string, 0)
	totalMatches := 0
	for _, file := range files {
		lines, raw, err := readGrepLines(file.AbsPath)
		if err != nil {
			return GrepResult{}, err
		}
		matches := grepFileMatchCount(lines, raw, pattern, options.Multiline)
		if matches == 0 {
			continue
		}
		totalMatches += matches
		filenames = append(filenames, file.RelPath)
	}
	limited, appliedLimit, appliedOffset := applyStringWindow(filenames, options)
	return GrepResult{
		Mode:          "files_with_matches",
		NumFiles:      len(limited),
		Filenames:     limited,
		NumMatches:    totalMatches,
		AppliedLimit:  appliedLimit,
		AppliedOffset: appliedOffset,
	}, nil
}

func grepContent(files []grepFile, pattern *regexp.Regexp, options GrepOptions) (GrepResult, error) {
	rows := make([]grepLine, 0)
	matchedFiles := make(map[string]struct{})
	totalMatches := 0
	for _, file := range files {
		lines, raw, err := readGrepLines(file.AbsPath)
		if err != nil {
			return GrepResult{}, err
		}
		matches := grepMatchingLineIndexes(lines, raw, pattern, options.Multiline)
		if len(matches) == 0 {
			continue
		}
		matchedFiles[file.RelPath] = struct{}{}
		totalMatches += len(matches)
		for _, lineIndex := range grepContextIndexes(matches, len(lines), int(options.Before), int(options.After)) {
			rows = append(rows, grepLine{File: file.RelPath, Line: lineIndex + 1, Text: lines[lineIndex]})
		}
	}
	limited, appliedLimit, appliedOffset := applyLineWindow(rows, options)
	contentLines := make([]string, 0, len(limited))
	for _, row := range limited {
		if options.ShowLineNumbers {
			contentLines = append(contentLines, fmt.Sprintf("%s:%d:%s", row.File, row.Line, row.Text))
		} else {
			contentLines = append(contentLines, fmt.Sprintf("%s:%s", row.File, row.Text))
		}
	}
	filenames := sortedMapKeys(matchedFiles)
	return GrepResult{
		Mode:          "content",
		NumFiles:      len(filenames),
		Filenames:     filenames,
		Content:       strings.Join(contentLines, "\n"),
		NumLines:      len(limited),
		NumMatches:    totalMatches,
		AppliedLimit:  appliedLimit,
		AppliedOffset: appliedOffset,
	}, nil
}

func grepCount(files []grepFile, pattern *regexp.Regexp, options GrepOptions) (GrepResult, error) {
	rows := make([]string, 0)
	filenames := make([]string, 0)
	totalMatches := 0
	for _, file := range files {
		lines, raw, err := readGrepLines(file.AbsPath)
		if err != nil {
			return GrepResult{}, err
		}
		count := grepFileMatchCount(lines, raw, pattern, options.Multiline)
		if count == 0 {
			continue
		}
		totalMatches += count
		filenames = append(filenames, file.RelPath)
		rows = append(rows, fmt.Sprintf("%s:%d", file.RelPath, count))
	}
	limitedRows, appliedLimit, appliedOffset := applyStringWindow(rows, options)
	return GrepResult{
		Mode:          "count",
		NumFiles:      len(filenames),
		Filenames:     filenames,
		Content:       strings.Join(limitedRows, "\n"),
		NumLines:      len(limitedRows),
		NumMatches:    totalMatches,
		AppliedLimit:  appliedLimit,
		AppliedOffset: appliedOffset,
	}, nil
}

func readGrepLines(path string) ([]string, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	content := strings.TrimSuffix(string(raw), "\n")
	if content == "" {
		return []string{}, raw, nil
	}
	return strings.Split(content, "\n"), raw, nil
}

func grepMatchingLineIndexes(lines []string, raw []byte, pattern *regexp.Regexp, multiline bool) []int {
	if multiline && pattern.Match(raw) {
		return indexesForRegexpLines(lines, pattern)
	}
	matches := make([]int, 0)
	for index, line := range lines {
		if pattern.MatchString(line) {
			matches = append(matches, index)
		}
	}
	return matches
}

func indexesForRegexpLines(lines []string, pattern *regexp.Regexp) []int {
	matches := make([]int, 0)
	for index, line := range lines {
		if pattern.MatchString(line) {
			matches = append(matches, index)
		}
	}
	if len(matches) == 0 && len(lines) > 0 {
		return []int{0}
	}
	return matches
}

func grepFileMatchCount(lines []string, raw []byte, pattern *regexp.Regexp, multiline bool) int {
	if multiline {
		return len(pattern.FindAll(raw, -1))
	}
	count := 0
	for _, line := range lines {
		count += len(pattern.FindAllString(line, -1))
	}
	return count
}

func grepContextIndexes(matches []int, totalLines int, before int, after int) []int {
	seen := make(map[int]struct{}, len(matches))
	indexes := make([]int, 0, len(matches))
	for _, match := range matches {
		start := match - before
		if start < 0 {
			start = 0
		}
		end := match + after
		if end >= totalLines {
			end = totalLines - 1
		}
		for index := start; index <= end; index++ {
			if _, ok := seen[index]; ok {
				continue
			}
			seen[index] = struct{}{}
			indexes = append(indexes, index)
		}
	}
	sort.Ints(indexes)
	return indexes
}

func applyStringWindow(values []string, options GrepOptions) ([]string, int, int) {
	offset := int(options.Offset)
	if offset > len(values) {
		offset = len(values)
	}
	limited := values[offset:]
	appliedOffset := 0
	if offset > 0 {
		appliedOffset = offset
	}
	limit := int(options.HeadLimit)
	appliedLimit := 0
	if limit > 0 && len(limited) > limit {
		limited = limited[:limit]
		appliedLimit = limit
	}
	return limited, appliedLimit, appliedOffset
}

func applyLineWindow(values []grepLine, options GrepOptions) ([]grepLine, int, int) {
	offset := int(options.Offset)
	if offset > len(values) {
		offset = len(values)
	}
	limited := values[offset:]
	appliedOffset := 0
	if offset > 0 {
		appliedOffset = offset
	}
	limit := int(options.HeadLimit)
	appliedLimit := 0
	if limit > 0 && len(limited) > limit {
		limited = limited[:limit]
		appliedLimit = limit
	}
	return limited, appliedLimit, appliedOffset
}

func grepTypeMatches(path string, requestedType string) bool {
	requestedType = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(requestedType)), ".")
	if requestedType == "" {
		return true
	}
	extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	for _, allowed := range grepTypeExtensions(requestedType) {
		if extension == strings.TrimPrefix(allowed, ".") {
			return true
		}
	}
	return false
}

func grepTypeExtensions(requestedType string) []string {
	switch requestedType {
	case "go":
		return []string{"go"}
	case "js", "javascript":
		return []string{"js", "jsx", "mjs", "cjs"}
	case "ts", "typescript":
		return []string{"ts", "tsx"}
	case "py", "python":
		return []string{"py"}
	case "rs", "rust":
		return []string{"rs"}
	case "java":
		return []string{"java"}
	case "json":
		return []string{"json"}
	case "md", "markdown":
		return []string{"md", "markdown"}
	case "txt", "text":
		return []string{"txt", "text"}
	case "yaml", "yml":
		return []string{"yaml", "yml"}
	case "html":
		return []string{"html", "htm"}
	case "css":
		return []string{"css"}
	case "sh", "shell":
		return []string{"sh", "bash", "zsh"}
	default:
		return []string{requestedType}
	}
}

func looksTextFile(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	buffer := make([]byte, 8192)
	n, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return false
	}
	return !bytes.Contains(buffer[:n], []byte{0})
}

func sortedMapKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
