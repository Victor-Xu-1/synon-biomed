package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"time"

	"github.com/google/uuid"

	"synon-go/internal/failurecontract"
	"synon-go/internal/software"
	"synon-go/internal/toolprogress"
)

var scientificRuntimeWarmupRetryDelays = []time.Duration{
	2 * time.Second,
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
}

type scientificRuntimeWarmupStatus struct {
	State         string
	Attempt       int
	MaxAttempts   int
	Environment   string
	Generation    string
	LastErrorCode string
	RetryAt       time.Time
	Progress      *scientificRuntimeProgress
}

type scientificRuntimeWarmupResult struct {
	Environment string
	Generation  string
}

type scientificRuntimeWarmupEnsure func(context.Context) (scientificRuntimeWarmupResult, error)

func scientificRuntimeWarmupError(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	var operationErr *software.OperationError
	if errors.As(err, &operationErr) && operationErr != nil {
		code := operationErr.Code
		if code == "" {
			code = "software_runtime_unavailable"
		}
		retryable := operationErr.Kind == failurecontract.Transient ||
			operationErr.Kind == failurecontract.RateLimited ||
			operationErr.Kind == failurecontract.NetworkBridgeDown ||
			operationErr.Kind == failurecontract.ProviderDegraded
		return code, retryable
	}
	return "software_runtime_unavailable", true
}

func runScientificRuntimeWarmup(
	ctx context.Context,
	delays []time.Duration,
	ensure scientificRuntimeWarmupEnsure,
	observe func(scientificRuntimeWarmupStatus),
) scientificRuntimeWarmupStatus {
	notify := func(status scientificRuntimeWarmupStatus) {
		if observe != nil {
			observe(status)
		}
	}
	status := scientificRuntimeWarmupStatus{State: "scheduled", MaxAttempts: len(delays)}
	if len(delays) == 0 || ensure == nil {
		status.State = "disabled"
		notify(status)
		return status
	}

	for index, delay := range delays {
		status.Attempt = index + 1
		status.RetryAt = time.Time{}
		if delay > 0 {
			status.State = "scheduled"
			if index > 0 {
				status.State = "retrying"
			}
			status.RetryAt = time.Now().UTC().Add(delay)
			notify(status)
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				status.State = "stopped"
				status.RetryAt = time.Time{}
				notify(status)
				return status
			case <-timer.C:
			}
		}

		status.State = "preparing"
		status.RetryAt = time.Time{}
		notify(status)
		result, err := ensure(ctx)
		if result.Environment != "" {
			status.Environment = result.Environment
		}
		if result.Generation != "" {
			status.Generation = result.Generation
		}
		if err == nil {
			status.State = "ready"
			status.LastErrorCode = ""
			notify(status)
			return status
		}

		code, retryable := scientificRuntimeWarmupError(err)
		status.LastErrorCode = code
		if !retryable || index == len(delays)-1 {
			status.State = "failed"
			notify(status)
			return status
		}
	}
	return status
}

func (s *Server) setScientificRuntimeWarmupStatus(id string, status scientificRuntimeWarmupStatus) {
	if s == nil {
		return
	}
	s.scientificRuntimeWarmupMu.Lock()
	if s.scientificRuntimeWarmups == nil {
		s.scientificRuntimeWarmups = make(map[string]scientificRuntimeWarmupStatus)
	}
	s.scientificRuntimeWarmups[id] = status
	s.scientificRuntimeWarmupMu.Unlock()
}

func (s *Server) setScientificRuntimeWarmupCancel(id string, cancel context.CancelFunc) {
	if s == nil {
		return
	}
	s.scientificRuntimeWarmupMu.Lock()
	if s.scientificRuntimeWarmupCancels == nil {
		s.scientificRuntimeWarmupCancels = make(map[string]context.CancelFunc)
	}
	if cancel == nil {
		delete(s.scientificRuntimeWarmupCancels, id)
	} else {
		s.scientificRuntimeWarmupCancels[id] = cancel
	}
	s.scientificRuntimeWarmupMu.Unlock()
}

