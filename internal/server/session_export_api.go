package server

import (
	"compress/gzip"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) handleFrameExport(w http.ResponseWriter, r *http.Request) {
	store, ok := s.attachmentStore(w)
	if !ok {
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/frames/"))
	if len(segments) < 2 || len(segments) > 3 {
		writeAttachmentError(w, http.StatusNotFound, "frame export endpoint not found")
		return
	}
	frameID, err := url.PathUnescape(segments[0])
	if err != nil || strings.TrimSpace(frameID) == "" {
		writeAttachmentError(w, http.StatusBadRequest, "invalid frame id")
		return
	}
	if len(segments) == 2 && (segments[1] == "export" || segments[1] == "bundle") {
		exported, found, err := store.BuildCompatibilitySessionExport(r.Context(), userID, frameID, time.Now())
		if err != nil {
			writeAttachmentStoreError(w, err)
			return
		}
		if !found {
			writeV11Detail(w, http.StatusNotFound, "Frame "+frameID+" not found")
			return
		}
		if segments[1] == "export" {
			s.writeSessionExport(w, r, exported)
			return
		}
		s.writeSessionBundle(w, r, store, exported)
		return
	}
	frame, found, err := store.GetFrame(frameID)
	if err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	if !found {
		writeAttachmentError(w, http.StatusNotFound, "frame not found")
		return
	}
	owned, err := store.ProjectOwnedBy(frame.ProjectID, userID)
	if err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	if !owned {
		writeAttachmentError(w, http.StatusNotFound, "frame not found")
		return
	}
	if len(segments) == 2 && segments[1] == "trace-shallow" {
		s.handleFrameTraceShallow(w, r, store, frame)
		return
	}
	snapshot, err := store.BuildFrameSessionExport(frame.RootFrameID)
	if err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	switch {
	case len(segments) == 2 && segments[1] == "artifacts":
		if r.Method != http.MethodGet {
			writeAttachmentError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeJSON(w, http.StatusOK, snapshot.Artifacts)
	case len(segments) == 3 && segments[1] == "artifacts" && segments[2] == "download":
		if r.Method != http.MethodGet {
			writeAttachmentError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		ids := make([]string, 0, len(snapshot.Artifacts))
		for _, artifact := range snapshot.Artifacts {
			ids = append(ids, artifact.ID)
		}
		if len(ids) == 0 {
			writeAttachmentError(w, http.StatusNotFound, "frame has no downloadable artifacts")
			return
		}
		framePrefix := snapshot.RootFrame.ID
		if len(framePrefix) > 8 {
			framePrefix = framePrefix[:8]
		}
		filename := "artifacts_" + framePrefix + ".zip"
		if err := s.writeArtifactZIP(w, r, store, userID, ids, queryBool(r, "include_metadata"), filename); err != nil {
			writeAttachmentStoreError(w, err)
		}
	default:
		writeAttachmentError(w, http.StatusNotFound, "frame export endpoint not found")
	}
}

func (s *Server) writeSessionExport(w http.ResponseWriter, r *http.Request, snapshot workspace.CompatibilitySessionExport) {
	if r.Method != http.MethodGet {
		writeAttachmentError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	filename := compatibilitySessionExportFilename(snapshot.RootFrameID, snapshot.ExportedAt)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if strings.Contains(strings.ToLower(r.Header.Get("Accept-Encoding")), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		w.WriteHeader(http.StatusOK)
		compressed := gzip.NewWriter(w)
		if err := json.NewEncoder(compressed).Encode(snapshot); err != nil {
			_ = compressed.Close()
			return
		}
		_ = compressed.Close()
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func compatibilitySessionExportFilename(rootFrameID, exportedAt string) string {
	prefix := rootFrameID
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	stamp := strings.ReplaceAll(strings.TrimSuffix(exportedAt, "Z"), ":", "-")
	if index := strings.IndexByte(stamp, '.'); index >= 0 {
		stamp = stamp[:index]
	}
	return safeArchiveFilename(prefix+"_"+stamp+".json", "session-export.json")
}
