package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

const maxAgentKernelNotificationWait = 30 * time.Minute

func (s *Server) recordAgentKernelBackgroundStart(access workspace.KernelFrameAccess, started kernelruntime.ExecutionStarted) error {
	if s == nil || s.workspaceStore == nil {
		return errors.New("kernel background notification store is unavailable")
	}
	event, err := s.workspaceStore.RecordBackgroundKernelExecutionStarted(context.Background(), access, workspace.BackgroundKernelExecution{
		ExecID: started.ExecID, ToolID: started.ToolUseID, ToolName: kernelPublicToolName(started),
		FrameID: access.Frame.ID, RootFrameID: access.Frame.RootFrameID,
		FrameIncarnationID:     access.Frame.IncarnationID,
		RootFrameIncarnationID: access.RootFrameIncarnationID,
		StartedAt:              started.StartedAt,
	})
	if err != nil {
		return err
	}
	if err := s.publishWorkspaceEvent(event); err != nil {
		log.Printf("publish durable background kernel start %s: %v", event.ID, err)
	}
	return nil
}

func agentKernelCellResultPayload(started kernelruntime.ExecutionStarted, result map[string]any, outputLimitBytes int64) map[string]any {
	encoded, err := json.Marshal(result)
	if err != nil {
		encoded = []byte(`{"ok":false,"error":"kernel result could not be encoded"}`)
	}
	if outputLimitBytes <= 0 {
		outputLimitBytes = defaultSessionRunnerOutputLimitBytes
	}
	payload, projectionErr := workspace.BuildKernelSettlementNotificationPayload(
		started.ExecID, started.ToolUseID, encoded, outputLimitBytes,
	)
	if projectionErr == nil {
		return payload
	}
	return map[string]any{
		"exec_id": started.ExecID, "tool_id": started.ToolUseID,
		"status": "errored", "output": `{"ok":false,"error":"kernel result projection failed"}`,
	}
}

