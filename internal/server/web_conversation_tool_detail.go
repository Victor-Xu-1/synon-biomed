package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

const maxWebConversationToolDetailPageSize = 100

func (s *Server) handleWebConversationToolDetail(
	w http.ResponseWriter,
	r *http.Request,
	frame workspace.CompatibilityFrame,
	messageID string,
) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	ownerID := compatAgentUserID(r)
	requestedBranchID, branchStatus, branchErr := webConversationToolDetailRequestedBranch(r)
	if branchErr != nil {
		writeWorkspaceJSON(w, branchStatus, map[string]any{"message": branchErr.Error()})
		return
	}
	message, resolvedBranchID, found, err := s.loadTranscriptToolDetailMessage(
		r, frame.ID, ownerID, messageID, requestedBranchID,
	)
	if err != nil {
		writeWebConversationError(w, err)
		return
	}
	if !found {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "tool detail not found"})
		return
	}
	content, ok := message["content"].(map[string]any)
	if !ok || strings.TrimSpace(webString(content["call_id"])) == "" {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "tool detail not found"})
		return
	}

	detailRequest, status, parseErr := parseWebConversationToolDetailRequest(
		r, messageID, content, resolvedBranchID,
	)
	if parseErr != nil {
		writeWorkspaceJSON(w, status, map[string]any{"message": parseErr.Error()})
		return
	}
	segments, err := parseWebConversationToolDetailPath(detailRequest.Path)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	reader, closer, err := s.openWebConversationToolDetailSource(
		r, frame.ID, frame.RootFrameID, ownerID, detailRequest.Section, content,
	)
	if err != nil {
		if errors.Is(err, errWebConversationToolDetailPathNotFound) {
			writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "tool detail is unavailable"})
		} else {
			writeWebConversationError(w, transcriptWebStorageError(err))
		}
		return
	}
	if closer != nil {
		defer closer.Close()
	}
	reader = &webConversationToolDetailContextReader{Context: r.Context(), ReadSeeker: reader}
	limit := max(1, min(
		compatPositiveQuery(r, "limit", maxWebConversationToolDetailPageSize, maxWebConversationToolDetailPageSize),
		maxWebConversationToolDetailPageSize,
	))
	var page webConversationToolDetailPage
	if detailRequest.Cursor == nil {
		page, err = readWebConversationToolDetailPage(
			reader, segments, detailRequest.Path, detailRequest.Offset, limit,
		)
	} else {
		page, err = readWebConversationToolDetailContinuation(
			reader, detailRequest.Path, *detailRequest.Cursor, limit,
		)
	}
	if err != nil {
		if errors.Is(err, errWebConversationToolDetailPathNotFound) {
			writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "tool detail path not found"})
		} else {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "tool detail data is invalid"})
		}
		return
	}
	var nextCursor any
	if detailRequest.Offset+len(page.Items) < page.Total {
		if page.NextByteOffset <= 0 || page.Kind == "scalar" {
			writeWebConversationError(w, transcriptWebStorageError(transcriptstore.ErrEventConflict))
			return
		}
		nextCursor, err = encodeWebConversationToolDetailCursor(webConversationToolDetailCursor{
			Version: 1, MessageID: messageID, BranchID: detailRequest.BranchID,
			Section: detailRequest.Section, Path: detailRequest.Path,
			Revision: detailRequest.Revision, Offset: detailRequest.Offset + len(page.Items),
			ByteOffset: page.NextByteOffset, Total: page.Total, Kind: page.Kind,
		})
		if err != nil {
			writeWebConversationError(w, transcriptWebStorageError(err))
			return
		}
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"message_id": messageID, "branch_id": detailRequest.BranchID,
		"section": detailRequest.Section, "path": detailRequest.Path,
		"revision": detailRequest.Revision, "kind": page.Kind, "total": page.Total,
		"from": detailRequest.Offset, "items": page.Items, "next_cursor": nextCursor,
	})
}

