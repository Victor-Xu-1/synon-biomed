// Package delegatefixture creates a deterministic, no-LLM delegation tree for
// backend/frontend integration tests. It must never be imported by production
// server startup code.
package delegatefixture

import (
	"context"
	"encoding/json"
	"fmt"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

const (
	UserID                          = "local"
	ProjectID                       = "delegate-fixture-project"
	ParentFrameID                   = "delegate-fixture-parent"
	ChildFrameID                    = "delegate-fixture-child"
	SecondChildFrameID              = "delegate-fixture-child-parallel"
	GrandchildFrameID               = "delegate-fixture-grandchild"
	ToolUseID                       = "toolu_delegate_fixture_001"
	SecondToolUseID                 = "toolu_delegate_fixture_002"
	GrandchildToolUseID             = "toolu_delegate_fixture_grandchild_001"
	NotificationToolUseID           = "toolu_delegate_fixture_notifications_001"
	SecondNotificationToolUseID     = "toolu_delegate_fixture_notifications_002"
	NotificationToolUseMessageID    = "delegate-parent-notification-tool-use-001"
	NotificationToolResultMessageID = "delegate-parent-notification-tool-result-001"
	SecondNotificationUseMessageID  = "delegate-parent-notification-tool-use-002"
	SecondNotificationResultID      = "delegate-parent-notification-tool-result-002"
	DelegateName                    = "literature-review"
	SecondDelegateName              = "evidence-check"
	GrandchildDelegateName          = "citation-audit"
)

type Fixture struct {
	UserID                          string `json:"userId"`
	ProjectID                       string `json:"projectId"`
	ParentFrameID                   string `json:"parentFrameId"`
	ChildFrameID                    string `json:"childFrameId"`
	SecondChildFrameID              string `json:"secondChildFrameId"`
	GrandchildFrameID               string `json:"grandchildFrameId"`
	ToolUseID                       string `json:"toolUseId"`
	SecondToolUseID                 string `json:"secondToolUseId"`
	GrandchildToolUseID             string `json:"grandchildToolUseId"`
	NotificationToolUseID           string `json:"notificationToolUseId"`
	SecondNotificationToolUseID     string `json:"secondNotificationToolUseId"`
	NotificationToolUseMessageID    string `json:"notificationToolUseMessageId"`
	NotificationToolResultMessageID string `json:"notificationToolResultMessageId"`
	SecondNotificationUseMessageID  string `json:"secondNotificationToolUseMessageId"`
	SecondNotificationResultID      string `json:"secondNotificationToolResultMessageId"`
	DelegateName                    string `json:"delegateName"`
	SecondDelegateName              string `json:"secondDelegateName"`
	GrandchildDelegateName          string `json:"grandchildDelegateName"`
	TracePath                       string `json:"tracePath"`
}

func Seed(store *workspace.Store) (Fixture, error) {
	fixture := Fixture{
		UserID: UserID, ProjectID: ProjectID, ParentFrameID: ParentFrameID,
		ChildFrameID: ChildFrameID, ToolUseID: ToolUseID,
		SecondChildFrameID: SecondChildFrameID, GrandchildFrameID: GrandchildFrameID,
		SecondToolUseID: SecondToolUseID, GrandchildToolUseID: GrandchildToolUseID,
		NotificationToolUseID:           NotificationToolUseID,
		SecondNotificationToolUseID:     SecondNotificationToolUseID,
		NotificationToolUseMessageID:    NotificationToolUseMessageID,
		NotificationToolResultMessageID: NotificationToolResultMessageID,
		SecondNotificationUseMessageID:  SecondNotificationUseMessageID,
		SecondNotificationResultID:      SecondNotificationResultID,
		DelegateName:                    DelegateName,
		SecondDelegateName:              SecondDelegateName,
		GrandchildDelegateName:          GrandchildDelegateName,
		TracePath:                       "/api/frames/" + ParentFrameID + "/trace-shallow",
	}
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		return Fixture{}, fmt.Errorf("open fixture transcript repository: %w", err)
	}
	if err = repository.RunImmediate(context.Background(), func(tx *transcriptstore.ImmediateTransaction) error {
		_, deleteErr := transcriptstore.DeleteProjectStreamsTx(context.Background(), tx, UserID, ProjectID)
		return deleteErr
	}); err != nil {
		return Fixture{}, fmt.Errorf("delete existing fixture transcript streams: %w", err)
	}
	if _, found, err := store.GetProject(ProjectID); err != nil {
		return Fixture{}, fmt.Errorf("inspect existing fixture project: %w", err)
	} else if found {
		if err := store.DeleteProject(ProjectID); err != nil {
			return Fixture{}, fmt.Errorf("replace existing fixture project: %w", err)
		}
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: ProjectID, UserID: UserID, Name: "Delegate integration fixture"}); err != nil {
		return Fixture{}, fmt.Errorf("create fixture project: %w", err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: ParentFrameID, ProjectID: ProjectID, AgentName: "OPERON",
		Status: "processing", ConversationType: "agent", Name: "Delegate parent fixture",
	}); err != nil {
		return Fixture{}, fmt.Errorf("create parent frame: %w", err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: ChildFrameID, ProjectID: ProjectID, ParentFrameID: ParentFrameID,
		AgentName: "LITERATURE_REVIEW", Status: "completed", ConversationType: "delegate",
		Name: "Literature review child fixture",
	}); err != nil {
		return Fixture{}, fmt.Errorf("create child frame: %w", err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: SecondChildFrameID, ProjectID: ProjectID, ParentFrameID: ParentFrameID,
		AgentName: "EVIDENCE_CHECK", Status: "awaiting_user_response", ConversationType: "delegate",
		Name: "Evidence check parallel child fixture",
	}); err != nil {
		return Fixture{}, fmt.Errorf("create second child frame: %w", err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: GrandchildFrameID, ProjectID: ProjectID, ParentFrameID: ChildFrameID,
		AgentName: "CITATION_AUDIT", Status: "failed", ConversationType: "delegate",
		Name: "Citation audit grandchild fixture",
	}); err != nil {
		return Fixture{}, fmt.Errorf("create grandchild frame: %w", err)
	}
	if _, err := store.SetFrameRuntimeMetadata(ParentFrameID, workspace.FrameRuntimeMetadata{
		ContextData: map[string]any{
			"_tool_id_to_frame_id": map[string]any{
				ToolUseID: ChildFrameID, NotificationToolUseID: ChildFrameID,
				SecondToolUseID: SecondChildFrameID, SecondNotificationToolUseID: SecondChildFrameID,
			},
		},
	}); err != nil {
		return Fixture{}, fmt.Errorf("set parent metadata: %w", err)
	}
	latestToolBlock := map[string]any{
		"type": "tool_use", "id": ToolUseID, "name": "Agent",
		"human_description": "Delegate a focused literature review", "status": "completed",
	}
	if _, err := store.SetFrameRuntimeMetadata(ChildFrameID, workspace.FrameRuntimeMetadata{
		DelegateName: DelegateName,
		ContextData: map[string]any{
			"_latest_tool_block": latestToolBlock,
			"_tool_id_to_frame_id": map[string]any{
				GrandchildToolUseID: GrandchildFrameID,
			},
		},
	}); err != nil {
		return Fixture{}, fmt.Errorf("set child metadata: %w", err)
	}
	if _, err := store.SetFrameRuntimeMetadata(SecondChildFrameID, workspace.FrameRuntimeMetadata{
		DelegateName: SecondDelegateName,
		ContextData: map[string]any{"_latest_tool_block": map[string]any{
			"type": "tool_use", "id": SecondToolUseID, "name": "Agent",
			"human_description": "Check the evidence table and request one missing assay detail",
			"status":            "needs_input",
		}},
	}); err != nil {
		return Fixture{}, fmt.Errorf("set second child metadata: %w", err)
	}
	if _, err := store.SetFrameRuntimeMetadata(GrandchildFrameID, workspace.FrameRuntimeMetadata{
		DelegateName: GrandchildDelegateName,
		ContextData: map[string]any{"_latest_tool_block": map[string]any{
			"type": "tool_use", "id": GrandchildToolUseID, "name": "Agent",
			"human_description": "Audit deterministic citation identifiers",
			"status":            "failed",
		}},
	}); err != nil {
		return Fixture{}, fmt.Errorf("set grandchild metadata: %w", err)
	}
	notificationContent, err := json.Marshal(map[string]any{
		"notifications": []any{
			map[string]any{
				"notification_type": "completion", "sender_frame_id": ChildFrameID,
				"payload": map[string]any{
					"status": "completed", "_completion_bullets": []string{
						"Reviewed deterministic fixture evidence.",
						"Returned two source-backed findings.",
					},
					"wall_s": 4.25,
				},
			},
			map[string]any{
				"notification_type": "child_message", "sender_frame_id": ChildFrameID,
				"payload": map[string]any{
					"sender_frame_id": ChildFrameID, "name": DelegateName,
					"agent_name": "LITERATURE_REVIEW", "kind": "question",
					"text": "Should the review include adjacent therapeutic targets?",
				},
			},
			map[string]any{
				"notification_type": "child_message", "sender_frame_id": ChildFrameID,
				"payload": map[string]any{
					"sender_frame_id": ChildFrameID, "name": DelegateName,
					"agent_name": "LITERATURE_REVIEW", "kind": "info",
					"text": "The deterministic evidence set contains two retained sources.",
				},
			},
		},
	})
	if err != nil {
		return Fixture{}, fmt.Errorf("marshal fixture notifications: %w", err)
	}
	questionContent, err := json.Marshal(map[string]any{
		"notifications": []any{map[string]any{
			"notification_type": "child_message", "sender_frame_id": SecondChildFrameID,
			"payload": map[string]any{
				"sender_frame_id": SecondChildFrameID, "name": SecondDelegateName,
				"agent_name": "EVIDENCE_CHECK", "kind": "question",
				"text": "Which assay endpoint should be treated as the primary evidence column?",
			},
		}},
	})
	if err != nil {
		return Fixture{}, fmt.Errorf("marshal second child question notification: %w", err)
	}
	parentEvents := []workspace.FrameEventInput{
		{ID: "delegate-parent-user-001", FrameID: ParentFrameID, Type: "user_message", Payload: map[string]any{
			"role": "user", "content": []any{map[string]any{
				"type": "text", "text": "Delegate a literature review without calling an LLM.",
			}},
		}},
		{ID: "delegate-parent-tool-use-001", FrameID: ParentFrameID, Type: "assistant_message", Payload: map[string]any{
			"role": "assistant", "content": []any{map[string]any{
				"type": "tool_use", "id": ToolUseID, "name": "Agent",
				"input": map[string]any{
					"description": "Review deterministic fixture evidence", "delegate_name": DelegateName,
					"human_description": "Delegate a focused literature review", "child_frame_id": ChildFrameID,
				},
			}},
		}},
		{ID: "delegate-parent-tool-result-001", FrameID: ParentFrameID, Type: "user_message", Payload: map[string]any{
			"role": "user", "content": []any{map[string]any{
				"type": "tool_result", "tool_use_id": ToolUseID,
				"content": "Child frame completed the fixture task.",
			}},
		}},
		{ID: NotificationToolUseMessageID, FrameID: ParentFrameID, Type: "assistant_message", Payload: map[string]any{
			"role": "assistant", "content": []any{map[string]any{
				"type": "tool_use", "id": NotificationToolUseID, "name": "collect_notifications",
				"input": map[string]any{
					"sender_frame_id":   ChildFrameID,
					"human_description": "Collect completion and child messages from the delegate",
				},
			}},
		}},
		{ID: NotificationToolResultMessageID, FrameID: ParentFrameID, Type: "user_message", Payload: map[string]any{
			"role": "user", "content": []any{map[string]any{
				"type": "tool_result", "tool_use_id": NotificationToolUseID,
				"content": string(notificationContent),
			}},
		}},
		{ID: "delegate-parent-tool-use-002", FrameID: ParentFrameID, Type: "assistant_message", Payload: map[string]any{
			"role": "assistant", "content": []any{map[string]any{
				"type": "tool_use", "id": SecondToolUseID, "name": "Agent",
				"input": map[string]any{
					"description":       "Check the deterministic evidence table",
					"delegate_name":     SecondDelegateName,
					"human_description": "Delegate a parallel evidence check",
					"child_frame_id":    SecondChildFrameID,
				},
			}},
		}},
		{ID: "delegate-parent-tool-result-002", FrameID: ParentFrameID, Type: "user_message", Payload: map[string]any{
			"role": "user", "content": []any{map[string]any{
				"type": "tool_result", "tool_use_id": SecondToolUseID,
				"content": "Parallel child is waiting for one assay clarification.",
			}},
		}},
		{ID: SecondNotificationUseMessageID, FrameID: ParentFrameID, Type: "assistant_message", Payload: map[string]any{
			"role": "assistant", "content": []any{map[string]any{
				"type": "tool_use", "id": SecondNotificationToolUseID, "name": "collect_notifications",
				"input": map[string]any{
					"sender_frame_id":   SecondChildFrameID,
					"human_description": "Collect the parallel child question",
				},
			}},
		}},
		{ID: SecondNotificationResultID, FrameID: ParentFrameID, Type: "user_message", Payload: map[string]any{
			"role": "user", "content": []any{map[string]any{
				"type": "tool_result", "tool_use_id": SecondNotificationToolUseID,
				"content": string(questionContent),
			}},
		}},
	}
	for _, event := range parentEvents {
		if _, err := store.AppendFrameEvent(event); err != nil {
			return Fixture{}, fmt.Errorf("append parent event %s: %w", event.ID, err)
		}
	}
	childEvents := []workspace.FrameEventInput{
		{ID: "delegate-child-user-001", FrameID: ChildFrameID, Type: "user_message", Payload: map[string]any{
			"role": "user", "content": []any{map[string]any{"type": "text", "text": "Review deterministic fixture evidence."}},
		}},
		{ID: "delegate-child-assistant-001", FrameID: ChildFrameID, Type: "assistant_message", Payload: map[string]any{
			"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "Fixture review complete."}},
		}},
		{ID: "delegate-child-tool-use-grandchild-001", FrameID: ChildFrameID, Type: "assistant_message", Payload: map[string]any{
			"role": "assistant", "content": []any{map[string]any{
				"type": "tool_use", "id": GrandchildToolUseID, "name": "Agent",
				"input": map[string]any{
					"description":       "Audit deterministic citation identifiers",
					"delegate_name":     GrandchildDelegateName,
					"human_description": "Delegate a nested citation audit",
					"child_frame_id":    GrandchildFrameID,
				},
			}},
		}},
		{ID: "delegate-child-tool-result-grandchild-001", FrameID: ChildFrameID, Type: "user_message", Payload: map[string]any{
			"role": "user", "content": []any{map[string]any{
				"type": "tool_result", "tool_use_id": GrandchildToolUseID,
				"content": "Nested citation audit failed on a deliberately missing identifier.",
			}},
		}},
	}
	for _, event := range childEvents {
		if _, err := store.AppendFrameEvent(event); err != nil {
			return Fixture{}, fmt.Errorf("append child event %s: %w", event.ID, err)
		}
	}
	secondChildEvents := []workspace.FrameEventInput{
		{ID: "delegate-second-child-user-001", FrameID: SecondChildFrameID, Type: "user_message", Payload: map[string]any{
			"role": "user", "content": []any{map[string]any{
				"type": "text", "text": "Check the deterministic evidence table in parallel.",
			}},
		}},
		{ID: "delegate-second-child-question-001", FrameID: SecondChildFrameID, Type: "assistant_message", Payload: map[string]any{
			"role": "assistant",
			"content": []any{map[string]any{
				"type": "text", "text": "Which assay endpoint should be treated as the primary evidence column?",
			}},
			"kind": "question",
		}},
	}
	for _, event := range secondChildEvents {
		if _, err := store.AppendFrameEvent(event); err != nil {
			return Fixture{}, fmt.Errorf("append second child event %s: %w", event.ID, err)
		}
	}
	grandchildEvents := []workspace.FrameEventInput{
		{ID: "delegate-grandchild-user-001", FrameID: GrandchildFrameID, Type: "user_message", Payload: map[string]any{
			"role": "user", "content": []any{map[string]any{"type": "text", "text": "Audit deterministic citation identifiers."}},
		}},
		{ID: "delegate-grandchild-assistant-001", FrameID: GrandchildFrameID, Type: "assistant_message", Payload: map[string]any{
			"role": "assistant",
			"content": []any{map[string]any{
				"type": "text", "text": "Citation audit failed deterministically because one source identifier is absent.",
			}},
			"error": "missing_source_identifier",
		}},
	}
	for _, event := range grandchildEvents {
		if _, err := store.AppendFrameEvent(event); err != nil {
			return Fixture{}, fmt.Errorf("append grandchild event %s: %w", event.ID, err)
		}
	}
	return fixture, nil
}
