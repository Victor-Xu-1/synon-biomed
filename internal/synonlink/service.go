package synonlink

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Client struct {
	ID               string    `json:"id"`
	UserID           string    `json:"userId"`
	DeviceID         string    `json:"deviceId,omitempty"`
	Name             string    `json:"name"`
	Kind             string    `json:"kind"`
	Version          string    `json:"version"`
	Browser          string    `json:"browser,omitempty"`
	Platform         string    `json:"platform,omitempty"`
	Capabilities     []string  `json:"capabilities"`
	SupportedActions []string  `json:"supportedActions,omitempty"`
	ConnectedAt      time.Time `json:"connectedAt"`
	LastSeenAt       time.Time `json:"lastSeenAt"`
	Status           string    `json:"status"`
}

type Command struct {
	ID        string         `json:"commandId"`
	Type      string         `json:"type"`
	Action    string         `json:"action"`
	Name      string         `json:"name"`
	Payload   map[string]any `json:"payload"`
	CreatedAt time.Time      `json:"createdAt"`
}

type CommandResult struct {
	CommandID string         `json:"commandId"`
	OK        bool           `json:"ok"`
	Value     map[string]any `json:"value"`
	Error     string         `json:"error,omitempty"`
}

type TaskLog struct {
	CommandID   string         `json:"commandId"`
	UserID      string         `json:"userId"`
	ClientID    string         `json:"clientId"`
	Action      string         `json:"action"`
	OK          bool           `json:"ok"`
	Status      string         `json:"status"`
	Error       string         `json:"error,omitempty"`
	StartedAt   time.Time      `json:"startedAt"`
	CompletedAt time.Time      `json:"completedAt"`
	Result      map[string]any `json:"result,omitempty"`
}

type RunningTask struct {
	CommandID string         `json:"commandId"`
	UserID    string         `json:"userId"`
	ClientID  string         `json:"clientId"`
	Action    string         `json:"action"`
	Status    string         `json:"status"`
	Summary   string         `json:"summary,omitempty"`
	Progress  map[string]any `json:"progress,omitempty"`
	StartedAt time.Time      `json:"startedAt"`
}

type AccessRequest struct {
	ID             string    `json:"id"`
	UserID         string    `json:"userId"`
	ClientID       string    `json:"clientId"`
	DeviceID       string    `json:"deviceId,omitempty"`
	Scope          string    `json:"scope"`
	Reason         string    `json:"reason,omitempty"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"createdAt"`
	DecidedAt      time.Time `json:"decidedAt,omitempty"`
	DecisionReason string    `json:"decisionReason,omitempty"`
}

type AccessEvent struct {
	Type      string        `json:"type"`
	Event     string        `json:"event"`
	Request   AccessRequest `json:"request"`
	CreatedAt time.Time     `json:"createdAt"`
}

type RememberedApprovalDecision struct {
	UserID    string    `json:"userId"`
	ClientID  string    `json:"clientId"`
	DeviceID  string    `json:"deviceId,omitempty"`
	Action    string    `json:"action"`
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

type commandProgress struct {
	Status   string
	Summary  string
	Progress map[string]any
}

type Service struct {
	mu                     sync.Mutex
	clients                map[string]Client
	pending                map[string][]Command
	waiters                map[string]chan CommandResult
	deliveries             map[string]chan<- Command
	accessEvents           map[string]chan<- AccessEvent
	progress               map[string]commandProgress
	logs                   []TaskLog
	logPath                string
	policyDefaultsProvider func() PolicyDefaults
	rememberedProvider     func(userID, clientID, action string) (RememberedApprovalDecision, bool)
	rememberedRecorder     func(RememberedApprovalDecision) error
	access                 map[string]AccessRequest
	nextID                 int64
	nextAccessID           int64
	now                    func() time.Time
}

func NewService() *Service {
	return &Service{
		clients:      make(map[string]Client),
		pending:      make(map[string][]Command),
		waiters:      make(map[string]chan CommandResult),
		deliveries:   make(map[string]chan<- Command),
		accessEvents: make(map[string]chan<- AccessEvent),
		progress:     make(map[string]commandProgress),
		access:       make(map[string]AccessRequest),
		now:          time.Now,
	}
}

func (s *Service) SetTaskLogPath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logPath = path
}

func (s *Service) SetPolicyDefaultsProvider(provider func() PolicyDefaults) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policyDefaultsProvider = provider
}

