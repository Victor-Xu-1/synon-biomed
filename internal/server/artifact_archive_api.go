package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/mholt/archives"
	workspace "synon-go/internal/persistence/workspace"
)

const (
	maxArtifactArchiveBytes         = 512 << 20
	maxArtifactArchiveEntryBytes    = 64 << 20
	maxArtifactArchiveNestedBytes   = 192 << 20
	maxArtifactArchiveExpandedBytes = 4 << 30
	maxArtifactArchiveEntries       = 5000
	maxArtifactArchiveDepth         = 4
)

type artifactArchiveEntry struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	ModifiedAt string `json:"modified_at,omitempty"`
	Directory  bool   `json:"directory"`
	Archive    bool   `json:"archive"`
}

type artifactArchiveListing struct {
	Filename   string                 `json:"filename"`
	Containers []string               `json:"containers"`
	Entries    []artifactArchiveEntry `json:"entries"`
}

func (s *Server) handleArtifactArchive(
	w http.ResponseWriter,
	r *http.Request,
	store *workspace.Store,
	userID string,
	segments []string,
) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeAttachmentError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if len(segments) != 4 && !(len(segments) == 5 && segments[4] == "content") {
		writeAttachmentError(w, http.StatusNotFound, "archive endpoint not found")
		return
	}
	artifactID, artifactErr := url.PathUnescape(segments[0])
	versionID, versionErr := url.PathUnescape(segments[2])
	artifactID, versionID = strings.TrimSpace(artifactID), strings.TrimSpace(versionID)
	if artifactErr != nil || versionErr != nil || artifactID == "" || versionID == "" {
		writeAttachmentError(w, http.StatusBadRequest, "invalid artifact version identity")
		return
	}
	metadata, found, err := store.GetCompatibilityVersionDownloadMetadata(r.Context(), userID, versionID)
	if err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	if !found || metadata.ArtifactID != artifactID {
		writeAttachmentError(w, http.StatusNotFound, "Artifact version unavailable")
		return
	}
	artifact, version, content, found, err := store.OpenArtifactVersionContent(versionID)
	if err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	if !found || artifact.ID != artifactID || version.ID != versionID {
		if content != nil {
			_ = content.Close()
		}
		writeAttachmentError(w, http.StatusNotFound, "Artifact version unavailable")
		return
	}
	defer content.Close()
	archiveBytes, err := readBoundedArchive(content, maxArtifactArchiveBytes)
	if err != nil {
		writeArtifactArchiveError(w, err)
		return
	}
	containers := append([]string{}, r.URL.Query()["container"]...)
	archiveFS, filename, err := openNestedArchive(r.Context(), artifact.Name, archiveBytes, containers)
	if err != nil {
		writeArtifactArchiveError(w, err)
		return
	}
	if len(segments) == 5 {
		serveArtifactArchiveEntry(w, r, archiveFS, r.URL.Query().Get("entry"))
		return
	}
	entries, err := listArtifactArchive(archiveFS)
	if err != nil {
		writeArtifactArchiveError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == http.MethodHead {
		return
	}
	_ = json.NewEncoder(w).Encode(artifactArchiveListing{Filename: filename, Containers: containers, Entries: entries})
}

func readBoundedArchive(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("archive exceeds preview size limit")
	}
	return data, nil
}

func openNestedArchive(
	ctx context.Context,
	filename string,
	data []byte,
	containers []string,
) (fs.FS, string, error) {
	if len(containers) > maxArtifactArchiveDepth {
		return nil, "", errors.New("archive nesting depth exceeded")
	}
	currentName, currentData := filename, data
	for depth := 0; ; depth++ {
		archiveFS, err := archives.FileSystem(ctx, currentName, bytes.NewReader(currentData))
		if err != nil {
			return nil, "", errors.New("unsupported or invalid archive")
		}
		if depth == len(containers) {
			return archiveFS, currentName, nil
		}
		container, err := normalizeArchiveEntryPath(containers[depth])
		if err != nil {
			return nil, "", err
		}
		file, err := archiveFS.Open(container)
		if err != nil {
			return nil, "", errors.New("nested archive entry not found")
		}
		info, statErr := file.Stat()
		if statErr != nil || info.IsDir() || info.Size() > maxArtifactArchiveNestedBytes {
			_ = file.Close()
			return nil, "", errors.New("nested archive entry is not previewable")
		}
		currentData, err = readBoundedArchive(file, maxArtifactArchiveNestedBytes)
		_ = file.Close()
		if err != nil {
			return nil, "", err
		}
		currentName = path.Base(container)
	}
}

