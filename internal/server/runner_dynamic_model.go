package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"

	"synon-go/internal/agentruntime"
	sessionstore "synon-go/internal/persistence/sessions"
	"synon-go/internal/providers"
)

// sessionRunnerDynamicModelClient resolves the conversation's current model
// before every provider call. A switch never cancels or recreates the runner;
// an in-flight request finishes on its original provider and the next call
// observes the new selection.
type sessionRunnerDynamicModelClient struct {
	server           *Server
	sessionID        string
	session          sessionstore.Session
	resolutionInput  providers.ResolutionInput
	audit            func(providers.AuditRecord)
	fallback         agentruntime.ModelClient
	fallbackModel    string
	role             string
	initial          sessionRunnerResolvedModelClient
	initialReady     bool
	initialSelection string
	initialRevision  int64

	mu                  sync.RWMutex
	lastSuccessfulModel string
}

type sessionRunnerResolvedModelClient struct {
	client            agentruntime.ModelClient
	identity          string
	model             string
	selection         string
	selectionRevision int64
}

type sessionRunnerModelCallError struct {
	cause             error
	selection         string
	selectionRevision int64
	model             string
	identity          string
}

func (failure *sessionRunnerModelCallError) Error() string {
	if failure == nil || failure.cause == nil {
		return "model provider call failed"
	}
	return failure.cause.Error()
}