func (s *Service) SetRememberedApprovalStore(provider func(userID, clientID, action string) (RememberedApprovalDecision, bool), recorder func(RememberedApprovalDecision) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rememberedProvider = provider
	s.rememberedRecorder = recorder
}

func (s *Service) PolicyDefaults() PolicyDefaults {
	provider := s.policyDefaultsProviderSnapshot()
	if provider == nil {
		return PolicyDefaults{}
	}
	return provider()
}

func (s *Service) policyDefaultsProviderSnapshot() func() PolicyDefaults {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.policyDefaultsProvider
}

func (s *Service) rememberedApprovalStoreSnapshot() (func(userID, clientID, action string) (RememberedApprovalDecision, bool), func(RememberedApprovalDecision) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rememberedProvider, s.rememberedRecorder
}

func (s *Service) Register(client Client) error {
	if client.ID == "" {
		return errors.New("client id is required")
	}
	if client.UserID == "" {
		return errors.New("user id is required")
	}
	if client.ConnectedAt.IsZero() {
		client.ConnectedAt = s.now().UTC()
	}
	if client.LastSeenAt.IsZero() {
		client.LastSeenAt = s.now().UTC()
	}
	if client.Kind == "" {
		client.Kind = "browser"
	}
	if client.Kind != "browser" {
		return fmt.Errorf("unsupported Synon Link client kind %s; only browser clients are accepted", client.Kind)
	}
	if client.Status == "" {
		client.Status = "online"
	}
	client.Capabilities = filterAllowed(client.Capabilities, allowedCapabilities())
	client.SupportedActions = filterAllowed(client.SupportedActions, allowedActions())
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clients[client.ID] = client
	return nil
}

func (s *Service) Attach(client Client, delivery chan<- Command) error {
	if err := s.Register(client); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deliveries[client.ID] = delivery
	return nil
}

func (s *Service) AttachAccessEvents(userID, clientID string, delivery chan<- AccessEvent) error {
	if clientID == "" {
		return errors.New("client id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	client, ok := s.clients[clientID]
	if !ok || client.UserID != userID {
		return fmt.Errorf("client %s is not connected for user %s", clientID, userID)
	}
	s.accessEvents[clientID] = delivery
	return nil
}

func (s *Service) ListClients(userID string) []Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	clients := make([]Client, 0)
	for _, client := range s.clients {
		if client.UserID == userID {
			clients = append(clients, client)
		}
	}
	return clients
}

func (s *Service) ListAllClients() []Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	clients := make([]Client, 0, len(s.clients))
	for _, client := range s.clients {
		clients = append(clients, client)
	}
	return clients
}

func (s *Service) SendCommand(ctx context.Context, userID, clientID, action string, payload map[string]any) (Command, <-chan CommandResult, error) {
	if action == "" {
		return Command{}, nil, errors.New("action is required")
	}
	defaults := s.PolicyDefaults()
	rememberedProvider, rememberedRecorder := s.rememberedApprovalStoreSnapshot()
	s.mu.Lock()
	client, ok := s.clients[clientID]
	if !ok || client.UserID != userID {
		s.mu.Unlock()
		return Command{}, nil, fmt.Errorf("client %s is not connected for user %s", clientID, userID)
	}
	if client.Status != "" && client.Status != "online" {
		s.mu.Unlock()
		return Command{}, nil, fmt.Errorf("client %s is offline (status: %s)", clientID, client.Status)
	}
	effectivePayload := payloadWithRememberedApproval(payload, rememberedProvider, defaults, userID, clientID, action)
	if err := ValidateCommandPolicyWithDefaults(client, action, effectivePayload, defaults); err != nil {
		s.mu.Unlock()
		return Command{}, nil, err
	}
	s.nextID++
	command := Command{
		ID:        fmt.Sprintf("cmd-%d", s.nextID),
		Type:      "command",
		Action:    action,
		Name:      action,
		Payload:   payload,
		CreatedAt: s.now().UTC(),
	}
	resultCh := make(chan CommandResult, 1)
	s.pending[clientID] = append(s.pending[clientID], command)
	s.waiters[command.ID] = resultCh
	delivery := s.deliveries[clientID]
	s.mu.Unlock()
	if decision, ok := rememberedDecisionFromApprovedPayload(defaults, client, action, payload); ok && rememberedRecorder != nil {
		if err := rememberedRecorder(decision); err != nil {
			s.cancelCommand(clientID, command.ID)
			return Command{}, nil, fmt.Errorf("remember approval decision: %w", err)
		}
	}

	if delivery != nil {
		select {
		case delivery <- command:
		case <-ctx.Done():
			s.cancelCommand(clientID, command.ID)
		}
	}

	go func() {
		<-ctx.Done()
		s.cancelCommand(clientID, command.ID)
	}()

	return command, resultCh, nil
}

