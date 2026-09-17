package server

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	defaultMCPAppRegistrationLease = 45 * time.Second
	defaultMCPAppCallTimeout       = 2 * time.Minute
	mcpAppRequestQueueSize         = 16
	maxMCPAppTools                 = 128
	maxMCPAppArtifactIDBytes       = 512
)

var errMCPAppBrokerClosed = errors.New("MCP app broker is closed")

type mcpAppToolDescriptor struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
}

type mcpAppRegistrationInput struct {
	UserID       string
	ProjectID    string
	RootFrameID  string
	FrameID      string
	ServerID     string
	ServerName   string
	ServerSource string
	ArtifactID   string
	Tools        []mcpAppToolDescriptor
}

type mcpAppRegistrationView struct {
	ID         string    `json:"registration_id"`
	ArtifactID string    `json:"artifact_id"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type mcpAppToolView struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
	ArtifactID  string         `json:"artifact_id"`
}

type mcpAppCallRequest struct {
	RequestID  string         `json:"request_id"`
	ServerID   string         `json:"server_id"`
	ServerName string         `json:"server_name"`
	ArtifactID string         `json:"artifact_id"`
	Tool       string         `json:"tool"`
	Arguments  map[string]any `json:"arguments"`
}

type mcpAppCallResult struct {
	Content           []mcpAppContent `json:"content,omitempty"`
	StructuredContent any             `json:"structured_content,omitempty"`
	IsError           bool            `json:"is_error,omitempty"`
}

type mcpAppContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type mcpAppRegistration struct {
	id           string
	userID       string
	projectID    string
	rootFrameID  string
	frameID      string
	serverID     string
	serverName   string
	serverSource string
	artifactID   string
	tools        map[string]mcpAppToolDescriptor
	expiresAt    time.Time
	requests     chan mcpAppCallRequest
	done         chan struct{}
}

type mcpAppPendingCall struct {
	registrationID string
	result         chan mcpAppCallResult
}

type mcpAppBroker struct {
	mu            sync.Mutex
	registrations map[string]*mcpAppRegistration
	pending       map[string]mcpAppPendingCall
	lease         time.Duration
	callTimeout   time.Duration
	now           func() time.Time
	closed        bool
}

func newMCPAppBroker() *mcpAppBroker {
	return newMCPAppBrokerWithDurations(defaultMCPAppRegistrationLease, defaultMCPAppCallTimeout)
}

func newMCPAppBrokerWithDurations(lease, callTimeout time.Duration) *mcpAppBroker {
	if lease <= 0 {
		lease = defaultMCPAppRegistrationLease
	}
	if callTimeout <= 0 {
		callTimeout = defaultMCPAppCallTimeout
	}
	return &mcpAppBroker{
		registrations: make(map[string]*mcpAppRegistration),
		pending:       make(map[string]mcpAppPendingCall),
		lease:         lease,
		callTimeout:   callTimeout,
		now:           time.Now,
	}
}

func (b *mcpAppBroker) Register(input mcpAppRegistrationInput) (mcpAppRegistrationView, error) {
	if b == nil {
		return mcpAppRegistrationView{}, errMCPAppBrokerClosed
	}
	if err := validateMCPAppRegistration(input); err != nil {
		return mcpAppRegistrationView{}, err
	}
	tools := make(map[string]mcpAppToolDescriptor, len(input.Tools))
	for _, tool := range input.Tools {
		if _, exists := tools[tool.Name]; exists {
			return mcpAppRegistrationView{}, fmt.Errorf("duplicate MCP app tool %q", tool.Name)
		}
		tools[tool.Name] = tool
	}
	now := b.now().UTC()
	registration := &mcpAppRegistration{
		id: uuid.NewString(), userID: input.UserID, projectID: input.ProjectID,
		rootFrameID: input.RootFrameID, frameID: input.FrameID,
		serverID: input.ServerID, serverName: input.ServerName,
		serverSource: input.ServerSource, artifactID: input.ArtifactID,
		tools: tools, expiresAt: now.Add(b.lease),
		requests: make(chan mcpAppCallRequest, mcpAppRequestQueueSize),
		done:     make(chan struct{}),
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return mcpAppRegistrationView{}, errMCPAppBrokerClosed
	}
	b.pruneExpiredLocked(now)
	b.registrations[registration.id] = registration
	return registration.view(), nil
}

func (b *mcpAppBroker) Poll(ctx context.Context, userID, registrationID string) (mcpAppCallRequest, bool, error) {
	if b == nil {
		return mcpAppCallRequest{}, false, errMCPAppBrokerClosed
	}
	registration, err := b.touchRegistration(userID, registrationID)
	if err != nil {
		return mcpAppCallRequest{}, false, err
	}
	select {
	case request := <-registration.requests:
		if _, err := b.touchRegistration(userID, registrationID); err != nil {
			return mcpAppCallRequest{}, false, err
		}
		return request, true, nil
	case <-registration.done:
		return mcpAppCallRequest{}, false, errors.New("MCP app registration is no longer active")
	case <-ctx.Done():
		return mcpAppCallRequest{}, false, ctx.Err()
	}
}

func (b *mcpAppBroker) ListTools(userID, rootFrameID, serverID string) ([]mcpAppToolView, error) {
	if b == nil {
		return nil, errMCPAppBrokerClosed
	}
	now := b.now().UTC()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, errMCPAppBrokerClosed
	}
	b.pruneExpiredLocked(now)
	tools := make([]mcpAppToolView, 0)
	for _, registration := range b.registrations {
		if registration.userID != userID || registration.rootFrameID != rootFrameID || registration.serverID != serverID {
			continue
		}
		for _, tool := range registration.tools {
			tools = append(tools, mcpAppToolView{
				Name: tool.Name, Description: tool.Description,
				InputSchema: tool.InputSchema, ArtifactID: registration.artifactID,
			})
		}
	}
	sort.Slice(tools, func(i, j int) bool {
		if tools[i].ArtifactID != tools[j].ArtifactID {
			return tools[i].ArtifactID < tools[j].ArtifactID
		}
		return tools[i].Name < tools[j].Name
	})
	return tools, nil
}

func (b *mcpAppBroker) Call(
	ctx context.Context,
	userID, rootFrameID, serverID, artifactID, toolName string,
	arguments map[string]any,
) (mcpAppCallResult, error) {
	if b == nil {
		return mcpAppCallResult{}, errMCPAppBrokerClosed
	}
	now := b.now().UTC()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return mcpAppCallResult{}, errMCPAppBrokerClosed
	}
	b.pruneExpiredLocked(now)
	registration, tool, err := b.selectRegistrationLocked(userID, rootFrameID, serverID, artifactID, toolName)
	if err != nil {
		b.mu.Unlock()
		return mcpAppCallResult{}, err
	}
	if err := validateKernelMCPInput(tool.InputSchema, arguments); err != nil {
		b.mu.Unlock()
		return mcpAppCallResult{}, fmt.Errorf("%s: unexpected argument or invalid value: %w", toolName, err)
	}
	request := mcpAppCallRequest{
		RequestID: uuid.NewString(), ServerID: registration.serverID,
		ServerName: registration.serverName, ArtifactID: registration.artifactID,
		Tool: toolName, Arguments: arguments,
	}
	pending := mcpAppPendingCall{registrationID: registration.id, result: make(chan mcpAppCallResult, 1)}
	b.pending[request.RequestID] = pending
	b.mu.Unlock()

	select {
	case registration.requests <- request:
	case <-registration.done:
		b.removePending(request.RequestID, pending)
		return mcpAppCallResult{}, errors.New("MCP app registration disconnected before accepting the call")
	default:
		b.removePending(request.RequestID, pending)
		return mcpAppCallResult{}, errors.New("MCP app registration is not consuming calls")
	}

	timer := time.NewTimer(b.callTimeout)
	defer timer.Stop()
	defer b.removePending(request.RequestID, pending)
	select {
	case result := <-pending.result:
		return result, nil
	case <-registration.done:
		return mcpAppCallResult{}, errors.New("MCP app registration disconnected while handling the call")
	case <-ctx.Done():
		return mcpAppCallResult{}, ctx.Err()
	case <-timer.C:
		return mcpAppCallResult{}, errors.New("MCP app tool call timed out")
	}
}

func (b *mcpAppBroker) Resolve(userID, registrationID, requestID string, result mcpAppCallResult) error {
	if b == nil {
		return errMCPAppBrokerClosed
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return errMCPAppBrokerClosed
	}
	registration, found := b.registrations[registrationID]
	if !found || registration.userID != userID {
		b.mu.Unlock()
		return errors.New("MCP app registration not found")
	}
	pending, found := b.pending[requestID]
	if !found || pending.registrationID != registrationID {
		b.mu.Unlock()
		return errors.New("MCP app request not found")
	}
	delete(b.pending, requestID)
	b.mu.Unlock()
	pending.result <- result
	return nil
}

func (b *mcpAppBroker) Unregister(userID, registrationID string) error {
	if b == nil {
		return errMCPAppBrokerClosed
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	registration, found := b.registrations[registrationID]
	if !found || registration.userID != userID {
		return errors.New("MCP app registration not found")
	}
	b.removeRegistrationLocked(registration)
	return nil
}

func (b *mcpAppBroker) Close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for _, registration := range b.registrations {
		close(registration.done)
	}
	b.registrations = make(map[string]*mcpAppRegistration)
	b.pending = make(map[string]mcpAppPendingCall)
}

func (b *mcpAppBroker) touchRegistration(userID, registrationID string) (*mcpAppRegistration, error) {
	now := b.now().UTC()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, errMCPAppBrokerClosed
	}
	b.pruneExpiredLocked(now)
	registration, found := b.registrations[registrationID]
	if !found || registration.userID != userID {
		return nil, errors.New("MCP app registration not found")
	}
	registration.expiresAt = now.Add(b.lease)
	return registration, nil
}

func (b *mcpAppBroker) selectRegistrationLocked(
	userID, rootFrameID, serverID, artifactID, toolName string,
) (*mcpAppRegistration, mcpAppToolDescriptor, error) {
	var matches []*mcpAppRegistration
	for _, registration := range b.registrations {
		if registration.userID != userID || registration.rootFrameID != rootFrameID || registration.serverID != serverID {
			continue
		}
		if artifactID != "" && registration.artifactID != artifactID {
			continue
		}
		if _, found := registration.tools[toolName]; found {
			matches = append(matches, registration)
		}
	}
	if len(matches) == 0 {
		return nil, mcpAppToolDescriptor{}, fmt.Errorf("%s: unknown tool or no matching live tile", toolName)
	}
	if artifactID == "" && len(matches) != 1 {
		return nil, mcpAppToolDescriptor{}, fmt.Errorf("%s: multiple live tiles match; artifact_id is required", toolName)
	}
	registration := matches[0]
	return registration, registration.tools[toolName], nil
}

func (b *mcpAppBroker) removePending(requestID string, pending mcpAppPendingCall) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if current, found := b.pending[requestID]; found && current.registrationID == pending.registrationID {
		delete(b.pending, requestID)
	}
}

func (b *mcpAppBroker) pruneExpiredLocked(now time.Time) {
	for _, registration := range b.registrations {
		if !registration.expiresAt.After(now) {
			b.removeRegistrationLocked(registration)
		}
	}
}

func (b *mcpAppBroker) removeRegistrationLocked(registration *mcpAppRegistration) {
	if current, found := b.registrations[registration.id]; !found || current != registration {
		return
	}
	delete(b.registrations, registration.id)
	close(registration.done)
	for requestID, pending := range b.pending {
		if pending.registrationID == registration.id {
			delete(b.pending, requestID)
		}
	}
}

func (r *mcpAppRegistration) view() mcpAppRegistrationView {
	return mcpAppRegistrationView{ID: r.id, ArtifactID: r.artifactID, ExpiresAt: r.expiresAt}
}

func validateMCPAppRegistration(input mcpAppRegistrationInput) error {
	for name, value := range map[string]string{
		"user id": input.UserID, "project id": input.ProjectID,
		"root frame id": input.RootFrameID, "frame id": input.FrameID,
		"server id": input.ServerID, "server name": input.ServerName,
		"server source": input.ServerSource, "artifact id": input.ArtifactID,
	} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("MCP app %s must be a non-empty normalized string", name)
		}
	}
	if len([]byte(input.ArtifactID)) > maxMCPAppArtifactIDBytes {
		return errors.New("MCP app artifact id is too long")
	}
	if len(input.Tools) == 0 || len(input.Tools) > maxMCPAppTools {
		return fmt.Errorf("MCP app must register between 1 and %d tools", maxMCPAppTools)
	}
	for _, tool := range input.Tools {
		if !validMCPAppToolName(tool.Name) {
			return fmt.Errorf("MCP app tool name %q is invalid", tool.Name)
		}
		if tool.InputSchema == nil {
			return fmt.Errorf("MCP app tool %q input schema is required", tool.Name)
		}
		if schemaType, _ := tool.InputSchema["type"].(string); schemaType != "object" {
			return fmt.Errorf("MCP app tool %q input schema must describe an object", tool.Name)
		}
		if _, err := compileKernelDraft7Schema(tool.InputSchema); err != nil {
			return fmt.Errorf("MCP app tool %q input schema is invalid: %w", tool.Name, err)
		}
	}
	return nil
}

func validMCPAppToolName(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len([]byte(value)) <= 128 &&
		!strings.ContainsAny(value, "\x00\r\n")
}
