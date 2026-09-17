package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	transcript "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/realtime"
)

const (
	compatEventSubscriberBuffer  = 128
	compatDurableCatchUpPageSize = 64
)

type compatTranscriptRebasePlan struct {
	byFrame map[string]transcript.FrameRealtimeRebase
}

func (s *Server) transcriptRebasePlan(
	ctx context.Context, ownerID string, filter workspace.RealtimeEventFilter,
) (compatTranscriptRebasePlan, error) {
	plan := compatTranscriptRebasePlan{byFrame: map[string]transcript.FrameRealtimeRebase{}}
	if s == nil || s.transcriptStore == nil {
		return plan, nil
	}
	const pageSize = 1000
	after := filter.AfterSequence
	for {
		rebases, err := s.transcriptStore.ListActiveFrameRealtimeRebases(
			ctx, ownerID, filter.FrameID, after, pageSize,
		)
		if err != nil {
			return plan, err
		}
		for _, rebase := range rebases {
			if filter.ProjectID != "" && rebase.ProjectID != filter.ProjectID {
				continue
			}
			if filter.RootFrameID != "" && rebase.RootFrameID != filter.RootFrameID {
				continue
			}
			plan.byFrame[rebase.SessionID] = rebase
		}
		if len(rebases) < pageSize {
			break
		}
		next := rebases[len(rebases)-1].RebaseSequence
		if next <= after {
			return plan, transcript.ErrEventConflict
		}
		after = next
	}
	return plan, nil
}

func (plan compatTranscriptRebasePlan) suppresses(event workspace.RealtimeEvent) bool {
	rebase, found := plan.byFrame[event.FrameID]
	return found && event.UserID == rebase.OwnerID && event.RootFrameID == rebase.RootFrameID &&
		event.Sequence <= rebase.RealtimeHighWater
}

type compatEventHub struct {
	mu                sync.Mutex
	nextID            uint64
	subscribers       map[uint64]chan workspace.RealtimeEvent
	directSubscribers map[uint64]chan compatDirectEvent
}

type compatDirectEvent struct {
	UserID      string
	ProjectID   string
	RootFrameID string
	FrameID     string
	Message     map[string]any
}

func newCompatEventHub() *compatEventHub {
	return &compatEventHub{
		subscribers:       map[uint64]chan workspace.RealtimeEvent{},
		directSubscribers: map[uint64]chan compatDirectEvent{},
	}
}

func (h *compatEventHub) Subscribe() (<-chan workspace.RealtimeEvent, func()) {
	if h == nil {
		closed := make(chan workspace.RealtimeEvent)
		close(closed)
		return closed, func() {}
	}
	h.mu.Lock()
	id := h.nextID
	h.nextID++
	// Durable events are read back from SQLite by sequence. The in-memory
	// channel is only a wake signal, so one pending notification represents all
	// commits that have not been drained yet.
	channel := make(chan workspace.RealtimeEvent, 1)
	h.subscribers[id] = channel
	h.mu.Unlock()
	return channel, func() {
		h.mu.Lock()
		if current, found := h.subscribers[id]; found && current == channel {
			delete(h.subscribers, id)
			close(channel)
		}
		h.mu.Unlock()
	}
}

func (h *compatEventHub) SubscribeDirect() (<-chan compatDirectEvent, func()) {
	if h == nil {
		closed := make(chan compatDirectEvent)
		close(closed)
		return closed, func() {}
	}
	h.mu.Lock()
	id := h.nextID
	h.nextID++
	channel := make(chan compatDirectEvent, compatEventSubscriberBuffer)
	h.directSubscribers[id] = channel
	h.mu.Unlock()
	return channel, func() {
		h.mu.Lock()
		if current, found := h.directSubscribers[id]; found && current == channel {
			delete(h.directSubscribers, id)
			close(channel)
		}
		h.mu.Unlock()
	}
}

// Publish coalesces durable wake-ups. The payload never authorizes delivery or
// cursor advancement: subscribers always drain the SQLite sequence authority.
// A full channel therefore means a wake is already pending, not that an event
// was lost or that the subscriber is lagging.
func (h *compatEventHub) Publish(event workspace.RealtimeEvent) int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, channel := range h.subscribers {
		select {
		case channel <- event:
		default:
			// A durable drain is already scheduled for this subscriber.
		}
	}
	return 0
}