func payloadWithRememberedApproval(payload map[string]any, provider func(userID, clientID, action string) (RememberedApprovalDecision, bool), defaults PolicyDefaults, userID, clientID, action string) map[string]any {
	if provider == nil || !defaults.RememberDecisions || normalizePolicyMode(defaults.Mode) == "deny" {
		return payload
	}
	if approved, _ := commandApprovalState(action, payload); approved {
		return payload
	}
	decision, ok := provider(userID, clientID, action)
	if !ok || decision.UserID != userID || decision.ClientID != clientID || decision.Action != action {
		return payload
	}
	copied := clonePayload(payload)
	copied["approval"] = map[string]any{
		"approved": true,
		"reason":   firstNonEmpty(decision.Reason, "remembered approval decision"),
	}
	copied["rememberedApproval"] = true
	return copied
}

func rememberedDecisionFromApprovedPayload(defaults PolicyDefaults, client Client, action string, payload map[string]any) (RememberedApprovalDecision, bool) {
	if !defaults.RememberDecisions {
		return RememberedApprovalDecision{}, false
	}
	approved, _ := commandApprovalState(action, payload)
	if !approved {
		return RememberedApprovalDecision{}, false
	}
	reason := approvalReasonValue(payload)
	if defaults.RequireReason && reason == "" {
		return RememberedApprovalDecision{}, false
	}
	return RememberedApprovalDecision{
		UserID:    client.UserID,
		ClientID:  client.ID,
		DeviceID:  client.DeviceID,
		Action:    action,
		Reason:    reason,
		CreatedAt: time.Now().UTC(),
	}, true
}

func clonePayload(payload map[string]any) map[string]any {
	copied := make(map[string]any, len(payload)+2)
	for key, value := range payload {
		copied[key] = value
	}
	return copied
}

func RememberedApprovalKey(userID, clientID, action string) string {
	return strings.Join([]string{userID, clientID, action}, "|")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (s *Service) PendingCommands(clientID string) []Command {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := s.pending[clientID]
	copied := make([]Command, len(pending))
	copy(copied, pending)
	return copied
}

func (s *Service) CompleteCommand(clientID, commandID string, value map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	waiter, ok := s.waiters[commandID]
	if !ok {
		return fmt.Errorf("command %s is not pending", commandID)
	}
	client := s.clients[clientID]
	var completed Command
	for _, command := range s.pending[clientID] {
		if command.ID == commandID {
			completed = command
			break
		}
	}
	s.removePendingLocked(clientID, commandID)
	delete(s.waiters, commandID)
	delete(s.progress, commandID)
	s.appendLogLocked(client, completed, true, "completed", "", value)
	waiter <- CommandResult{CommandID: commandID, OK: true, Value: value}
	close(waiter)
	return nil
}

func (s *Service) RecordProgress(clientID, commandID, status, summary string, progress map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.waiters[commandID]; !ok {
		return fmt.Errorf("command %s is not pending", commandID)
	}
	client := s.clients[clientID]
	client.LastSeenAt = s.now().UTC()
	s.clients[clientID] = client
	s.progress[commandID] = commandProgress{
		Status:   status,
		Summary:  summary,
		Progress: progress,
	}
	return nil
}

func (s *Service) ListRunning(userID string) []RunningTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	running := make([]RunningTask, 0)
	for clientID, commands := range s.pending {
		client := s.clients[clientID]
		if client.UserID != userID {
			continue
		}
		for _, command := range commands {
			progress := s.progress[command.ID]
			running = append(running, RunningTask{
				CommandID: command.ID,
				UserID:    client.UserID,
				ClientID:  clientID,
				Action:    command.Action,
				Status:    progress.Status,
				Summary:   progress.Summary,
				Progress:  progress.Progress,
				StartedAt: command.CreatedAt,
			})
		}
	}
	return running
}

func (s *Service) ListAllRunning() []RunningTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	running := make([]RunningTask, 0)
	for clientID, commands := range s.pending {
		client := s.clients[clientID]
		for _, command := range commands {
			progress := s.progress[command.ID]
			running = append(running, RunningTask{
				CommandID: command.ID,
				UserID:    client.UserID,
				ClientID:  clientID,
				Action:    command.Action,
				Status:    progress.Status,
				Summary:   progress.Summary,
				Progress:  progress.Progress,
				StartedAt: command.CreatedAt,
			})
		}
	}
	return running
}

