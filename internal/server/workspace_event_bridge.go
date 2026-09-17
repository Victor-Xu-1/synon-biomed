package server

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	eventjournal "synon-go/internal/persistence/journal"
	workspace "synon-go/internal/persistence/workspace"
)

// publishWorkspaceEvent is the single bridge from the durable frame journal
// into the v1.1 realtime contract. Durable publication precedes both in-memory
// fanouts so a successful mutation never reports a transient-only event.
func (s *Server) publishWorkspaceEvent(event workspace.FrameEvent) error {
	if s == nil || s.workspaceStore == nil {
		return errors.New("workspace runtime is not configured")
	}
	realtimeID := "frame-event:" + strings.TrimSpace(event.ID)
	if existing, found, err := s.workspaceStore.GetRealtimeEventByID(realtimeID); err != nil {
		return err
	} else if found {
		if strings.TrimSpace(fmt.Sprint(existing.Payload["source_event_id"])) != strings.TrimSpace(event.ID) {
			return fmt.Errorf("realtime event id %q does not identify frame event %q", realtimeID, event.ID)
		}
		frameContext, found, err := s.workspaceStore.GetFrameRealtimeContext(event.FrameID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("frame %q does not exist for realtime publication", event.FrameID)
		}
		if err := s.publishWebFrameEventProjection(frameContext, event); err != nil {
			return fmt.Errorf("persist web projection for frame event %q: %w", event.ID, err)
		}
		s.workspaceEvents.Publish(event)
		s.signalFrameResumeDispatchForEvent(event.Type)
		return nil
	}
	frameContext, found, err := s.workspaceStore.GetFrameRealtimeContext(event.FrameID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("frame %q does not exist for realtime publication", event.FrameID)
	}
	_, err = s.publishCompatEvent(workspace.FrameRealtimeEventInput(realtimeID, frameContext.UserID, frameContext.Frame, event))
	if err != nil {
		return fmt.Errorf("persist baseline event for frame event %q: %w", event.ID, err)
	}
	if err := s.publishWebFrameEventProjection(frameContext, event); err != nil {
		return fmt.Errorf("persist web projection for frame event %q: %w", event.ID, err)
	}
	s.workspaceEvents.Publish(event)
	s.signalFrameResumeDispatchForEvent(event.Type)
	return nil
}

func baselineTypeForFrameEvent(sourceType string) string {
	return workspace.RealtimeTypeForFrameEvent(sourceType)
}

func (s *Server) mirrorSessionEntryToWorkspaceFrame(entry *eventjournal.Entry) error {
	if s == nil || s.workspaceStore == nil || entry == nil {
		return nil
	}
	if _, found, err := s.workspaceStore.GetFrameRealtimeContext(entry.SessionID); err != nil {
		return err
	} else if !found {
		return nil
	}
	payload := make(map[string]any, len(entry.Message)+2)
	for key, value := range entry.Message {
		payload[key] = value
	}
	payload["journal_event_id"] = entry.EventID
	payload["journal_created_at"] = entry.CreatedAt
	eventType := strings.TrimSpace(fmt.Sprint(entry.Message["type"]))
	if eventType == "" || eventType == "message" {
		eventType = strings.TrimSpace(fmt.Sprint(entry.Message["role"])) + "_message"
		payload["type"] = eventType
	}
	if eventType == "runner_finished" {
		description := ""
		if strings.EqualFold(strings.TrimSpace(fmt.Sprint(entry.Message["status"])), "failed") {
			description = strings.TrimSpace(fmt.Sprint(entry.Message["text"]))
		}
		if err := s.workspaceStore.UpdateFrameRuntimePresentation(entry.SessionID, workspace.FrameRuntimePresentationInput{
			StatusDescription: &description,
		}); err != nil {
			return fmt.Errorf("project runner finish status description: %w", err)
		}
	}
	event, err := s.workspaceStore.AppendFrameEvent(workspace.FrameEventInput{
		ID:      "session-journal:" + entry.SessionID + ":" + strconv.FormatInt(entry.EventID, 10),
		FrameID: entry.SessionID, Type: eventType, Payload: payload,
	})
	if err != nil {
		return err
	}
	return s.publishWorkspaceEvent(event)
}