// PublishDirect sends a transient read-model projection without allocating a
// durable cursor. Its accepted replacement must still come from the canonical
// Transcript authority.
func (h *compatEventHub) PublishDirect(event compatDirectEvent) int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	disconnected := 0
	for id, channel := range h.directSubscribers {
		select {
		case channel <- event:
		default:
			delete(h.directSubscribers, id)
			close(channel)
			disconnected++
		}
	}
	return disconnected
}

func (s *Server) publishCompatEvent(input workspace.RealtimeEventInput) (workspace.RealtimeEvent, error) {
	if s == nil || s.workspaceStore == nil {
		return workspace.RealtimeEvent{}, errors.New("workspace runtime is not configured")
	}
	event, err := s.workspaceStore.AppendRealtimeEvent(input)
	if err != nil {
		return workspace.RealtimeEvent{}, err
	}
	if event.Kind != realtime.DeliveryNone {
		s.compatEvents.Publish(event)
	}
	return event, nil
}

func (s *Server) handleCompatEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	store, userID, filter, ok := s.compatEventRequest(w, r)
	if !ok {
		return
	}
	if err := s.prepareTranscriptWebReplay(r.Context(), userID); err != nil && !compatRealtimeReplayCanDegrade(err) {
		writeTranscriptReplayPreparationError(w, err)
		return
	}
	rebasePlan, err := s.transcriptRebasePlan(r.Context(), userID, filter)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "transcript realtime rebase failed"})
		return
	}
	through, err := store.LatestRealtimeEventSequence(filter)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	visible := make([]workspace.RealtimeEvent, 0, filter.Limit)
	next, _, err := replayCompatEvents(store, filter, through, func(event workspace.RealtimeEvent) bool {
		visible = append(visible, event)
		return len(visible) < filter.Limit
	}, rebasePlan.suppresses)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "realtime event replay failed"})
		return
	}
	if next < filter.AfterSequence {
		next = filter.AfterSequence
	}
	if len(visible) == 0 && next < through {
		// A page containing only suppressed legacy events still advances the
		// durable cursor so callers cannot be trapped behind the activation fence.
		for next < through {
			pageFilter := filter
			pageFilter.AfterSequence = next
			pageFilter.ThroughSequence = through
			pageFilter.Limit = 1000
			events, pageErr := store.ListRealtimeEvents(pageFilter)
			if pageErr != nil || len(events) == 0 {
				break
			}
			next = events[len(events)-1].Sequence
		}
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"ok": true, "user_id": userID, "events": visible, "next_sequence": next,
		"cursor_reset": filter.CursorReset,
	})
}

func compatRealtimeReplayCanDegrade(err error) bool {
	// Realtime is an owner-wide transport, while a poisoned or temporarily
	// unsettled projection belongs to one durable delivery stream. Refusing the
	// WebSocket would isolate every healthy conversation for that owner. Existing
	// realtime rows remain replayable and later outbox settlement publishes a new
	// durable wake, so keep the transport alive while preserving the failed
	// delivery for diagnosis and repair.
	return errors.Is(err, errTranscriptWebReplayIncomplete) || errors.Is(err, errTranscriptWebReplayPoisoned)
}