func (s *Service) RequestAccess(userID, clientID, scope, reason string) (AccessRequest, error) {
	if userID == "" {
		return AccessRequest{}, errors.New("user id is required")
	}
	if clientID == "" {
		return AccessRequest{}, errors.New("client id is required")
	}
	if scope == "" {
		return AccessRequest{}, errors.New("access scope is required")
	}
	s.mu.Lock()
	client, ok := s.clients[clientID]
	if !ok || client.UserID != userID {
		s.mu.Unlock()
		return AccessRequest{}, fmt.Errorf("client %s is not connected for user %s", clientID, userID)
	}
	s.nextAccessID++
	request := AccessRequest{
		ID:        fmt.Sprintf("access-%d", s.nextAccessID),
		UserID:    userID,
		ClientID:  clientID,
		DeviceID:  client.DeviceID,
		Scope:     scope,
		Reason:    reason,
		Status:    "pending",
		CreatedAt: s.now().UTC(),
	}
	s.access[request.ID] = request
	delivery := s.accessEvents[clientID]
	s.mu.Unlock()
	s.deliverAccessEvent(delivery, "access_request_created", request)
	return request, nil
}

func (s *Service) ListAccessRequests(userID, status string) []AccessRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	requests := make([]AccessRequest, 0)
	for _, request := range s.access {
		if request.UserID != userID {
			continue
		}
		if status != "" && request.Status != status {
			continue
		}
		requests = append(requests, request)
	}
	return requests
}

func (s *Service) DecideAccess(userID, requestID string, approved bool, reason string) (AccessRequest, error) {
	if userID == "" || requestID == "" {
		return AccessRequest{}, errors.New("user id and request id are required")
	}
	s.mu.Lock()
	request, ok := s.access[requestID]
	if !ok || request.UserID != userID {
		s.mu.Unlock()
		return AccessRequest{}, fmt.Errorf("access request %s not found", requestID)
	}
	if request.Status != "pending" {
		s.mu.Unlock()
		return AccessRequest{}, fmt.Errorf("access request %s is already %s", requestID, request.Status)
	}
	if approved {
		request.Status = "granted"
	} else {
		request.Status = "denied"
	}
	request.DecidedAt = s.now().UTC()
	request.DecisionReason = reason
	s.access[request.ID] = request
	delivery := s.accessEvents[request.ClientID]
	s.mu.Unlock()
	s.deliverAccessEvent(delivery, "access_request_decided", request)
	return request, nil
}

func (s *Service) RevokeAccess(userID, requestID, reason string) (AccessRequest, error) {
	if userID == "" || requestID == "" {
		return AccessRequest{}, errors.New("user id and request id are required")
	}
	s.mu.Lock()
	request, ok := s.access[requestID]
	if !ok || request.UserID != userID {
		s.mu.Unlock()
		return AccessRequest{}, fmt.Errorf("access request %s not found", requestID)
	}
	if request.Status == "revoked" {
		s.mu.Unlock()
		return AccessRequest{}, fmt.Errorf("access request %s is already revoked", requestID)
	}
	request.Status = "revoked"
	request.DecidedAt = s.now().UTC()
	request.DecisionReason = reason
	s.access[request.ID] = request
	delivery := s.accessEvents[request.ClientID]
	s.mu.Unlock()
	s.deliverAccessEvent(delivery, "access_request_revoked", request)
	return request, nil
}

func (s *Service) deliverAccessEvent(delivery chan<- AccessEvent, event string, request AccessRequest) {
	if delivery == nil {
		return
	}
	accessEvent := AccessEvent{
		Type:      "access_event",
		Event:     event,
		Request:   request,
		CreatedAt: s.now().UTC(),
	}
	select {
	case delivery <- accessEvent:
	default:
	}
}

func (s *Service) Disconnect(clientID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.deliveries, clientID)
	delete(s.accessEvents, clientID)
	_, ok := s.clients[clientID]
	if !ok {
		return
	}
	for _, command := range s.pending[clientID] {
		delete(s.waiters, command.ID)
		delete(s.progress, command.ID)
	}
	delete(s.pending, clientID)
	delete(s.clients, clientID)
}

