package fileops

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

func List(root string, requestedPath string) (ListResult, error) {
	target, rel, err := resolveExisting(root, requestedPath)
	if err != nil {
		return ListResult{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return ListResult{}, err
	}
	if !info.IsDir() {
		return ListResult{}, fmt.Errorf("%s is not a directory", requestedPath)
	}
	items, err := os.ReadDir(target)
	if err != nil {
		return ListResult{}, err
	}
	entries := make([]Entry, 0, len(items))
	for _, item := range items {
		itemInfo, err := item.Info()
		if err != nil {
			return ListResult{}, err
		}
		entryPath := filepath.ToSlash(filepath.Join(rel, item.Name()))
		if rel == "." {
			entryPath = item.Name()
		}
		entryType := "file"
		if itemInfo.IsDir() {
			entryType = "directory"
		}
		entries = append(entries, Entry{
			Name:       item.Name(),
			Path:       entryPath,
			Type:       entryType,
			Size:       itemInfo.Size(),
			ModifiedAt: itemInfo.ModTime().UTC(),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Type != entries[j].Type {
			return entries[i].Type == "directory"
		}
		return entries[i].Name < entries[j].Name
	})
	return ListResult{Path: filepath.ToSlash(rel), Entries: entries}, nil
}

func Info(root string, requestedPath string) (InfoResult, error) {
	target, rel, err := resolveExisting(root, requestedPath)
	if err != nil {
		return InfoResult{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return InfoResult{}, err
	}
	result := InfoResult{
		Name:       info.Name(),
		Path:       filepath.ToSlash(rel),
		Type:       fileType(info),
		Size:       info.Size(),
		ModifiedAt: info.ModTime().UTC(),
		Mode:       info.Mode().String(),
	}
	if info.IsDir() {
		return result, nil
	}
	sample, err := readSample(target, 512)
	if err != nil {
		return InfoResult{}, err
	}
	hash, err := fileSHA256(target)
	if err != nil {
		return InfoResult{}, err
	}
	mimeType, encoding, binary := classifyReadContent(sample)
	result.SHA256 = hash
	result.MIME = mimeType
	result.Encoding = encoding
	result.Binary = binary
	return result, nil
}

func Read(root string, requestedPath string, limit int64, requestedEncoding string) (ReadResult, error) {
	target, rel, err := resolveExisting(root, requestedPath)
	if err != nil {
		return ReadResult{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return ReadResult{}, err
	}
	if info.IsDir() {
		return ReadResult{}, fmt.Errorf("%s is a directory", requestedPath)
	}
	if limit <= 0 {
		limit = defaultReadLimit
	}
	file, err := os.Open(target)
	if err != nil {
		return ReadResult{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return ReadResult{}, err
	}
	truncated := int64(len(data)) > limit
	if truncated {
		data = data[:limit]
	}
	hash, err := fileSHA256(target)
	if err != nil {
		return ReadResult{}, err
	}
	mimeType, encoding, binary := classifyReadContent(data)
	contentEncoding := normalizeContentEncoding(requestedEncoding)
	content, err := encodeContent(data, contentEncoding)
	if err != nil {
		return ReadResult{}, err
	}
	return ReadResult{
		Path:            filepath.ToSlash(rel),
		Content:         content,
		ContentEncoding: contentEncoding,
		Bytes:           len(data),
		Truncated:       truncated,
		Size:            info.Size(),
		ModifiedAt:      info.ModTime().UTC(),
		SHA256:          hash,
		MIME:            mimeType,
		Encoding:        encoding,
		Binary:          binary,
	}, nil
}

func OriginalRead(root string, requestedPath string, offset int64, limit int64) (OriginalReadResult, error) {
	target, rel, err := resolveExisting(root, requestedPath)
	if err != nil {
		return OriginalReadResult{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return OriginalReadResult{}, err
	}
	if info.IsDir() {
		return OriginalReadResult{}, fmt.Errorf("%s is a directory", requestedPath)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return OriginalReadResult{}, err
	}
	filePath := filepath.ToSlash(rel)
	if mediaType := originalReadMediaType(target); mediaType != "" {
		return OriginalReadResult{
			Type: "image",
			File: OriginalReadFile{
				FilePath:     filePath,
				Base64:       base64.StdEncoding.EncodeToString(data),
				Type:         mediaType,
				OriginalSize: info.Size(),
			},
		}, nil
	}
	if strings.EqualFold(filepath.Ext(target), ".pdf") {
		return OriginalReadResult{
			Type: "pdf",
			File: OriginalReadFile{
				FilePath:     filePath,
				Base64:       base64.StdEncoding.EncodeToString(data),
				OriginalSize: info.Size(),
			},
		}, nil
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return OriginalReadResult{}, fmt.Errorf("Read refuses binary file: %s", filePath)
	}
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := splitReadLines(content)
	totalLines := len(lines)
	startLine := int(offset)
	if startLine <= 0 {
		startLine = 1
	}
	startIndex := startLine - 1
	if startIndex > totalLines {
		startIndex = totalLines
	}
	endIndex := totalLines
	if limit > 0 && startIndex+int(limit) < endIndex {
		endIndex = startIndex + int(limit)
	}
	selected := strings.Join(lines[startIndex:endIndex], "\n")
	if strings.HasSuffix(content, "\n") && endIndex == totalLines && selected != "" {
		selected += "\n"
	}
	return OriginalReadResult{
		Type: "text",
		File: OriginalReadFile{
			FilePath:   filePath,
			Content:    selected,
			NumLines:   endIndex - startIndex,
			StartLine:  startLine,
			TotalLines: totalLines,
		},
	}, nil
}

func ReadBatch(root string, requestedPaths []string, maxBytesPerFile int64, maxFiles int64) (ReadBatchResult, error) {
	if len(requestedPaths) == 0 {
		return ReadBatchResult{}, errors.New("file_read_batch.file_paths is required")
	}
	if maxFiles <= 0 {
		maxFiles = defaultReadBatchMaxFiles
	}
	if maxFiles > maxReadBatchFiles {
		maxFiles = maxReadBatchFiles
	}
	if maxBytesPerFile <= 0 {
		maxBytesPerFile = defaultReadBatchMaxBytes
	}
	if maxBytesPerFile > maxReadBatchBytes {
		maxBytesPerFile = maxReadBatchBytes
	}
	result := ReadBatchResult{
		Files:  make([]ReadBatchFile, 0, minInt(len(requestedPaths), int(maxFiles))),
		Errors: make([]ReadBatchError, 0),
	}
	if int64(len(requestedPaths)) > maxFiles {
		result.Errors = append(result.Errors, ReadBatchError{
			FilePath: "*",
			Reason:   "too_many_files",
			Message:  fmt.Sprintf("file_read_batch accepts at most %d files per call.", maxFiles),
		})
	}
	for index, requestedPath := range requestedPaths {
		if int64(index) >= maxFiles {
			break
		}
		file, batchErr := readBatchOne(root, requestedPath, maxBytesPerFile)
		if batchErr != nil {
			result.Errors = append(result.Errors, *batchErr)
			continue
		}
		result.Files = append(result.Files, file)
	}
	return result, nil
}

func readBatchOne(root string, requestedPath string, maxBytesPerFile int64) (ReadBatchFile, *ReadBatchError) {
	target, rel, err := resolveExisting(root, requestedPath)
	filePath := filepath.ToSlash(requestedPath)
	if rel != "" {
		filePath = filepath.ToSlash(rel)
	}
	if err != nil {
		return ReadBatchFile{}, readBatchError(filePath, err)
	}
	info, err := os.Stat(target)
	if err != nil {
		return ReadBatchFile{}, readBatchError(filePath, err)
	}
	if info.IsDir() {
		return ReadBatchFile{}, &ReadBatchError{
			FilePath: filePath,
			Reason:   "read_error",
			Message:  fmt.Sprintf("cannot batch-read directory: %s", filePath),
		}
	}
	if hasBinaryExtension(target) {
		return ReadBatchFile{}, &ReadBatchError{
			FilePath: filePath,
			Reason:   "binary",
			Message:  fmt.Sprintf("Refusing to batch-read binary file: %s", filePath),
		}
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		return ReadBatchFile{}, readBatchError(filePath, err)
	}
	if looksBinaryForBatch(raw) {
		return ReadBatchFile{}, &ReadBatchError{
			FilePath: filePath,
			Reason:   "binary",
			Message:  fmt.Sprintf("Refusing to batch-read binary file: %s", filePath),
		}
	}
	encoding := detectBatchEncoding(raw)
	truncated := int64(len(raw)) > maxBytesPerFile
	returned := raw
	if truncated {
		returned = raw[:maxBytesPerFile]
	}
	fullContent := decodeBatchText(raw, encoding)
	content := decodeBatchText(returned, encoding)
	return ReadBatchFile{
		FilePath:      filePath,
		Content:       content,
		SizeBytes:     info.Size(),
		TotalLines:    countLogicalLines(fullContent),
		ReturnedLines: countLogicalLines(content),
		Encoding:      encoding,
		Truncated:     truncated,
	}, nil
}

func readBatchError(filePath string, err error) *ReadBatchError {
	reason := "read_error"
	if errors.Is(err, os.ErrNotExist) {
		reason = "not_found"
	}
	return &ReadBatchError{FilePath: filePath, Reason: reason, Message: err.Error()}
}

func splitReadLines(content string) []string {
	if content == "" {
		return []string{}
	}
	trimmed := strings.TrimSuffix(content, "\n")
	if trimmed == "" {
		return []string{""}
	}
	return strings.Split(trimmed, "\n")
}

func originalReadMediaType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return ""
	}
}

func readSample(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, limit))
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func classifyReadContent(data []byte) (string, string, bool) {
	mimeType := "application/octet-stream"
	if len(data) > 0 {
		mimeType = http.DetectContentType(data)
	}
	binary := bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data)
	encoding := "utf-8"
	if binary {
		encoding = "binary"
	}
	return mimeType, encoding, binary
}

func normalizeContentEncoding(encoding string) string {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "utf8", "utf-8":
		return "utf8"
	case "base64":
		return "base64"
	default:
		return strings.ToLower(strings.TrimSpace(encoding))
	}
}

func encodeContent(data []byte, encoding string) (string, error) {
	switch encoding {
	case "utf8":
		return string(data), nil
	case "base64":
		return base64.StdEncoding.EncodeToString(data), nil
	default:
		return "", fmt.Errorf("unsupported file_read encoding: %s", encoding)
	}
}

func decodeContent(content string, encoding string) ([]byte, error) {
	switch encoding {
	case "utf8":
		return []byte(content), nil
	case "base64":
		data, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			return nil, fmt.Errorf("file_write invalid base64 content: %w", err)
		}
		return data, nil
	default:
		return nil, fmt.Errorf("unsupported file_write encoding: %s", encoding)
	}
}