func (s *Server) handleCompatEventStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	store, userID, filter, ok := s.compatEventRequest(w, r)
	if !ok {
		return
	}
	if err := s.prepareTranscriptWebReplay(r.Context(), userID); err != nil && !compatRealtimeReplayCanDegrade(err) {
		writeTranscriptReplayPreparationError(w, err)
		return
	}
	rebasePlan, err := s.transcriptRebasePlan(r.Context(), userID, filter)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "transcript realtime rebase failed"})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "streaming is not supported"})
		return
	}
	channel, unsubscribe := s.compatEvents.Subscribe()
	defer unsubscribe()
	through, err := store.LatestRealtimeEventSequence(filter)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if through < filter.AfterSequence {
		through = filter.AfterSequence
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	wroteReplay := false
	if filter.CursorReset {
		if !writeCompatSSEControl(w, map[string]any{"type": "realtime.cursorReset", "cursor": filter.AfterSequence}) {
			return
		}
		wroteReplay = true
	}
	cursor, replayed, replayErr := replayCompatEvents(store, filter, through, func(event workspace.RealtimeEvent) bool {
		if !writeCompatSSE(w, event) {
			return false
		}
		wroteReplay = true
		return true
	}, rebasePlan.suppresses)
	if replayErr != nil {
		if !wroteReplay {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "realtime event replay failed"})
		}
		return
	}
	if !replayed {
		return
	}
	flusher.Flush()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	drainDurable := func() bool {
		next, complete, err := s.catchUpCompatEvents(r.Context(), store, userID, filter, cursor, func(event workspace.RealtimeEvent) bool {
			return writeCompatSSE(w, event)
		})
		if err != nil || !complete {
			return false
		}
		if next > cursor {
			cursor = next
			flusher.Flush()
		}
		return true
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case _, open := <-channel:
			if !open {
				return
			}
			// Hub notifications are wake-ups only. Durable storage is the sole
			// ordering authority, so a later notification cannot advance the
			// cursor past an event that committed without a hub publication.
			if !drainDurable() {
				return
			}
		}
	}
}

func (s *Server) handleCompatEventWebSocket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	store, userID, filter, ok := s.compatEventRequest(w, r)
	if !ok {
		return
	}
	// WebSocket establishment is an owner-wide transport operation. It must not
	// synchronously repair or drain every Transcript delivery for that owner:
	// one large, active, or poisoned conversation would otherwise exceed the
	// handshake deadline and isolate all healthy live work. The durable outbox
	// recovery loop materializes Transcript events and publishes its own wake;
	// this connection replays only already-materialized realtime rows.
	channel, unsubscribe := s.compatEvents.Subscribe()
	defer unsubscribe()
	directChannel, unsubscribeDirect := s.compatEvents.SubscribeDirect()
	defer unsubscribeDirect()
	through, err := store.LatestRealtimeEventSequence(filter)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if through < filter.AfterSequence {
		through = filter.AfterSequence
	}
	connection, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	connection.SetReadLimit(64 << 10)
	defer connection.Close(websocket.StatusNormalClosure, "closed")
	ctx, cancel := context.WithCancel(r.Context())
	direct := make(chan map[string]any, compatEventSubscriberBuffer)
	watches := newCompatFileWatchSet(ctx, s.fileRoot, direct)
	defer watches.Close()
	controls := make(chan map[string]any, 16)
	readDone := make(chan struct{})
	go readCompatEventWebSocket(ctx, connection, controls, readDone)
	defer func() {
		cancel()
		_ = connection.CloseNow()
		<-readDone
	}()

	if !writeCompatWebSocket(ctx, connection, map[string]any{"type": "connected", "cursor": filter.AfterSequence}) {
		return
	}
	if filter.CursorReset && !writeCompatWebSocket(ctx, connection, map[string]any{
		"type": "realtime.cursorReset", "cursor": filter.AfterSequence,
	}) {
		return
	}
	cursor := filter.AfterSequence
	durablePending := through > cursor
	handleControl := func(control map[string]any) bool {
		response := watches.Control(control)
		if response == nil && stringValue(control["type"]) != "file_watch" && stringValue(control["type"]) != "file_unwatch" {
			response = s.compatWebSocketControl(ctx, userID, control)
		}
		return response == nil || writeCompatWebSocket(ctx, connection, response)
	}
	drainDurablePage := func() (bool, bool) {
		next, complete, delivered, err := s.catchUpCompatEventPage(ctx, store, userID, filter, cursor, func(event workspace.RealtimeEvent) bool {
			return writeCompatWebSocket(ctx, connection, compatEventMessage(event))
		})
		if err != nil || !delivered {
			_ = connection.Close(websocket.StatusInternalError, "realtime catch-up failed")
			return false, false
		}
		if next > cursor {
			cursor = next
		}
		return true, complete
	}
	for {
		// Control messages are checked before every bounded durable page. A ping
		// therefore waits behind at most one small catch-up batch instead of an
		// owner-wide backlog.
		select {
		case control, open := <-controls:
			if !open || !handleControl(control) {
				return
			}
			continue
		default:
		}
		if durablePending {
			ok, complete := drainDurablePage()
			if !ok {
				return
			}
			durablePending = !complete
			continue
		}
		select {
		case <-ctx.Done():
			return
		case control, open := <-controls:
			if !open || !handleControl(control) {
				return
			}
		case message, open := <-direct:
			if !open {
				return
			}
			if !writeCompatWebSocket(ctx, connection, message) {
				return
			}
		case event, open := <-directChannel:
			if !open {
				return
			}
			if !compatDirectEventVisible(event, userID, filter) {
				continue
			}
			if !writeCompatWebSocket(ctx, connection, event.Message) {
				return
			}
		case _, open := <-channel:
			if !open {
				return
			}
			// The hub only wakes a durable drain. It never authorizes a cursor
			// advance or direct delivery of its in-memory payload.
			durablePending = true
		}
	}
}