func truncateUTF8ByRunes(value string, limit int) string {
	if limit < 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func truncateUTF8ByBytes(value string, limit int64) string {
	if limit <= 0 {
		return ""
	}
	if int64(len(value)) <= limit {
		return value
	}
	cut := int(limit)
	for cut > 0 && !utf8.ValidString(value[:cut]) {
		cut--
	}
	return value[:cut]
}

func kernelPublicToolName(started kernelruntime.ExecutionStarted) string {
	switch strings.ToLower(strings.TrimSpace(started.KernelKind)) {
	case "operon":
		return "repl"
	case "r":
		return "r"
	default:
		return "python"
	}
}

func (s *Server) executeAgentKernelNotificationWait(ctx context.Context, identity *agentKernelContext, claimToken string, input map[string]any) (map[string]any, error) {
	timeout, err := agentKernelNotificationTimeoutDuration(input)
	if err != nil {
		return nil, err
	}
	return s.waitForAgentKernelNotification(ctx, identity, timeout, claimToken)
}

func agentKernelNotificationTimeoutDuration(input map[string]any) (time.Duration, error) {
	value, found := input["timeout_seconds"]
	if !found {
		return 30 * time.Second, nil
	}
	var seconds float64
	switch typed := value.(type) {
	case float64:
		seconds = typed
	case float32:
		seconds = float64(typed)
	case int:
		seconds = float64(typed)
	case int64:
		seconds = float64(typed)
	default:
		return 0, errors.New("timeout_seconds must be a number")
	}
	if math.IsNaN(seconds) || seconds <= 0 {
		return 0, nil
	}
	if math.IsInf(seconds, 1) || seconds >= maxAgentKernelNotificationWait.Seconds() {
		return maxAgentKernelNotificationWait, nil
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

func (s *Server) waitForAgentKernelNotification(ctx context.Context, identity *agentKernelContext, timeout time.Duration, claimToken string) (map[string]any, error) {
	if identity == nil {
		return nil, errors.New("kernel execution identity is unavailable")
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		access, err := s.validateKernelHostIdentity(ctx, identity.access)
		if err != nil {
			return nil, err
		}
		if _, err := s.workspaceStore.FlushUndeliveredKernelChildLandings(ctx, access.Frame.ID, access.Frame.RootFrameID, access.UserID); err != nil {
			return nil, errors.New("kernel notification recovery failed")
		}
		if err := s.reconcileLostAgentKernelExecutions(ctx, access); err != nil {
			return nil, errors.New("kernel notification recovery failed")
		}
		notifications, err := s.workspaceStore.ClaimUnreadNotifications(ctx, access.Frame.ID, access.Frame.RootFrameID, access.UserID, claimToken, 1000)
		if err != nil {
			return nil, errors.New("kernel notification read failed")
		}
		if len(notifications) > 0 {
			return s.projectAgentKernelNotifications(ctx, access, notifications)
		}
		pending, hasPending, err := s.agentKernelPendingWork(ctx, access)
		if err != nil {
			return nil, errors.New("kernel pending work could not be inspected")
		}
		if !hasPending {
			// A background settlement atomically replaces its pending outbox row
			// with an unread notification. agentKernelPendingWork intentionally
			// combines several durable projections, so its first reads can precede
			// that commit while its final pending-settlement read follows it. Claim
			// once more before declaring the task idle; after that commit the
			// notification is durable and this read must observe it.
			notifications, err = s.workspaceStore.ClaimUnreadNotifications(
				ctx, access.Frame.ID, access.Frame.RootFrameID, access.UserID, claimToken, 1000,
			)
			if err != nil {
				return nil, errors.New("kernel notification read failed")
			}
			if len(notifications) > 0 {
				return s.projectAgentKernelNotifications(ctx, access, notifications)
			}
			return map[string]any{
				"status":            "completed",
				"num_notifications": 0,
				"notifications":     []any{},
				"system_hint":       "All delegations have completed and their notifications have been consumed.",
			}, nil
		}
		if agentKernelHasOnlyUncollectedLandings(pending) {
			return agentKernelUncollectedResult(pending), nil
		}
		if timeout == 0 {
			return agentKernelNotificationTimeoutResult(pending), nil
		}
		var wake <-chan workspace.FrameEvent
		unsubscribe := func() {}
		if s.workspaceEvents != nil {
			wake, unsubscribe = s.workspaceEvents.Subscribe()
		}
		// Acquire the durable outbox generation before the second read. A
		// settlement commit closes this channel even when realtime fanout is
		// delayed or unavailable, so notification visibility never depends on
		// an ephemeral websocket projection.
		var outboxWake <-chan struct{}
		if s.workspaceStore != nil {
			outboxWake = s.workspaceStore.OutboxWake()
		}
		// The durable recheck closes the interval between the first read and
		// subscription. The hub is only a latency optimization; the periodic
		// scan remains authoritative when publication is unavailable.
		notifications, err = s.workspaceStore.ClaimUnreadNotifications(ctx, access.Frame.ID, access.Frame.RootFrameID, access.UserID, claimToken, 1000)
		if err != nil {
			unsubscribe()
			return nil, errors.New("kernel notification read failed")
		}
		if len(notifications) > 0 {
			unsubscribe()
			return s.projectAgentKernelNotifications(ctx, access, notifications)
		}
		select {
		case <-ctx.Done():
			unsubscribe()
			return nil, ctx.Err()
		case <-deadline.C:
			unsubscribe()
			// The timer and a durable commit may become ready together. Re-read
			// once before reporting timeout so a committed result cannot be
			// hidden behind the stale pre-wait pending snapshot.
			notifications, err = s.workspaceStore.ClaimUnreadNotifications(
				ctx, access.Frame.ID, access.Frame.RootFrameID, access.UserID, claimToken, 1000,
			)
			if err != nil {
				return nil, errors.New("kernel notification read failed")
			}
			if len(notifications) > 0 {
				return s.projectAgentKernelNotifications(ctx, access, notifications)
			}
			return agentKernelNotificationTimeoutResult(pending), nil
		case <-wake:
			unsubscribe()
		case <-outboxWake:
			unsubscribe()
		case <-ticker.C:
			unsubscribe()
		}
	}
}

func agentKernelNotificationTimeoutResult(pending map[string]any) map[string]any {
	result := map[string]any{
		"status": "timeout", "num_notifications": 0, "notifications": []any{},
		"running_children": pending["children"], "pending_work": pending,
	}
	if executions, ok := pending["executions"].([]any); ok && len(executions) > 0 {
		result["system_hint"] = "This session cannot complete while the work listed in pending_work is running. If an execution looks stale — it predates a session restart, or has been running far longer than its task warrants — clear it with host.stop_child('<exec_id>') in a python cell (fresh: true if the primary kernel is busy): a live cell is interrupted and returns partial output; a stale entry is simply cleared so the session can complete."
	}
	return result
}

func (s *Server) reconcileLostAgentKernelExecutions(ctx context.Context, access workspace.KernelFrameAccess) error {
	persisted, err := s.workspaceStore.ListPendingBackgroundKernelExecutions(ctx, access)
	if err != nil {
		return err
	}
	active := map[string]bool{}
	if s.kernelManager != nil {
		for _, execution := range s.kernelManager.ListExecStreams(access.Frame.ID) {
			active[execution.ExecID] = true
		}
	}
	for _, execution := range persisted {
		if active[execution.ExecID] {
			continue
		}
		event, marked, err := s.workspaceStore.MarkBackgroundKernelExecutionLostIfPending(ctx, access, execution)
		if err != nil {
			return err
		}
		if marked {
			if err := s.publishWorkspaceEvent(event); err != nil {
				log.Printf("publish lost background kernel event %s: %v", event.ID, err)
			}
		}
	}
	return nil
}

func (s *Server) projectAgentKernelNotifications(ctx context.Context, access workspace.KernelFrameAccess, notifications []workspace.Notification) (map[string]any, error) {
	projected := make([]any, 0, len(notifications))
	completed := []any{}
	questions := []string{}
	for _, notification := range notifications {
		value := map[string]any{
			"id":                 notification.ID,
			"notification_type":  notification.NotificationType,
			"sender_frame_id":    notification.SenderFrameID,
			"recipient_frame_id": notification.RecipientFrameID,
			"payload":            copyMapAny(notification.Payload),
			"created_at":         notification.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
		projected = append(projected, value)
		if notification.NotificationType == "cell_result" {
			if execID := strings.TrimSpace(stringValue(notification.Payload["exec_id"])); execID != "" {
				completed = append(completed, execID)
			}
		}
		if notification.NotificationType == "child_message" && stringValue(notification.Payload["kind"]) == "question" {
			name := firstNonEmpty(stringValue(notification.Payload["name"]), stringValue(notification.Payload["agent_name"]), "agent")
			sender := firstNonEmpty(stringValue(notification.Payload["sender_frame_id"]), notification.SenderFrameID)
			questions = append(questions, fmt.Sprintf("%s (%s)", name, sender))
		}
	}
	children, err := s.workspaceStore.ListKernelActiveChildren(ctx, access.Frame.ID, access.UserID)
	if err != nil {
		return nil, errors.New("kernel running children could not be inspected")
	}
	result := map[string]any{
		"status": "received", "notifications": projected, "num_notifications": len(projected),
		"running_children": projectAgentKernelPendingChildren(children, time.Now().UTC()),
	}
	if len(completed) > 0 {
		result["cells_completed"] = completed
	}
	if len(questions) > 0 {
		result["system_hint"] = fmt.Sprintf("%d agent question(s) — answer each with host.send_message('<frame_id>', '<answer>'): %s.", len(questions), strings.Join(questions, ", "))
	}
	return result, nil
}

func agentKernelNotificationClaimCount(raw string) (int, error) {
	var result struct {
		Status           string            `json:"status"`
		NumNotifications int               `json:"num_notifications"`
		Notifications    []json.RawMessage `json:"notifications"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return 0, errors.New("kernel notification result is invalid")
	}
	switch result.Status {
	case "received":
		if result.NumNotifications <= 0 || result.NumNotifications > 1000 || len(result.Notifications) != result.NumNotifications {
			return 0, errors.New("kernel notification result manifest is invalid")
		}
		return result.NumNotifications, nil
	case "timeout", "error", "uncollected_results":
		if result.NumNotifications != 0 || len(result.Notifications) != 0 {
			return 0, errors.New("kernel notification result manifest is invalid")
		}
		return 0, nil
	default:
		return 0, errors.New("kernel notification result status is invalid")
	}
}

func (s *Server) ackAgentKernelNotificationClaim(ctx context.Context, frameID, claimToken string, expected int) error {
	if s == nil || s.workspaceStore == nil {
		return errors.New("kernel notification store is unavailable")
	}
	access, found, err := s.workspaceStore.GetKernelFrameAccessContext(ctx, strings.TrimSpace(frameID))
	if err != nil || !found {
		return errors.New("kernel notification authority is unavailable")
	}
	_, peerNotificationIDs, err := s.workspaceStore.AckClaimedNotificationsDetailed(ctx, access.Frame.ID, access.Frame.RootFrameID, access.UserID, claimToken, expected)
	if err != nil {
		return errors.New("kernel notification acknowledgement failed")
	}
	s.releaseKernelPeerReservations(peerNotificationIDs...)
	return nil
}

func (s *Server) agentKernelPendingWork(ctx context.Context, access workspace.KernelFrameAccess) (map[string]any, bool, error) {
	children, err := s.workspaceStore.ListKernelActiveChildren(ctx, access.Frame.ID, access.UserID)
	if err != nil {
		return nil, false, err
	}
	executions, err := s.workspaceStore.ListPendingBackgroundKernelExecutions(ctx, access)
	if err != nil {
		return nil, false, err
	}
	unread, err := s.workspaceStore.CountUnreadNotifications(ctx, access.Frame.ID, access.Frame.RootFrameID, access.UserID)
	if err != nil {
		return nil, false, err
	}
	landings, err := s.workspaceStore.ListKernelChildLandings(ctx, access.Frame.ID, access.Frame.RootFrameID, access.UserID, 100)
	if err != nil {
		return nil, false, err
	}
	undelivered, err := s.workspaceStore.CountUndeliveredKernelChildLandings(ctx, access.Frame.ID, access.Frame.RootFrameID, access.UserID)
	if err != nil {
		return nil, false, err
	}
	pendingSettlements, err := s.workspaceStore.CountPendingBackgroundKernelSettlements(ctx, access)
	if err != nil {
		return nil, false, err
	}
	jobs, err := s.workspaceStore.ListComputeJobs(access.UserID, access.Frame.ProjectID)
	if err != nil {
		return nil, false, err
	}
	activeJobs := []workspace.ComputeJob{}
	for _, job := range jobs {
		if job.FrameID != nil && *job.FrameID == access.Frame.ID && activeComputeJobState(job.State) {
			activeJobs = append(activeJobs, job)
		}
	}
	sort.Slice(executions, func(i, j int) bool {
		if executions[i].StartedAt.Equal(executions[j].StartedAt) {
			return executions[i].ExecID < executions[j].ExecID
		}
		return executions[i].StartedAt.Before(executions[j].StartedAt)
	})
	now := time.Now().UTC()
	pending := map[string]any{
		"children":             projectAgentKernelPendingChildren(children, now),
		"executions":           projectAgentKernelPendingExecutions(executions, now),
		"unread_notifications": unread, "active_compute_jobs": len(activeJobs),
		"undelivered_rows": undelivered, "uncollected_landings": projectAgentKernelLandings(landings),
		"pending_settlements": pendingSettlements,
	}
	return pending, len(children) > 0 || len(executions) > 0 || unread > 0 || len(activeJobs) > 0 ||
		undelivered > 0 || pendingSettlements > 0 || len(landings) > 0, nil
}

func projectAgentKernelLandings(landings []workspace.Notification) []any {
	result := make([]any, 0, len(landings))
	for _, landing := range landings {
		result = append(result, map[string]any{
			"frame_id":   firstNonEmpty(stringValue(landing.Payload["frame_id"]), landing.SenderFrameID),
			"agent_name": firstNonEmpty(stringValue(landing.Payload["agent_name"]), "unknown"),
			"status":     firstNonEmpty(stringValue(landing.Payload["status"]), "completed"),
		})
	}
	return result
}

func agentKernelHasOnlyUncollectedLandings(pending map[string]any) bool {
	landings, _ := pending["uncollected_landings"].([]any)
	children, _ := pending["children"].([]any)
	executions, _ := pending["executions"].([]any)
	return len(landings) > 0 && len(children) == 0 && len(executions) == 0 &&
		int(numberValue(pending["unread_notifications"])) == 0 &&
		int(numberValue(pending["active_compute_jobs"])) == 0 &&
		int(numberValue(pending["undelivered_rows"])) == 0 &&
		int(numberValue(pending["pending_settlements"])) == 0
}

func agentKernelUncollectedResult(pending map[string]any) map[string]any {
	landings, _ := pending["uncollected_landings"].([]any)
	ids := make([]string, 0, len(landings))
	for _, raw := range landings {
		landing, _ := raw.(map[string]any)
		if id := strings.TrimSpace(stringValue(landing["frame_id"])); id != "" {
			ids = append(ids, id)
		}
	}
	quoted := "'" + strings.Join(ids, "', '") + "'"
	return map[string]any{
		"status": "uncollected_results", "num_notifications": 0, "notifications": []any{},
		"uncollected": landings,
		"system_hint": fmt.Sprintf("%d delegated child(ren) finished with results you have not retrieved. Call host.collect([%s]) in a python cell to retrieve them (or stop_child to discard a result you no longer need). The session cannot complete until every finished delegation is collected or explicitly stopped.", len(landings), quoted),
	}
}

func projectAgentKernelPendingChildren(children []workspace.KernelSupervisedChild, now time.Time) []any {
	result := make([]any, 0, len(children))
	for _, child := range children {
		result = append(result, map[string]any{
			"frame_id": child.FrameID, "agent_name": child.AgentName, "status": child.Status,
			"started_at":  child.StartedAt.UTC().Format(time.RFC3339Nano),
			"age_seconds": nonNegativeAgeSeconds(now, child.StartedAt),
		})
	}
	return result
}

func projectAgentKernelPendingExecutions(executions []workspace.BackgroundKernelExecution, now time.Time) []any {
	result := make([]any, 0, len(executions))
	for _, execution := range executions {
		result = append(result, map[string]any{
			"exec_id": execution.ExecID, "tool_name": execution.ToolName,
			"started_at":  execution.StartedAt.UTC().Format(time.RFC3339Nano),
			"age_seconds": nonNegativeAgeSeconds(now, execution.StartedAt),
		})
	}
	return result
}

func nonNegativeAgeSeconds(now, startedAt time.Time) any {
	if startedAt.IsZero() {
		return nil
	}
	seconds := int64(math.Round(now.Sub(startedAt).Seconds()))
	if seconds < 0 {
		return 0
	}
	return seconds
}

func activeComputeJobState(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pending", "staging", "queued", "running", "harvesting":
		return true
	default:
		return false
	}
}