func (failure *sessionRunnerModelCallError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

func wrapSessionRunnerModelCallError(
	err error,
	snapshot sessionConversationModelSnapshot,
	model string,
	identity string,
) error {
	if err == nil {
		return nil
	}
	return &sessionRunnerModelCallError{
		cause:             err,
		selection:         strings.TrimSpace(snapshot.Selection),
		selectionRevision: snapshot.Revision,
		model:             strings.TrimSpace(model),
		identity:          strings.TrimSpace(identity),
	}
}

func wrapResolvedSessionRunnerModelCallError(err error, resolved sessionRunnerResolvedModelClient) error {
	return wrapSessionRunnerModelCallError(err, sessionConversationModelSnapshot{
		Selection: resolved.selection,
		Revision:  resolved.selectionRevision,
	}, resolved.model, resolved.identity)
}

func (s *Server) sessionRunnerModelSelectionAdvancedSinceFailure(sessionID string, err error) bool {
	var failure *sessionRunnerModelCallError
	if s == nil || err == nil || !errors.As(err, &failure) || failure == nil {
		return false
	}
	current, snapshotErr := s.sessionConversationModelSnapshot(sessionID)
	if snapshotErr != nil {
		return false
	}
	if current.Revision > failure.selectionRevision {
		return true
	}
	return current.Revision == failure.selectionRevision &&
		strings.TrimSpace(current.Selection) != strings.TrimSpace(failure.selection)
}

func (client *sessionRunnerDynamicModelClient) takeInitial(ctx context.Context) (sessionRunnerResolvedModelClient, bool, error) {
	if client == nil {
		return sessionRunnerResolvedModelClient{}, false, nil
	}
	client.mu.Lock()
	if !client.initialReady {
		client.mu.Unlock()
		return sessionRunnerResolvedModelClient{}, false, nil
	}
	client.initialReady = false
	initial := client.initial
	selection := client.initialSelection
	revision := client.initialRevision
	client.mu.Unlock()
	if client.server == nil {
		return initial, true, nil
	}
	sessionID := strings.TrimSpace(client.sessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(client.session.ID)
	}
	if sessionID == "" {
		return initial, true, nil
	}
	snapshot, err := client.server.sessionConversationModelSnapshotWithContext(ctx, sessionID)
	if err != nil {
		return sessionRunnerResolvedModelClient{}, false, err
	}
	if snapshot.Revision != revision || strings.TrimSpace(snapshot.Selection) != strings.TrimSpace(selection) {
		return sessionRunnerResolvedModelClient{}, false, nil
	}
	initial.selection = snapshot.Selection
	initial.selectionRevision = snapshot.Revision
	return initial, true, nil
}

func (client *sessionRunnerDynamicModelClient) resolve(ctx context.Context) (sessionRunnerResolvedModelClient, error) {
	if client == nil {
		return sessionRunnerResolvedModelClient{}, errors.New("dynamic session model runtime is unavailable")
	}
	if initial, ready, err := client.takeInitial(ctx); err != nil {
		return sessionRunnerResolvedModelClient{}, err
	} else if ready {
		return initial, nil
	}
	if client.server == nil {
		return sessionRunnerResolvedModelClient{}, errors.New("dynamic session model runtime is unavailable")
	}
	session := client.session
	sessionID := strings.TrimSpace(client.sessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(session.ID)
	}
	if client.server.sessionStore != nil && sessionID != "" {
		refreshed, found, err := client.server.sessionStore.Get(sessionID)
		if err != nil {
			return sessionRunnerResolvedModelClient{}, err
		}
		if found {
			session = refreshed
		}
	}
	if strings.TrimSpace(session.ID) == "" {
		return sessionRunnerResolvedModelClient{}, errors.New("dynamic session model has no task authority")
	}
	roleFallbackModel := ""
	roleFallbackSource := ""
	if strings.EqualFold(strings.TrimSpace(client.role), "reviewer") {
		roleFallbackModel = sessionRunnerReviewerModel(session)
		roleFallbackSource = "workspace-reviewer-model"
	}
	resolutionInput := client.resolutionInput
	resolutionInput.Context = ctx
	profile, _, snapshot, err := client.server.resolveSessionModelProfileSnapshotWithFallback(
		session, resolutionInput, roleFallbackModel, roleFallbackSource,
	)
	if err != nil {
		return sessionRunnerResolvedModelClient{
			selection: snapshot.Selection, selectionRevision: snapshot.Revision,
		}, err
	}
	if profile == nil {
		if client.fallback == nil {
			return sessionRunnerResolvedModelClient{
				selection: snapshot.Selection, selectionRevision: snapshot.Revision,
			}, errors.New("dynamic session model has no configured provider")
		}
		model := strings.TrimSpace(client.fallbackModel)
		return sessionRunnerResolvedModelClient{
			client: client.fallback, identity: "fallback\x00" + model, model: model,
			selection: snapshot.Selection, selectionRevision: snapshot.Revision,
		}, nil
	}
	delegate, err := providers.NewRuntimeModelClient(*profile, client.server.httpClient, client.audit)
	if err != nil {
		return sessionRunnerResolvedModelClient{
			model: profile.Model, selection: snapshot.Selection, selectionRevision: snapshot.Revision,
		}, err
	}
	delegate = newSessionOutputBudgetClient(delegate, client.server.runtimeStore, *profile, session.ID, client.role)
	return sessionRunnerResolvedModelClient{
		client: delegate,
		identity: strings.Join([]string{
			strings.TrimSpace(profile.Provider.ID),
			strings.TrimSpace(profile.Provider.Endpoint),
			strings.TrimSpace(profile.Model),
		}, "\x00"),
		model: strings.TrimSpace(profile.Model), selection: snapshot.Selection, selectionRevision: snapshot.Revision,
	}, nil
}

func (client *sessionRunnerDynamicModelClient) recordSuccess(resolved sessionRunnerResolvedModelClient) {
	if client == nil || strings.TrimSpace(resolved.model) == "" {
		return
	}
	client.mu.Lock()
	client.lastSuccessfulModel = strings.TrimSpace(resolved.model)
	client.mu.Unlock()
}

func (client *sessionRunnerDynamicModelClient) LastSuccessfulModel() string {
	if client == nil {
		return ""
	}
	client.mu.RLock()
	defer client.mu.RUnlock()
	return client.lastSuccessfulModel
}

func sessionRunnerModelHandoffAllowed(ctx context.Context, err error) bool {
	if err == nil || ctx == nil || ctx.Err() != nil || context.Cause(ctx) != nil {
		return false
	}
	// A provider-local request deadline does not cancel the task context. If the
	// user selected another model while that request was in flight, the new
	// provider may safely take over as long as no visible output was emitted.
	// Task cancellation and explicit user stop remain non-replayable.
	return !errors.Is(err, context.Canceled) &&
		!errors.Is(err, ErrGenerationStopped)
}

func (client *sessionRunnerDynamicModelClient) resolveHandoff(
	ctx context.Context,
	previous sessionRunnerResolvedModelClient,
	callErr error,
) (sessionRunnerResolvedModelClient, bool, error) {
	if !sessionRunnerModelHandoffAllowed(ctx, callErr) {
		return sessionRunnerResolvedModelClient{}, false, nil
	}
	next, err := client.resolve(ctx)
	if err != nil {
		return next, false, err
	}
	if next.identity == previous.identity {
		return sessionRunnerResolvedModelClient{}, false, nil
	}
	return next, true, nil
}

func (client *sessionRunnerDynamicModelClient) Complete(
	ctx context.Context,
	request agentruntime.ModelRequest,
) (agentruntime.ModelResponse, error) {
	resolved, err := client.resolve(ctx)
	if err != nil {
		return agentruntime.ModelResponse{}, wrapResolvedSessionRunnerModelCallError(err, resolved)
	}
	for {
		response, callErr := resolved.client.Complete(ctx, request)
		if callErr == nil {
			client.recordSuccess(resolved)
			if strings.TrimSpace(response.Model) == "" {
				response.Model = resolved.model
			}
			return response, nil
		}
		handoff, changed, resolveErr := client.resolveHandoff(ctx, resolved, callErr)
		if resolveErr != nil {
			return agentruntime.ModelResponse{}, wrapResolvedSessionRunnerModelCallError(
				errors.Join(callErr, resolveErr), handoff,
			)
		}
		if !changed {
			return agentruntime.ModelResponse{}, wrapResolvedSessionRunnerModelCallError(callErr, resolved)
		}
		resolved = handoff
	}
}

func (client *sessionRunnerDynamicModelClient) CompleteStream(
	ctx context.Context,
	request agentruntime.ModelRequest,
	emit func(agentruntime.ModelStreamEvent) error,
) (agentruntime.ModelResponse, error) {
	resolved, err := client.resolve(ctx)
	if err != nil {
		return agentruntime.ModelResponse{}, wrapResolvedSessionRunnerModelCallError(err, resolved)
	}
	var visibleOutput atomic.Bool
	trackedEmit := func(event agentruntime.ModelStreamEvent) error {
		if event.ContentDelta != "" {
			visibleOutput.Store(true)
		}
		if emit == nil {
			return nil
		}
		return emit(event)
	}
	complete := func(target sessionRunnerResolvedModelClient) (agentruntime.ModelResponse, error) {
		if streaming, ok := target.client.(agentruntime.StreamingModelClient); ok {
			return streaming.CompleteStream(ctx, request, trackedEmit)
		}
		return target.client.Complete(ctx, request)
	}
	for {
		response, callErr := complete(resolved)
		if callErr == nil {
			client.recordSuccess(resolved)
			if strings.TrimSpace(response.Model) == "" {
				response.Model = resolved.model
			}
			return response, nil
		}
		if visibleOutput.Load() {
			return agentruntime.ModelResponse{}, wrapResolvedSessionRunnerModelCallError(callErr, resolved)
		}
		handoff, changed, resolveErr := client.resolveHandoff(ctx, resolved, callErr)
		if resolveErr != nil {
			return agentruntime.ModelResponse{}, wrapResolvedSessionRunnerModelCallError(
				errors.Join(callErr, resolveErr), handoff,
			)
		}
		if !changed {
			return agentruntime.ModelResponse{}, wrapResolvedSessionRunnerModelCallError(callErr, resolved)
		}
		resolved = handoff
	}
}