func compatDirectEventVisible(event compatDirectEvent, userID string, filter workspace.RealtimeEventFilter) bool {
	if event.UserID == "" || event.UserID != userID {
		return false
	}
	if filter.ProjectID != "" && event.ProjectID != filter.ProjectID {
		return false
	}
	if filter.RootFrameID != "" && event.RootFrameID != filter.RootFrameID {
		return false
	}
	if filter.FrameID != "" && event.FrameID != filter.FrameID {
		return false
	}
	eventType := strings.TrimSpace(stringValue(event.Message["type"]))
	return eventType != "" && (filter.Type == "" || filter.Type == eventType)
}

func writeTranscriptReplayPreparationError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, errTranscriptWebReplayIncomplete) {
		status = http.StatusServiceUnavailable
		w.Header().Set("Retry-After", "1")
	}
	writeWorkspaceJSON(w, status, map[string]any{"ok": false, "error": "transcript replay preparation failed"})
}

func (s *Server) catchUpCompatEvents(
	ctx context.Context, store *workspace.Store, userID string, filter workspace.RealtimeEventFilter,
	cursor int64, visit func(workspace.RealtimeEvent) bool,
) (int64, bool, error) {
	for {
		next, complete, delivered, err := s.catchUpCompatEventPage(ctx, store, userID, filter, cursor, visit)
		if err != nil || !delivered {
			return next, false, err
		}
		if !complete {
			if next <= cursor {
				return next, false, transcript.ErrEventConflict
			}
			cursor = next
			continue
		}
		return next, true, nil
	}
}

func (s *Server) catchUpCompatEventPage(
	ctx context.Context, store *workspace.Store, userID string, filter workspace.RealtimeEventFilter,
	cursor int64, visit func(workspace.RealtimeEvent) bool,
) (int64, bool, bool, error) {
	// Initial connection setup repairs and drains transcript projections before
	// taking its durable cursor. A live wake must only read already-materialized
	// realtime rows: synchronously draining every transcript for the owner here
	// lets one active or poisoned conversation close otherwise healthy sockets
	// and stalls unrelated conversation/project invalidations. Transcript outbox
	// materialization publishes its own durable wake after the row exists.
	catchupFilter := filter
	catchupFilter.AfterSequence = cursor
	through, err := store.LatestRealtimeEventSequence(catchupFilter)
	if err != nil {
		return cursor, false, false, err
	}
	if through <= cursor {
		return cursor, true, true, nil
	}
	plan, err := s.transcriptRebasePlan(ctx, userID, catchupFilter)
	if err != nil {
		return cursor, false, false, err
	}
	pageFilter := catchupFilter
	pageFilter.ThroughSequence = through
	pageFilter.Limit = compatDurableCatchUpPageSize
	events, err := store.ListRealtimeEvents(pageFilter)
	if err != nil {
		return cursor, false, false, err
	}
	for _, event := range events {
		cursor = event.Sequence
		if event.Kind == realtime.DeliveryNone || plan.suppresses(event) {
			continue
		}
		if !visit(event) {
			return cursor, false, false, nil
		}
	}
	if len(events) == 0 {
		return cursor, cursor >= through, true, nil
	}
	return cursor, cursor >= through || len(events) < pageFilter.Limit, true, nil
}