func hasBinaryExtension(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".pdf", ".zip", ".gz", ".tgz", ".tar", ".7z", ".rar", ".exe", ".dll", ".so", ".dylib", ".bin", ".wasm", ".sqlite", ".db", ".mp3", ".mp4", ".mov", ".avi", ".woff", ".woff2", ".ttf", ".otf":
		return true
	default:
		return false
	}
}

func looksBinaryForBatch(buffer []byte) bool {
	sample := buffer
	if len(sample) > 4096 {
		sample = sample[:4096]
	}
	return bytes.IndexByte(sample, 0) >= 0 && detectBatchEncoding(buffer) != "utf16le"
}

func detectBatchEncoding(buffer []byte) string {
	if len(buffer) >= 2 && buffer[0] == 0xff && buffer[1] == 0xfe {
		return "utf16le"
	}
	return "utf8"
}

func decodeBatchText(buffer []byte, encoding string) string {
	switch encoding {
	case "utf16le":
		start := 0
		if len(buffer) >= 2 && buffer[0] == 0xff && buffer[1] == 0xfe {
			start = 2
		}
		units := make([]uint16, 0, (len(buffer)-start)/2)
		for index := start; index+1 < len(buffer); index += 2 {
			units = append(units, uint16(buffer[index])|uint16(buffer[index+1])<<8)
		}
		return string(utf16.Decode(units))
	default:
		return string(buffer)
	}
}

func countLogicalLines(content string) int {
	if content == "" {
		return 0
	}
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	if strings.HasSuffix(normalized, "\n") {
		return len(lines) - 1
	}
	return len(lines)
}

func minInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

func fileType(info os.FileInfo) string {
	if info.IsDir() {
		return "directory"
	}
	return "file"
}
