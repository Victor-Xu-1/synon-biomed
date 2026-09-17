package server

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"strings"
	eventjournal "synon-go/internal/persistence/journal"
	workspace "synon-go/internal/persistence/workspace"
	"unicode"
	"unicode/utf16"
)

func (s *Server) sendKernelChildMessage(ctx context.Context, access workspace.KernelFrameAccess, target, message, kind, callID string, budgets *kernelMessageBudgets) (map[string]any, error) {
	normalized := normalizeKernelPeerMessage(message)
	if normalized == "" {
		return map[string]any{"target": target, "status": "refused", "error": "message empty after whitespace/control-char collapse"}, nil
	}
	isAsideMain := target == "main" || (access.Frame.ConversationType == "aside" || access.Frame.ConversationType == "aside_session") && target == access.Frame.RootFrameID
	isDirectParent := target == "parent" || strings.TrimSpace(access.Frame.ParentFrameID) != "" && target == access.Frame.ParentFrameID
	if budgets == nil {
		budgets = &kernelMessageBudgets{}
	}
	if isAsideMain {
		budgets.mu.Lock()
		defer budgets.mu.Unlock()
		if budgets.asideCallIDs == nil {
			budgets.asideCallIDs = map[string]struct{}{}
		}
		_, callSeen := budgets.asideCallIDs[callID]
		if !callSeen && budgets.asidePosts >= 8 {
			return map[string]any{"status": "refused", "error": `per-dispatch aside→main send_message cap reached (8, shared across "main" and the main root's frame_id)`}, nil
		}
		length := kernelUTF16Length(normalized)
		if length > 16384 {
			return map[string]any{"status": "refused", "error": fmt.Sprintf("message too long (%d chars; max 16384). Summarize it — asides cannot create artifacts.", length)}, nil
		}
		message = normalized
		kind = "info"
		notificationID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("kernel-aside-main:"+access.Frame.ID+":"+callID)).String()
		freshReservation := false
		if !callSeen {
			pending, fresh, accepted := s.reserveKernelPeerPending(notificationID, access.Frame.RootFrameID)
			if !accepted {
				return map[string]any{"status": "refused", "error": fmt.Sprintf("main session already has %d undelivered peer-agent notes queued (cap 32). Wait for main to drain them.", pending)}, nil
			}
			freshReservation = fresh
		}
		input := workspace.CreateNotificationInput{
			ID:            notificationID,
			SenderFrameID: access.Frame.ID, RecipientFrameID: access.Frame.RootFrameID,
			RootFrameID: access.Frame.RootFrameID, OwnerUserID: access.UserID,
			NotificationType: "child_message",
			Payload: map[string]any{
				"sender_frame_id": access.Frame.ID, "name": access.Frame.Name,
				"agent_name": access.Frame.AgentName, "text": message, "kind": "info", "from_aside": true,
			},
		}
		var notification workspace.Notification
		var err error
		switch access.Frame.ConversationType {
		case "aside", "aside_session":
			notification, _, err = s.workspaceStore.CreateNotification(ctx, input)
		case "agent":
			notification, _, err = s.workspaceStore.CreateCompatibilityAsideMainNotification(ctx, input)
		default:
			if freshReservation {
				s.releaseKernelPeerReservations(notificationID)
			}
			return map[string]any{"target": target, "status": "failed", "error": "main is available only as an info target from an aside frame"}, nil
		}
		if err != nil {
			if freshReservation {
				s.releaseKernelPeerReservations(notificationID)
			}
			return nil, err
		}
		if !callSeen {
			budgets.asideCallIDs[callID] = struct{}{}
			budgets.asidePosts++
		}
		if _, err := s.eventJournal.Append(notification.RecipientFrameID, eventjournal.Message{
			"type": "message", "role": "user", "text": "[From side chat] " + message,
			"kind": "info", "sender_frame_id": access.Frame.ID, "fromAside": true,
		}, eventjournal.Metadata{ClientMessageID: "kernel-aside-main:" + callID}); err != nil {
			return nil, err
		}
		mainStatus := ""
		if mainFrame, found, _ := s.workspaceStore.GetFrame(notification.RecipientFrameID); found {
			mainStatus = mainFrame.Status
		}
		active := mainStatus == "processing"
		hint := "Main session is not currently processing — the note is held in-memory only (not persisted; lost on daemon restart) and lands at main's next user turn."
		if active {
			hint = "Delivered as a user-interrupt: main consumes it at its next loop iteration. Held in-memory until then (lost on daemon restart); if main's turn ends first, the note waits for the next user turn."
		}
		status := "queued_idle"
		if active {
			status = "injected"
		}
		return map[string]any{"status": status, "main_frame_id": notification.RecipientFrameID, "main_status": mainStatus, "system_hint": hint}, nil
	}
	var parentNotificationID string
	var freshParentReservation bool
	if isDirectParent {
		budgets.mu.Lock()
		defer budgets.mu.Unlock()
		if budgets.peerCallIDs == nil {
			budgets.peerCallIDs = map[string]struct{}{}
		}
		_, callSeen := budgets.peerCallIDs[callID]
		if !callSeen && budgets.peerPosts >= 32 {
			return map[string]any{
				"target": access.Frame.ParentFrameID, "status": "failed",
				"error": "too many send_message peer posts this task (cap: 32, not reset in-task) — consolidate remaining updates into your final result / structured output instead of posting more.",
			}, nil
		}
		message = truncateKernelUTF16(normalized, 4000)
		parentNotificationID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("kernel-child-message-notification:"+access.Frame.ID+":"+access.Frame.ParentFrameID+":"+callID)).String()
		if !callSeen {
			pending, freshReservation, accepted := s.reserveKernelPeerPending(parentNotificationID, access.Frame.ParentFrameID)
			if !accepted {
				return map[string]any{"target": access.Frame.ParentFrameID, "status": "refused", "error": fmt.Sprintf("target already has %d undelivered peer-agent notes queued (cap 32). Wait for it to drain them.", pending)}, nil
			}
			freshParentReservation = freshReservation
		}
	} else {
		message = normalized
	}
	queued, err := s.workspaceStore.QueueKernelSupervisionMessageWithID(ctx, access.Frame.ID, target, access.UserID, callID, message, kind)
	if err != nil {
		if freshParentReservation {
			s.releaseKernelPeerReservations(parentNotificationID)
		}
		return map[string]any{"target": target, "status": "failed", "error": err.Error()}, nil
	}
	if queued.Relation == "parent" {
		if isDirectParent {
			_, callSeen := budgets.peerCallIDs[callID]
			if !callSeen {
				budgets.peerCallIDs[callID] = struct{}{}
				budgets.peerPosts++
			}
		}
		clientID := "kernel-parent-message:" + callID
		if _, err := s.eventJournal.Append(queued.TargetFrameID, eventjournal.Message{
			"type": "message", "role": "user", "text": message, "kind": kind,
			"sender_frame_id": access.Frame.ID, "notification_type": "child_message",
		}, eventjournal.Metadata{ClientMessageID: clientID}); err != nil {
			return nil, err
		}
		targetStatus := ""
		if targetFrame, found, _ := s.workspaceStore.GetFrame(queued.TargetFrameID); found {
			targetStatus = targetFrame.Status
		}
		note := "sent."
		if targetStatus == "processing" {
			note = "sent — target consumes it at its next loop iteration (in-memory — not persisted across daemon restart)."
		} else if strings.HasPrefix(targetStatus, "awaiting_") {
			note = "queued — target is parked on user input; reads after it resumes (in-memory — not persisted across daemon restart)."
		}
		if kind == "question" {
			note += " The answer (if any) arrives as a message from that agent at your next turn boundary — it does not block this cell."
		}
		return map[string]any{"status": "sent", "kind": kind, "target_frame_id": queued.TargetFrameID, "note": note}, nil
	} else if queued.Relation == "child" {
		if freshParentReservation {
			s.releaseKernelPeerReservations(parentNotificationID)
		}
		if queued.Queued == nil {
			return nil, errors.New("child message was not durably queued")
		}
		if err := s.materializeKernelChildMessages(queued.Child, []workspace.KernelChildQueuedMessage{*queued.Queued}); err != nil {
			return nil, err
		}
		s.startKernelChildRun(queued.Child)
	}
	return map[string]any{"target": queued.TargetFrameID, "status": queued.Action, "kind": kind, "relation": queued.Relation}, nil
}

func normalizeKernelPeerMessage(value string) string {
	var builder strings.Builder
	space := false
	for _, r := range value {
		collapse := unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
		if collapse {
			space = builder.Len() > 0
			continue
		}
		if space {
			builder.WriteByte(' ')
			space = false
		}
		builder.WriteRune(r)
	}
	normalized := strings.TrimSpace(builder.String())
	for _, prefix := range []string{"From ", "System]", "Stopped by ", "Memory]", "Auditor]"} {
		normalized = strings.ReplaceAll(normalized, "["+prefix, "⟦"+prefix)
	}
	return normalized
}

func kernelUTF16Length(value string) int {
	length := 0
	for _, r := range value {
		length += utf16.RuneLen(r)
	}
	return length
}

func truncateKernelUTF16(value string, limit int) string {
	if kernelUTF16Length(value) <= limit {
		return value
	}
	length := 0
	var builder strings.Builder
	for _, r := range value {
		units := utf16.RuneLen(r)
		if length+units > limit {
			break
		}
		builder.WriteRune(r)
		length += units
	}
	return builder.String() + " …[truncated]"
}

func truncateKernelSupervisionResult(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "..."
}