func listArtifactArchive(archiveFS fs.FS) ([]artifactArchiveEntry, error) {
	entries := make([]artifactArchiveEntry, 0, 64)
	var expandedBytes int64
	err := fs.WalkDir(archiveFS, ".", func(entryPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entryPath == "." {
			return nil
		}
		if len(entries) >= maxArtifactArchiveEntries {
			return errors.New("archive entry limit exceeded")
		}
		normalized, err := normalizeArchiveEntryPath(entryPath)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			if info.Size() < 0 || info.Size() > maxArtifactArchiveExpandedBytes-expandedBytes {
				return errors.New("archive expanded size limit exceeded")
			}
			expandedBytes += info.Size()
		}
		modifiedAt := ""
		if !info.ModTime().IsZero() {
			modifiedAt = info.ModTime().UTC().Format(time.RFC3339)
		}
		entries = append(entries, artifactArchiveEntry{
			Path: normalized, Name: path.Base(normalized), Size: info.Size(), ModifiedAt: modifiedAt,
			Directory: entry.IsDir(), Archive: !entry.IsDir() && isArchiveFilename(normalized),
		})
		return nil
	})
	return entries, err
}

func serveArtifactArchiveEntry(w http.ResponseWriter, r *http.Request, archiveFS fs.FS, rawEntry string) {
	entryPath, err := normalizeArchiveEntryPath(rawEntry)
	if err != nil {
		writeArtifactArchiveError(w, err)
		return
	}
	file, err := archiveFS.Open(entryPath)
	if err != nil {
		writeAttachmentError(w, http.StatusNotFound, "archive entry not found")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		writeAttachmentError(w, http.StatusBadRequest, "archive entry is not a file")
		return
	}
	if info.Mode()&fs.ModeSymlink != 0 || info.Size() > maxArtifactArchiveEntryBytes {
		writeAttachmentError(w, http.StatusRequestEntityTooLarge, "archive entry is not previewable")
		return
	}
	data, err := readBoundedArchive(file, maxArtifactArchiveEntryBytes)
	if err != nil {
		writeArtifactArchiveError(w, err)
		return
	}
	contentType := mime.TypeByExtension(path.Ext(entryPath))
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": path.Base(entryPath)}))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

func normalizeArchiveEntryPath(value string) (string, error) {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	if value == "" || strings.ContainsRune(value, 0) || strings.HasPrefix(value, "/") {
		return "", errors.New("invalid archive entry path")
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("invalid archive entry path")
	}
	return cleaned, nil
}

func isArchiveFilename(filename string) bool {
	filename = strings.ToLower(filename)
	for _, suffix := range []string{
		".zip", ".tar", ".tar.gz", ".tgz", ".tar.bz2", ".tbz", ".tbz2", ".tar.xz", ".txz", ".tar.zst",
		".gz", ".br", ".bz2", ".lz4", ".lz", ".mz", ".sz", ".s2", ".xz", ".zz", ".zst", ".7z", ".rar",
	} {
		if strings.HasSuffix(filename, suffix) {
			return true
		}
	}
	return false
}

func writeArtifactArchiveError(w http.ResponseWriter, err error) {
	status := http.StatusUnprocessableEntity
	if strings.Contains(err.Error(), "limit") || strings.Contains(err.Error(), "depth") || strings.Contains(err.Error(), "size") {
		status = http.StatusRequestEntityTooLarge
	} else if strings.Contains(err.Error(), "path") || strings.Contains(err.Error(), "entry") {
		status = http.StatusBadRequest
	}
	writeAttachmentError(w, status, err.Error())
}