func (s *Server) compatEventRequest(w http.ResponseWriter, r *http.Request) (*workspace.Store, string, workspace.RealtimeEventFilter, bool) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return nil, "", workspace.RealtimeEventFilter{}, false
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return nil, "", workspace.RealtimeEventFilter{}, false
	}
	cursor, err := compatEventCursor(r)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return nil, "", workspace.RealtimeEventFilter{}, false
	}
	eventType := strings.TrimSpace(r.URL.Query().Get("type"))
	if eventType != "" {
		if _, found := realtime.LookupEvent(eventType); !found {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "unknown realtime event type"})
			return nil, "", workspace.RealtimeEventFilter{}, false
		}
	}
	filter := workspace.RealtimeEventFilter{
		UserID: userID, IncludeGlobal: true,
		ProjectID:   strings.TrimSpace(r.URL.Query().Get("project_id")),
		RootFrameID: strings.TrimSpace(r.URL.Query().Get("root_frame_id")),
		FrameID:     strings.TrimSpace(r.URL.Query().Get("frame_id")), Type: eventType,
		Limit: workspaceEventLimit(r),
	}
	owned, err := compatEventScopeOwned(store, userID, filter)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return nil, "", workspace.RealtimeEventFilter{}, false
	}
	if !owned {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "realtime event scope not found"})
		return nil, "", workspace.RealtimeEventFilter{}, false
	}
	latest, err := store.LatestRealtimeEventSequence(filter)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return nil, "", workspace.RealtimeEventFilter{}, false
	}
	filter.AfterSequence = cursor.Sequence
	if cursor.Latest {
		filter.AfterSequence = latest
	} else if filter.AfterSequence > latest {
		filter.AfterSequence = latest
		filter.CursorReset = true
	}
	return store, userID, filter, true
}

type compatEventCursorValue struct {
	Latest   bool
	Sequence int64
}

func compatEventCursor(r *http.Request) (compatEventCursorValue, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("after_sequence"))
	if raw == "" {
		raw = strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	}
	if raw == "" {
		return compatEventCursorValue{}, nil
	}
	if strings.EqualFold(raw, "latest") {
		return compatEventCursorValue{Latest: true}, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return compatEventCursorValue{}, fmt.Errorf("invalid realtime event cursor %q", raw)
	}
	return compatEventCursorValue{Sequence: value}, nil
}

func compatEventScopeOwned(store *workspace.Store, userID string, filter workspace.RealtimeEventFilter) (bool, error) {
	missing := false
	if filter.ProjectID != "" {
		ownerID, found, err := store.ProjectOwnerID(filter.ProjectID)
		if err != nil {
			return false, err
		}
		if found && ownerID != userID {
			return false, nil
		}
		missing = missing || !found
	}
	if filter.RootFrameID != "" {
		root, found, err := store.GetFrameRealtimeContext(filter.RootFrameID)
		if err != nil {
			return false, err
		}
		if !found {
			missing = true
		} else if root.UserID != userID || root.Frame.ID != root.Frame.RootFrameID || root.Frame.ParentFrameID != "" ||
			(filter.ProjectID != "" && root.Frame.ProjectID != filter.ProjectID) {
			return false, nil
		}
	}
	if filter.FrameID != "" {
		frame, found, err := store.GetFrameRealtimeContext(filter.FrameID)
		if err != nil {
			return false, err
		}
		if !found {
			missing = true
		} else if frame.UserID != userID || (filter.ProjectID != "" && frame.Frame.ProjectID != filter.ProjectID) ||
			(filter.RootFrameID != "" && frame.Frame.RootFrameID != filter.RootFrameID) {
			return false, nil
		}
	}
	if !missing {
		return true, nil
	}
	historyFilter := filter
	historyFilter.IncludeGlobal = false
	historyFilter.Type = ""
	historyFilter.AfterSequence = 0
	historyFilter.ThroughSequence = 0
	historyFilter.Limit = 1
	events, err := store.ListRealtimeEvents(historyFilter)
	if err != nil {
		return false, err
	}
	return len(events) > 0, nil
}