func (s *Service) RevokeDevice(userID, deviceID, reason string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	revoked := false
	for clientID, client := range s.clients {
		if client.UserID != userID || client.DeviceID != deviceID {
			continue
		}
		revoked = true
		for _, command := range s.pending[clientID] {
			if waiter := s.waiters[command.ID]; waiter != nil {
				waiter <- CommandResult{CommandID: command.ID, OK: false, Error: reason}
				close(waiter)
			}
			delete(s.waiters, command.ID)
			delete(s.progress, command.ID)
			s.appendLogLocked(client, command, false, "revoked", reason, nil)
		}
		delete(s.pending, clientID)
		delete(s.deliveries, clientID)
		client.Status = "offline"
		client.LastSeenAt = s.now().UTC()
		s.clients[clientID] = client
	}
	return revoked
}

func (s *Service) ListTaskLogs(userID string, limit int) []TaskLog {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 50
	}
	if s.logPath != "" {
		if logs, err := s.readTaskLogsLocked(userID, limit); err == nil {
			return logs
		}
	}
	logs := make([]TaskLog, 0)
	for index := len(s.logs) - 1; index >= 0 && len(logs) < limit; index-- {
		if s.logs[index].UserID == userID {
			logs = append(logs, s.logs[index])
		}
	}
	return logs
}

func (s *Service) cancelCommand(clientID, commandID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.waiters[commandID]; !ok {
		return
	}
	s.removePendingLocked(clientID, commandID)
	delete(s.waiters, commandID)
	delete(s.progress, commandID)
}

func (s *Service) appendLogLocked(client Client, command Command, ok bool, status string, errMessage string, result map[string]any) {
	entry := TaskLog{
		CommandID:   command.ID,
		UserID:      client.UserID,
		ClientID:    client.ID,
		Action:      command.Action,
		OK:          ok,
		Status:      status,
		Error:       errMessage,
		StartedAt:   command.CreatedAt,
		CompletedAt: s.now().UTC(),
		Result:      sanitizeResultForTaskLog(result),
	}
	s.logs = append(s.logs, entry)
	if len(s.logs) > 1000 {
		s.logs = append([]TaskLog(nil), s.logs[len(s.logs)-1000:]...)
	}
	if s.logPath != "" {
		_ = s.appendTaskLogFileLocked(entry)
	}
}

func sanitizeResultForTaskLog(result map[string]any) map[string]any {
	if result == nil {
		return nil
	}
	sanitized := make(map[string]any, len(result))
	for key, value := range result {
		if key == "imageBase64" {
			if text, ok := value.(string); ok {
				sanitized[key] = fmt.Sprintf("[redacted base64 image: %d chars]", len(text))
			} else {
				sanitized[key] = "[redacted image]"
			}
			continue
		}
		sanitized[key] = value
	}
	return sanitized
}

func (s *Service) appendTaskLogFileLocked(entry TaskLog) error {
	if err := os.MkdirAll(filepath.Dir(s.logPath), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(s.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	_, err = file.Write(append(data, '\n'))
	return err
}

func (s *Service) readTaskLogsLocked(userID string, limit int) ([]TaskLog, error) {
	file, err := os.Open(s.logPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []TaskLog{}, nil
		}
		return nil, err
	}
	defer file.Close()
	all := make([]TaskLog, 0)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var entry TaskLog
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry.UserID == userID {
			all = append(all, entry)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	logs := make([]TaskLog, 0, limit)
	for index := len(all) - 1; index >= 0 && len(logs) < limit; index-- {
		logs = append(logs, all[index])
	}
	return logs, nil
}

func (s *Service) removePendingLocked(clientID, commandID string) {
	pending := s.pending[clientID]
	next := pending[:0]
	for _, command := range pending {
		if command.ID != commandID {
			next = append(next, command)
		}
	}
	if len(next) == 0 {
		delete(s.pending, clientID)
		return
	}
	s.pending[clientID] = next
}

func allowedCapabilities() map[string]struct{} {
	values := []string{
		"tabs", "tabGroups", "scripting", "activeTab", "captureVisibleTab", "synonLink", "taskLoop",
		"browserSearch", "humanPageRead", "llmMediaCapture", "mediaManifest", "noLocalOcr", "scrollPage", "refreshTab",
		"downloads", "downloadOpen", "downloadDiscovery", "visualClick", "visualElementMap", "pageWait",
		"tabCleanup", "localFiles", "localFileOperations", "localFileAdvanced", "fileSystemAccess",
		"permissionReset", "bookmarks", "personalization", "loggedInPageRead",
	}
	return set(values)
}

func allowedActions() map[string]struct{} {
	return set(ActionNames())
}

func set(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func filterAllowed(values []string, allowed map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, ok := allowed[value]; !ok {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