func (s *Server) cancelScientificRuntimeWarmup(id string) bool {
	if s == nil {
		return false
	}
	s.scientificRuntimeWarmupMu.RLock()
	cancel := s.scientificRuntimeWarmupCancels[id]
	s.scientificRuntimeWarmupMu.RUnlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

func (s *Server) scientificRuntimeWarmupStatus(id string) scientificRuntimeWarmupStatus {
	if s == nil {
		return scientificRuntimeWarmupStatus{State: "disabled"}
	}
	s.scientificRuntimeWarmupMu.RLock()
	status := s.scientificRuntimeWarmups[id]
	s.scientificRuntimeWarmupMu.RUnlock()
	if status.State == "" {
		if !s.ManagedEnvironmentSupervisorEnabled() {
			status.State = "disabled"
		} else {
			status.State = "waiting_for_selection"
			status.MaxAttempts = len(scientificRuntimeWarmupRetryDelays)
		}
	}
	return status
}

func scientificRuntimeWarmupHealthValue(status scientificRuntimeWarmupStatus) map[string]any {
	result := map[string]any{
		"status": status.State,
	}
	appendScientificRuntimeProgress(result, status.Progress)
	if status.Attempt > 0 {
		result["attempt"] = status.Attempt
	}
	if status.MaxAttempts > 0 {
		result["max_attempts"] = status.MaxAttempts
	}
	if status.Environment != "" {
		result["environment"] = status.Environment
	}
	if status.Generation != "" {
		result["generation"] = status.Generation
	}
	if status.LastErrorCode != "" {
		result["last_error_code"] = status.LastErrorCode
	}
	if !status.RetryAt.IsZero() {
		result["retry_at"] = status.RetryAt.Format(time.RFC3339)
	}
	return result
}

func (s *Server) scientificRuntimeWarmupsHealth() map[string]any {
	result := make(map[string]any)
	for _, definition := range scientificRuntimeWarmupDefinitions() {
		result[definition.ID] = scientificRuntimeWarmupHealthValue(s.scientificRuntimeWarmupStatus(definition.ID))
	}
	return result
}

func (s *Server) queueScientificRuntimeWarmup(id string) bool {
	if s == nil || s.scientificRuntimeWarmupWake == nil {
		return false
	}
	if _, found := scientificRuntimeWarmupDefinitionByID(id); !found {
		return false
	}
	s.scientificRuntimeWarmupMu.Lock()
	status := s.scientificRuntimeWarmups[id]
	switch status.State {
	case "scheduled", "preparing", "retrying", "ready":
		s.scientificRuntimeWarmupMu.Unlock()
		return true
	}
	status = scientificRuntimeWarmupStatus{
		State: "scheduled", MaxAttempts: len(scientificRuntimeWarmupRetryDelays),
	}
	if s.scientificRuntimeWarmups == nil {
		s.scientificRuntimeWarmups = make(map[string]scientificRuntimeWarmupStatus)
	}
	s.scientificRuntimeWarmups[id] = status
	s.scientificRuntimeWarmupMu.Unlock()
	select {
	case s.scientificRuntimeWarmupWake <- id:
		return true
	default:
		s.setScientificRuntimeWarmupStatus(id, scientificRuntimeWarmupStatus{
			State: "failed", MaxAttempts: len(scientificRuntimeWarmupRetryDelays),
			LastErrorCode: "warmup_queue_unavailable",
		})
		return false
	}
}

func (s *Server) ensureScientificRuntime(
	ctx context.Context,
	id string,
	request software.Request,
	executableWitnesses []string,
) (scientificRuntimeWarmupResult, error) {
	canonical, err := software.CanonicalRequestJSON(request)
	if err != nil {
		return scientificRuntimeWarmupResult{}, err
	}
	var runtimeInput map[string]any
	if err := json.Unmarshal(canonical, &runtimeInput); err != nil {
		return scientificRuntimeWarmupResult{}, err
	}
	controller, plan, err := s.resolveSoftwareRuntime(ctx, runtimeInput)
	if err != nil {
		return scientificRuntimeWarmupResult{}, err
	}
	provision, _, err := s.provisionSoftwareRuntime(ctx, controller, plan, id+"-warmup-"+uuid.NewString())
	if err != nil {
		return scientificRuntimeWarmupResult{Environment: plan.Environment}, err
	}
	for _, executable := range executableWitnesses {
		if err := s.kernelManager.VerifyManagedEnvironmentExecutable(provision.Environment, executable); err != nil {
			return scientificRuntimeWarmupResult{
					Environment: provision.Environment, Generation: provision.Generation,
				}, software.WrapOperationError(
					err,
					"software_executable_missing",
					fmt.Sprintf("runtime executable witness %q failed", executable),
					"repair_the_registered_runtime_then_retry_the_same_warmup",
					false,
				)
		}
	}
	return scientificRuntimeWarmupResult{
		Environment: provision.Environment,
		Generation:  provision.Generation,
	}, nil
}

func (s *Server) ensureScientificRuntimeDefinition(
	ctx context.Context,
	definition scientificRuntimeWarmupDefinition,
) (scientificRuntimeWarmupResult, error) {
	request, executableWitnesses, err := definition.BuildRequest()
	if err != nil {
		return scientificRuntimeWarmupResult{}, err
	}
	return s.ensureScientificRuntime(ctx, definition.ID, request, executableWitnesses)
}

func (s *Server) loadScientificRuntimeWarmupSelection() ([]string, bool, error) {
	if s == nil || s.settingsStore == nil {
		return defaultScientificRuntimeWarmupIDs(), false, nil
	}
	setting, found, err := s.settingsStore.Get(scientificRuntimeSelectionSettingKey)
	if err != nil || !found {
		return defaultScientificRuntimeWarmupIDs(), false, err
	}
	raw, ok := setting.Value.([]any)
	if !ok {
		if stringsValue, stringsOK := setting.Value.([]string); stringsOK {
			selected, normalizeErr := normalizeScientificRuntimeWarmupIDs(stringsValue)
			return selected, true, normalizeErr
		}
		return nil, true, errors.New("stored scientific runtime selection is invalid")
	}
	values := make([]string, 0, len(raw))
	for _, item := range raw {
		value, ok := item.(string)
		if !ok {
			return nil, true, errors.New("stored scientific runtime selection is invalid")
		}
		values = append(values, value)
	}
	selected, err := normalizeScientificRuntimeWarmupIDs(values)
	return selected, true, err
}

func (s *Server) storeScientificRuntimeWarmupSelection(values []string) ([]string, error) {
	selected, err := normalizeScientificRuntimeWarmupIDs(values)
	if err != nil {
		return nil, err
	}
	if s == nil || s.settingsStore == nil {
		return nil, errors.New("settings store is not configured")
	}
	if _, err := s.settingsStore.Set(scientificRuntimeSelectionSettingKey, selected); err != nil {
		return nil, err
	}
	if s.ManagedEnvironmentSupervisorEnabled() {
		for _, id := range selected {
			s.queueScientificRuntimeWarmup(id)
		}
	}
	return selected, nil
}

// RunScientificRuntimeWarmups owns optional post-install runtime preparation.
// The gateway and onboarding stay available while one bounded queue prepares
// selected immutable environments. Large runtimes are never queued unless a
// persisted user selection includes them.
func (s *Server) RunScientificRuntimeWarmups(ctx context.Context) error {
	if ctx == nil {
		return errors.New("scientific runtime warmup requires a service context")
	}
	definitions := scientificRuntimeWarmupDefinitions()
	if s == nil || !s.ManagedEnvironmentSupervisorEnabled() {
		if s != nil {
			for _, definition := range definitions {
				s.setScientificRuntimeWarmupStatus(definition.ID, scientificRuntimeWarmupStatus{State: "disabled"})
			}
		}
		<-ctx.Done()
		return nil
	}
	for _, definition := range definitions {
		s.setScientificRuntimeWarmupStatus(definition.ID, scientificRuntimeWarmupStatus{
			State: "waiting_for_selection", MaxAttempts: len(scientificRuntimeWarmupRetryDelays),
		})
	}

	selected, configured, err := s.loadScientificRuntimeWarmupSelection()
	if err != nil {
		log.Printf("scientific runtime warmup selection is unavailable: %T", err)
	} else if configured {
		for _, id := range selected {
			s.queueScientificRuntimeWarmup(id)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case id := <-s.scientificRuntimeWarmupWake:
			if !s.shouldRunScientificRuntimeWarmup(id) {
				continue
			}
			definition, found := scientificRuntimeWarmupDefinitionByID(id)
			if !found {
				continue
			}
			runContext, cancel := context.WithCancel(ctx)
			s.setScientificRuntimeWarmupCancel(id, cancel)
			status := runScientificRuntimeWarmup(
				runContext,
				scientificRuntimeWarmupRetryDelays,
				func(runContext context.Context) (scientificRuntimeWarmupResult, error) {
					attempt := s.scientificRuntimeWarmupStatus(id).Attempt
					progressContext := toolprogress.WithReporter(runContext, func(update toolprogress.Update) {
						s.reportScientificRuntimeProgress(id, attempt, update)
					})
					return s.ensureScientificRuntimeDefinition(progressContext, definition)
				},
				func(current scientificRuntimeWarmupStatus) {
					s.setScientificRuntimeWarmupStatus(id, current)
				},
			)
			cancel()
			s.setScientificRuntimeWarmupCancel(id, nil)
			if status.State == "ready" {
				log.Printf("scientific runtime warmup completed id=%s environment=%s generation=%s", id, status.Environment, status.Generation)
			} else if status.State == "failed" {
				log.Printf("scientific runtime warmup stopped id=%s attempts=%d code=%s", id, status.Attempt, status.LastErrorCode)
			}
		}
	}
}

// Recheck the persisted selection at dequeue time. A stale queued request
// must not install software that the user deselected before it started.
func (s *Server) shouldRunScientificRuntimeWarmup(id string) bool {
	if s.scientificRuntimeWarmupStatus(id).State == "ready" {
		return false
	}
	selected, configured, err := s.loadScientificRuntimeWarmupSelection()
	if err != nil {
		s.setScientificRuntimeWarmupStatus(id, scientificRuntimeWarmupStatus{State: "failed", LastErrorCode: "runtime_selection_unavailable"})
		return false
	}
	if !configured || !slices.Contains(selected, id) {
		s.setScientificRuntimeWarmupStatus(id, scientificRuntimeWarmupStatus{State: "waiting_for_selection"})
		return false
	}
	return true
}
