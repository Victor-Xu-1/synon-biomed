package server

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) writeArtifactZIP(
	w http.ResponseWriter,
	r *http.Request,
	store *workspace.Store,
	userID string,
	artifactIDs []string,
	includeMetadata bool,
	filename string,
) error {
	if err := validateArtifactIDs(store, userID, artifactIDs); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
		"filename": safeArchiveFilename(filename, "artifacts.zip"),
	}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	archive := zip.NewWriter(w)
	used := map[string]int{}
	for _, artifactID := range artifactIDs {
		artifact, version, content, found, err := store.OpenCurrentArtifactContent(artifactID)
		if err != nil {
			_ = archive.Close()
			return err
		}
		if !found {
			_ = archive.Close()
			return fmt.Errorf("artifact %q has no current version", artifactID)
		}
		name := uniqueArchiveFilename(safeArchiveFilename(artifact.Name, artifact.ID), used)
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetModTime(version.CreatedAt)
		entry, err := archive.CreateHeader(header)
		if err != nil {
			_ = content.Close()
			_ = archive.Close()
			return fmt.Errorf("create artifact ZIP entry: %w", err)
		}
		if _, err := io.Copy(entry, content); err != nil {
			_ = content.Close()
			_ = archive.Close()
			return fmt.Errorf("write artifact ZIP entry: %w", err)
		}
		if err := content.Close(); err != nil {
			_ = archive.Close()
			return fmt.Errorf("close artifact content: %w", err)
		}
		if includeMetadata {
			metadataName := safeArchiveFilename(name+".metadata.json", artifact.ID+".metadata.json")
			if err := writeZIPJSON(archive, metadataName, artifactDownloadMetadata(artifact, version), version.CreatedAt); err != nil {
				_ = archive.Close()
				return err
			}
		}
	}
	if err := archive.Close(); err != nil {
		return fmt.Errorf("finalize artifact ZIP: %w", err)
	}
	return nil
}

func (s *Server) handleScriptBundle(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID, rawVersionID string) {
	if r.Method != http.MethodGet {
		writeAttachmentError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	versionID, err := url.PathUnescape(rawVersionID)
	if err != nil || strings.TrimSpace(versionID) == "" {
		writeAttachmentError(w, http.StatusBadRequest, "invalid artifact version id")
		return
	}
	artifact, version, found, err := store.GetArtifactVersionMetadata(versionID)
	if err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	if !found {
		writeV11Detail(w, http.StatusNotFound, "Version "+versionID+" not found")
		return
	}
	owned, err := store.ProjectOwnedBy(artifact.ProjectID, userID)
	if err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	if !owned {
		writeV11Detail(w, http.StatusNotFound, "Version "+versionID+" not found")
		return
	}
	lineage, found, err := store.GetArtifactVersionLineageRecord(versionID, true)
	if err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	if !found {
		writeV11Detail(w, http.StatusNotFound, "Version "+versionID+" not found")
		return
	}
	s.writeScriptReproducibilityBundle(w, store, artifact, version, lineage)
}

func writeZIPJSON(archive *zip.Writer, name string, value any, modified time.Time) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode ZIP metadata: %w", err)
	}
	content = append(content, '\n')
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetModTime(modified)
	entry, err := archive.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("create ZIP metadata entry: %w", err)
	}
	if _, err := entry.Write(content); err != nil {
		return fmt.Errorf("write ZIP metadata entry: %w", err)
	}
	return nil
}

func pathExtension(filename string) string {
	index := strings.LastIndex(filename, ".")
	if index <= 0 || len(filename)-index > 16 {
		return ""
	}
	return filename[index:]
}