func replayCompatEvents(
	store *workspace.Store, filter workspace.RealtimeEventFilter, through int64,
	visit func(workspace.RealtimeEvent) bool, skips ...func(workspace.RealtimeEvent) bool,
) (int64, bool, error) {
	cursor := filter.AfterSequence
	for cursor < through {
		pageFilter := filter
		pageFilter.AfterSequence = cursor
		pageFilter.ThroughSequence = through
		pageFilter.Limit = 1000
		events, err := store.ListRealtimeEvents(pageFilter)
		if err != nil {
			return cursor, false, err
		}
		for _, event := range events {
			cursor = event.Sequence
			if event.Kind == realtime.DeliveryNone {
				continue
			}
			if len(skips) > 0 && skips[0] != nil && skips[0](event) {
				continue
			}
			if !visit(event) {
				return cursor, false, nil
			}
		}
		if len(events) == 0 || len(events) < pageFilter.Limit {
			break
		}
	}
	return cursor, cursor >= through, nil
}

func compatEventVisible(event workspace.RealtimeEvent, userID string, filter workspace.RealtimeEventFilter) bool {
	if (event.UserID == "" && !filter.IncludeGlobal) || (event.UserID != "" && event.UserID != userID) {
		return false
	}
	if filter.ProjectID != "" && event.ProjectID != filter.ProjectID {
		return false
	}
	if filter.RootFrameID != "" && event.RootFrameID != filter.RootFrameID {
		return false
	}
	if filter.FrameID != "" && event.FrameID != filter.FrameID {
		return false
	}
	return filter.Type == "" || event.Type == filter.Type
}

func writeCompatSSE(w http.ResponseWriter, event workspace.RealtimeEvent) bool {
	raw, err := json.Marshal(event)
	if err != nil {
		return false
	}
	_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, event.Type, raw)
	return err == nil
}

func writeCompatSSEControl(w http.ResponseWriter, message map[string]any) bool {
	raw, err := json.Marshal(message)
	if err != nil {
		return false
	}
	eventType := stringValue(message["type"])
	if eventType == "realtime.cursorReset" {
		if cursor, ok := compatSSEControlCursor(message["cursor"]); ok {
			_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", cursor, eventType, raw)
			return err == nil
		}
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, raw)
	return err == nil
}

func compatSSEControlCursor(value any) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return typed, typed >= 0
	case int:
		return int64(typed), typed >= 0
	case float64:
		if typed >= 0 && typed <= math.MaxInt64 && typed == math.Trunc(typed) {
			return int64(typed), true
		}
	}
	return 0, false
}

func compatEventMessage(event workspace.RealtimeEvent) map[string]any {
	message := make(map[string]any, len(event.Payload)+10)
	for key, value := range event.Payload {
		message[key] = value
	}
	message["type"] = event.Type
	message["_event_id"] = event.ID
	message["_sequence"] = event.Sequence
	message["_kind"] = event.Kind
	message["_created_at"] = event.CreatedAt
	message["invalidations"] = event.Invalidations
	if event.ProjectID != "" {
		message["project_id"] = event.ProjectID
	}
	if event.RootFrameID != "" {
		message["root_frame_id"] = event.RootFrameID
	}
	if event.FrameID != "" {
		message["frame_id"] = event.FrameID
	}
	return message
}

func readCompatEventWebSocket(ctx context.Context, connection *websocket.Conn, controls chan<- map[string]any, done chan<- struct{}) {
	defer close(done)
	defer close(controls)
	for {
		messageType, raw, err := connection.Read(ctx)
		if err != nil {
			return
		}

		if messageType != websocket.MessageText {
			continue
		}
		var message map[string]any
		if err := json.Unmarshal(raw, &message); err != nil {
			message = map[string]any{"type": "__parse_error"}
		}
		select {
		case controls <- message:
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) compatWebSocketControl(ctx context.Context, userID string, message map[string]any) map[string]any {
	switch stringValue(message["type"]) {
	case "ping":
		return map[string]any{"type": "pong"}
	case "kernel_user_exec", "kernel_user_interrupt":
		return s.compatKernelTerminalAck(ctx, userID, message)
	case "__parse_error":
		return map[string]any{"type": "error", "code": "PARSE_ERROR"}
	default:
		return map[string]any{"type": "error", "code": "UNKNOWN_TYPE"}
	}
}
func writeCompatWebSocket(ctx context.Context, connection *websocket.Conn, value any) bool {
	writeContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return writeWebSocketJSON(writeContext, connection, value) == nil
}
