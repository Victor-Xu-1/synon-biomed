package server

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	workspace "synon-go/internal/persistence/workspace"
)

type webConversationLineageFrame struct {
	FrameID           string  `json:"frameId"`
	RootFrameID       string  `json:"rootFrameId"`
	ParentFrameID     *string `json:"parentFrameId"`
	Ordinal           int     `json:"ordinal"`
	Label             string  `json:"label"`
	AgentName         *string `json:"agentName"`
	Status            string  `json:"status"`
	StatusDescription *string `json:"statusDescription"`
	TaskSummary       *string `json:"taskSummary"`
	MessageCount      int     `json:"messageCount"`
	DirectChildCount  int     `json:"directChildCount"`
}

type webConversationLineageTotals struct {
	NeedsInput int `json:"needsInput"`
	Running    int `json:"running"`
	Failed     int `json:"failed"`
	Completed  int `json:"completed"`
	Stopped    int `json:"stopped"`
}

type webConversationLineage struct {
	RootFrameID    string                        `json:"rootFrameId"`
	CurrentFrameID string                        `json:"currentFrameId"`
	Current        webConversationLineageFrame   `json:"current"`
	Ancestors      []webConversationLineageFrame `json:"ancestors"`
	DirectChildren []webConversationLineageFrame `json:"directChildren"`
	RootChildren   []webConversationLineageFrame `json:"rootChildren"`
	Totals         webConversationLineageTotals  `json:"totals"`
}

const webConversationUntitledLabel = "未命名任务"

// webConversationFrameLabel returns the display label for a lineage frame.
// The frontend contract requires a non-empty label, so frames without a
// delegate name, conversation name, or task summary fall back to a stable
// untitled label instead of emitting an empty string.
func webConversationFrameLabel(frame workspace.Frame) string {
	for _, candidate := range []string{frame.DelegateName, frame.Name, frame.TaskSummary} {
		if label := strings.TrimSpace(candidate); label != "" {
			return label
		}
	}
	if request := strings.TrimSpace(compatibilityInputRequestText(frame.InputData["request"])); request != "" {
		return request
	}
	return webConversationUntitledLabel
}

func (s *Server) handleWebConversationLineage(
	w http.ResponseWriter,
	r *http.Request,
	requested workspace.CompatibilityFrame,
) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	snapshot, err := s.workspaceStore.GetFrameTraceSnapshot(requested.RootFrameID, nil)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"message": "unable to load conversation lineage"})
		return
	}
	lineage, err := buildWebConversationLineage(requested.ID, snapshot)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"message": "unable to project conversation lineage"})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, lineage)
}

func buildWebConversationLineage(frameID string, snapshot workspace.FrameTraceSnapshot) (webConversationLineage, error) {
	frameByID := make(map[string]workspace.Frame, len(snapshot.Frames))
	for _, frame := range snapshot.Frames {
		frameByID[frame.ID] = frame
	}
	current, found := frameByID[frameID]
	if !found {
		return webConversationLineage{}, fmt.Errorf("frame %q is missing from trace", frameID)
	}
	root, found := frameByID[current.RootFrameID]
	if !found {
		return webConversationLineage{}, fmt.Errorf("frame %q trace has no root", frameID)
	}

	childrenByParent := make(map[string][]workspace.Frame)
	for _, frame := range snapshot.Frames {
		if frame.ParentFrameID == "" || frame.IsHidden {
			continue
		}
		childrenByParent[frame.ParentFrameID] = append(childrenByParent[frame.ParentFrameID], frame)
	}
	ordered := make([]workspace.Frame, 0, len(snapshot.Frames)-1)
	for _, frame := range snapshot.Frames {
		if frame.ID != root.ID && !frame.IsHidden {
			ordered = append(ordered, frame)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].RootSequence != ordered[j].RootSequence {
			return ordered[i].RootSequence < ordered[j].RootSequence
		}
		if !ordered[i].CreatedAt.Equal(ordered[j].CreatedAt) {
			return ordered[i].CreatedAt.Before(ordered[j].CreatedAt)
		}
		return ordered[i].ID < ordered[j].ID
	})
	ordinalByID := map[string]int{root.ID: 0}
	for index, frame := range ordered {
		ordinalByID[frame.ID] = index + 1
	}
	for parentID := range childrenByParent {
		sort.SliceStable(childrenByParent[parentID], func(i, j int) bool {
			return ordinalByID[childrenByParent[parentID][i].ID] < ordinalByID[childrenByParent[parentID][j].ID]
		})
	}

	project := func(frame workspace.Frame) webConversationLineageFrame {
		return webConversationLineageFrame{
			FrameID: frame.ID, RootFrameID: frame.RootFrameID,
			ParentFrameID: webNullableString(frame.ParentFrameID), Ordinal: ordinalByID[frame.ID],
			Label: webConversationFrameLabel(frame), AgentName: webNullableString(frame.AgentName), Status: webConversationDelegateStatus(frame),
			StatusDescription: webNullableString(frame.StatusDescription), TaskSummary: webNullableString(frame.TaskSummary),
			MessageCount: frame.MessageCount, DirectChildCount: len(childrenByParent[frame.ID]),
		}
	}

	ancestors := make([]workspace.Frame, 0)
	for parentID := current.ParentFrameID; parentID != ""; {
		parent, exists := frameByID[parentID]
		if !exists {
			break
		}
		ancestors = append([]workspace.Frame{parent}, ancestors...)
		parentID = parent.ParentFrameID
	}
	projectAll := func(frames []workspace.Frame) []webConversationLineageFrame {
		result := make([]webConversationLineageFrame, 0, len(frames))
		for _, frame := range frames {
			result = append(result, project(frame))
		}
		return result
	}
	rootChildren := projectAll(childrenByParent[root.ID])
	totals := webConversationLineageTotals{}
	for _, child := range rootChildren {
		switch child.Status {
		case "needs-input":
			totals.NeedsInput++
		case "running":
			totals.Running++
		case "failed":
			totals.Failed++
		case "stopped":
			totals.Stopped++
		default:
			totals.Completed++
		}
	}
	return webConversationLineage{
		RootFrameID: root.ID, CurrentFrameID: current.ID, Current: project(current),
		Ancestors: projectAll(ancestors), DirectChildren: projectAll(childrenByParent[current.ID]),
		RootChildren: rootChildren, Totals: totals,
	}, nil
}

func webConversationDelegateStatus(frame workspace.Frame) string {
	if pending, ok := frame.OutputData["pending_input_requests"].([]any); ok && len(pending) > 0 {
		return "needs-input"
	}
	if strings.EqualFold(strings.TrimSpace(frame.Status), "awaiting_plan_approval") &&
		strings.TrimSpace(stringValue(frame.ContextData["_plan_artifact_id"])) != "" {
		return "needs-input"
	}
	switch strings.ToLower(strings.TrimSpace(frame.Status)) {
	case "awaiting_user_response", "awaiting_plan_approval", "needs_input", "needs-input", "awaiting_input":
		// A durable user-input boundary is a real pause in the delegated
		// conversation, not active execution. Keep it distinct from running so
		// the capsule can offer the answer action and never show a false spinner.
		return "needs-input"
	case "processing", "running", "executing", "in_progress", "in-progress", "queued", "pending", "created":
		return "running"
	case "failed", "error":
		return "failed"
	case "cancelled", "canceled", "stopped", "replaced":
		return "stopped"
	default:
		return "completed"
	}
}

func webNullableString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}
