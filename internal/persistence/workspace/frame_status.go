package workspace

import (
	"errors"
	"strings"
)

const (
	FrameStatusProcessing           = "processing"
	FrameStatusAwaitingUserResponse = "awaiting_user_response"
	FrameStatusAwaitingPlanApproval = "awaiting_plan_approval"
	FrameStatusCompleted            = "completed"
	FrameStatusFailed               = "failed"
	FrameStatusCancelled            = "cancelled"
	FrameStatusSuccess              = "success"
	FrameStatusReplaced             = "replaced"
)

func canonicalFrameStatus(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "running", "executing", "in_progress", "in-progress", "queued", "pending", "created", FrameStatusProcessing:
		return FrameStatusProcessing, nil
	case "waiting_input", "needs_input", "needs-input", "awaiting_input", FrameStatusAwaitingUserResponse:
		return FrameStatusAwaitingUserResponse, nil
	case "waiting_approval", FrameStatusAwaitingPlanApproval:
		return FrameStatusAwaitingPlanApproval, nil
	case FrameStatusCompleted:
		return FrameStatusCompleted, nil
	case "error", FrameStatusFailed:
		return FrameStatusFailed, nil
	case "canceled", "stopped", "cancelling", "canceling", FrameStatusCancelled:
		return FrameStatusCancelled, nil
	case FrameStatusSuccess:
		return FrameStatusSuccess, nil
	case FrameStatusReplaced:
		return FrameStatusReplaced, nil
	default:
		return "", errors.New("frame status is invalid")
	}
}

func canonicalFrameStatusPointer(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	status, err := canonicalFrameStatus(*value)
	if err != nil {
		return nil, err
	}
	return &status, nil
}