func (s *Server) loadTranscriptToolDetailMessage(
	r *http.Request,
	frameID, ownerID, messageID, branchID string,
) (map[string]any, string, bool, error) {
	if s.transcriptWebReadModel == nil {
		if s.transcriptStore == nil {
			return nil, "", false, nil
		}
	} else {
		active, found, err := s.activatedTranscriptWebReadModel(r.Context(), ownerID, frameID, branchID)
		if err != nil {
			return nil, "", false, err
		}
		if found {
			record, recordFound, err := s.transcriptWebReadModel.GetTranscriptWebMessageByID(
				r.Context(), active.transcriptWebMessageIdentity(ownerID, messageID),
			)
			if err != nil {
				return nil, "", false, transcriptWebReadModelServingError(err)
			}
			if !recordFound {
				return nil, active.snapshot.BranchID, false, nil
			}
			messages, err := transcriptWebReadModelMessageMaps([]transcriptstore.TranscriptWebMessageRecord{record})
			if err != nil || len(messages) != 1 {
				return nil, "", false, transcriptWebStorageError(err)
			}
			return messages[0], active.snapshot.BranchID, true, nil
		}
	}
	view, active, err := s.loadActivatedTranscriptWebHistoryView(r.Context(), ownerID, frameID, branchID)
	if err != nil {
		return nil, "", false, err
	}
	if !active {
		if s.transcriptStore != nil {
			return nil, "", false, transcriptWebAuthorityUnavailableError()
		}
		return nil, "", false, nil
	}
	index := exactTranscriptMessageIndexValue(view.messages, messageID)
	if index < 0 {
		return nil, view.snapshot.BranchID, false, nil
	}
	return cloneTranscriptWebProjectionMap(view.messages[index]), view.snapshot.BranchID, true, nil
}

func (s *Server) openWebConversationToolDetailSource(
	r *http.Request,
	frameID, rootFrameID, ownerID, section string,
	content map[string]any,
) (io.ReadSeeker, io.Closer, error) {
	value := content[section]
	if section == "input" && value == nil {
		value = content["args"]
	}
	if section == "output" && value == nil {
		value = content["error"]
	}
	if text, ok := value.(string); ok {
		if section == "output" {
			if descriptor, found := decodeWebConversationLargeToolResultDescriptor(text); found {
				record, reader, available, err := s.workspaceStore.OpenRunnerLargeToolResultContent(
					r.Context(), descriptor.VersionID, ownerID,
				)
				if err != nil {
					return nil, nil, err
				}
				if !available {
					return nil, nil, errWebConversationToolDetailPathNotFound
				}
				callID := strings.TrimSpace(webString(content["call_id"]))
				toolName := strings.TrimSpace(webString(content["name"]))
				attempt := webConversationToolDetailRevision(content["attempt"])
				if record.ArtifactID != descriptor.ArtifactID || record.RootFrameID != rootFrameID ||
					record.FrameID != frameID || record.ToolCallID != callID || record.ToolName != toolName ||
					(attempt > 0 && record.Attempt != attempt) {
					_ = reader.Close()
					return nil, nil, errWebConversationToolDetailPathNotFound
				}
				return reader, reader, nil
			}
		}
		return strings.NewReader(text), nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 || string(encoded) == "null" {
		return nil, nil, errWebConversationToolDetailPathNotFound
	}
	return strings.NewReader(string(encoded)), nil, nil
}

type webConversationLargeToolResultDescriptor struct {
	ArtifactID string `json:"artifact_id"`
	VersionID  string `json:"version_id"`
	ContentURL string `json:"content_url"`
	Truncated  bool   `json:"truncated"`
}

type webConversationToolDetailContextReader struct {
	context.Context
	io.ReadSeeker
}

func (reader *webConversationToolDetailContextReader) Read(buffer []byte) (int, error) {
	if err := reader.Context.Err(); err != nil {
		return 0, err
	}
	return reader.ReadSeeker.Read(buffer)
}

func decodeWebConversationLargeToolResultDescriptor(raw string) (webConversationLargeToolResultDescriptor, bool) {
	var descriptor webConversationLargeToolResultDescriptor
	if json.Unmarshal([]byte(raw), &descriptor) != nil || !descriptor.Truncated ||
		!workspace.IsRunnerLargeToolResultArtifactID(descriptor.ArtifactID) ||
		!workspace.IsRunnerLargeToolResultVersionID(descriptor.VersionID) {
		return webConversationLargeToolResultDescriptor{}, false
	}
	expected := "/api/artifacts/" + url.PathEscape(descriptor.ArtifactID) +
		"/versions/" + url.PathEscape(descriptor.VersionID)
	return descriptor, descriptor.ContentURL == expected
}

var errWebConversationToolDetailPathNotFound = errors.New("tool detail path not found")
