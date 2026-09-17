package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
	"synon-go/internal/outbox"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/software/localcontainer"
)

type managedOperationRequest struct {
	Kind             string                                         `json:"kind"`
	Create           *kernelruntime.CreateManagedEnvironmentInput   `json:"create,omitempty"`
	Packages         *kernelruntime.MutateManagedPackagesInput      `json:"packages,omitempty"`
	Register         *kernelruntime.RegisterManagedEnvironmentInput `json:"register,omitempty"`
	DeleteName       string                                         `json:"delete_name,omitempty"`
	DeleteGeneration *string                                        `json:"delete_generation,omitempty"`
	Container        *localcontainer.Spec                           `json:"container,omitempty"`
	Metadata         map[string]any                                 `json:"metadata"`
}

func (request managedOperationRequest) validate(tool string) error {
	variants := 0
	for _, present := range []bool{request.Create != nil, request.Packages != nil, request.Register != nil, request.DeleteName != "", request.Container != nil} {
		if present {
			variants++
		}
	}
	if variants != 1 || stringValue(request.Metadata["operation_id"]) == "" {
		return errors.New("managed operation requires one typed input and a receipt identity")
	}
	valid := false
	switch request.Kind {
	case "create":
		valid = tool == manageEnvironmentsToolName && request.Create != nil && request.Create.OperationID == stringValue(request.Metadata["operation_id"])
	case "register":
		valid = tool == manageEnvironmentsToolName && request.Register != nil && request.Register.OperationID == stringValue(request.Metadata["operation_id"])
	case "delete":
		valid = tool == manageEnvironmentsToolName && request.DeleteName != ""
	case "install", "uninstall":
		valid = tool == managePackagesToolName && request.Packages != nil && request.Packages.OperationID == stringValue(request.Metadata["operation_id"])
	case "container":
		valid = tool == manageEnvironmentsToolName && request.Container != nil
	}
	if !valid {
		return errors.New("managed operation input conflicts with its tool authority")
	}
	return nil
}

func (s *Server) runManagedOperation(ctx context.Context, owner, tool string, request managedOperationRequest, authority managedEnvironmentAuthority) (result map[string]any, resultErr error) {
	if err := request.validate(tool); err != nil {
		return nil, err
	}
	defer func() {
		if resultErr == nil && result != nil {
			result["environment_binding"] = request.binding(tool)
		}
	}()
	var environment kernelruntime.ManagedEnvironment
	var err error
	switch request.Kind {
	case "create":
		if request.Create == nil || authority == nil {
			return nil, errors.New("environment create authority is unavailable")
		}
		environment, err = authority.CreateManagedEnvironment(ctx, *request.Create)
	case "register":
		if request.Register == nil || authority == nil {
			return nil, errors.New("environment registration authority is unavailable")
		}
		if err = s.requireManagedEnvironmentRegistrationGrant(owner, request.Register.SourcePath, request.Register.VenvPath); err == nil {
			environment, err = authority.RegisterManagedEnvironment(ctx, *request.Register)
		}
	case "delete":
		if request.DeleteName == "" || authority == nil {
			return nil, errors.New("environment deletion authority is unavailable")
		}
		if request.DeleteGeneration == nil {
			return nil, errors.New("environment deletion has no admitted generation")
		}
		err = authority.DeleteManagedEnvironment(ctx, kernelruntime.DeleteManagedEnvironmentInput{Name: request.DeleteName, ExpectedGeneration: *request.DeleteGeneration, OperationID: stringValue(request.Metadata["operation_id"])})
		environment = kernelruntime.ManagedEnvironment{Name: request.DeleteName, Status: "deactivated"}
	case "install", "uninstall":
		if request.Packages == nil || authority == nil {
			return nil, errors.New("package mutation authority is unavailable")
		}
		if request.Kind == "install" {
			environment, err = authority.InstallManagedPackages(ctx, *request.Packages)
		} else {
			environment, err = authority.UninstallManagedPackages(ctx, *request.Packages)
		}
	case "container":
		if request.Container == nil {
			return nil, errors.New("container preparation input is unavailable")
		}
		manager, managerErr := s.localContainerEnvironmentManager()
		if managerErr != nil {
			return nil, managerErr
		}
		prepared, observed, prepareErr := manager.Prepare(ctx, *request.Container, stringValue(request.Metadata["operation_id"]))
		if prepareErr != nil {
			return managedEnvironmentFailureReceipt(tool, prepareErr, request.Metadata), nil
		}
		result := copyMapAny(request.Metadata)
		result["tool"], result["ok"], result["executed"], result["status"] = tool, true, true, "completed"
		result["environment"], result["mode"], result["container_preflight"] = prepared, prepared.Disposition, observed
		return result, nil
	default:
		return nil, errors.New("unknown managed operation kind")
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return managedEnvironmentFailureReceipt(tool, err, request.Metadata), nil
	}
	return managedEnvironmentOperationReceipt(tool, "completed", environment, request.Metadata), nil
}

// The same bounded receipt is consumed on the synchronous, notification and
// history paths; it carries identity, not a second execution authority.
func (request managedOperationRequest) binding(tool string) map[string]any {
	input := map[string]any{"implementation": request.Metadata["implementation"]}
	switch {
	case request.Create != nil:
		input["name"] = request.Create.Name
	case request.Register != nil:
		input["name"] = request.Register.Name
	case request.Packages != nil:
		input["environment"] = request.Packages.Environment
		input["fork_to"] = request.Packages.ForkTo
	}
	return map[string]any{"version": 1, "tool": tool, "input": input}
}

func (s *Server) executeManagedEnvironmentOperation(ctx context.Context, access workspace.KernelFrameAccess, call agentruntime.ToolCall, tool string, background bool, metadata map[string]any, request managedOperationRequest, authority managedEnvironmentAuthority) (any, error) {
	request.Metadata = copyMapAny(metadata)
	dispatchID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("task-operation:"+access.UserID+":"+access.Frame.ID+":"+access.Frame.IncarnationID+":"+call.ID)).String()
	if background && s.workspaceStore != nil && call.ID != "" {
		prior, found, err := s.workspaceStore.FindTaskOperation(ctx, dispatchID)
		if err != nil {
			return nil, err
		}
		if found {
			envelope, err := workspace.DecodeTaskOperation(prior)
			if err != nil {
				return nil, err
			}
			var admitted managedOperationRequest
			if err := json.Unmarshal(envelope.Request, &admitted); err != nil {
				return nil, err
			}
			// Preserve the original observed precondition. The enqueue transaction
			// still checks all caller intent, ownership and incarnation fields.
			request.DeleteGeneration = admitted.DeleteGeneration
		}
	}
	if request.Kind == "delete" && request.DeleteGeneration == nil {
		if authority == nil {
			return nil, errors.New("environment deletion authority is unavailable")
		}
		environment, found, err := authority.InspectManagedEnvironment(ctx, request.DeleteName)
		if err != nil {
			return nil, err
		}
		generation := ""
		if found {
			generation = environment.Generation
		}
		request.DeleteGeneration = &generation
	}
	if err := request.validate(tool); err != nil {
		return nil, err
	}
	if !background {
		return s.runManagedOperation(ctx, access.UserID, tool, request, authority)
	}
	s.detachedKernelObserverMu.Lock()
	draining := s.detachedKernelObserversDraining
	s.detachedKernelObserverMu.Unlock()
	if draining {
		return nil, errors.New("background operation admission is draining")
	}
	// Machine inventory belongs to its preflight receipt, not dispatch identity.
	// Re-observation after restart must not conflict with the admitted operation.
	delete(request.Metadata, "preflight")
	delete(request.Metadata, "container_preflight")
	if s.workspaceStore == nil || strings.TrimSpace(call.ID) == "" {
		return nil, errors.New("durable background operation authority is unavailable")
	}
	operationID := stringValue(metadata["operation_id"])
	notificationID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("task-operation-result:"+dispatchID)).String()
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	var observationAdmission *transcriptstore.ToolOperationAdmission
	if run, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun); run != nil && run.Transcript != nil {
		observationAdmission = &transcriptstore.ToolOperationAdmission{
			Claim: run.Transcript.Claim, CallID: call.ID, ToolName: tool, BootID: s.kernelOperationBootID,
		}
	}
	event, err := s.workspaceStore.EnqueueTaskOperation(ctx, workspace.TaskOperation{
		Version: 1, ID: dispatchID, OwnerID: access.UserID, FrameID: access.Frame.ID, FrameIncarnationID: access.Frame.IncarnationID,
		RootFrameID: access.Frame.RootFrameID, RootFrameIncarnationID: access.RootFrameIncarnationID, Tool: tool, NotificationID: notificationID, Request: raw,
		ObservationAdmission: observationAdmission,
	})
	if err != nil {
		return nil, err
	}
	admitted, err := workspace.DecodeTaskOperation(event)
	if err != nil {
		return nil, err
	}
	s.signalTranscriptWebDelivery()
	result := supervisedEnvironmentAccepted(admitted.Observation, operationID, notificationID, tool)
	result["operation_state"] = "queued"
	return result, nil
}

var errTaskOperationCancelled = errors.New("task operation was cancelled")

type managedOperationDeliverer struct {
	server    *Server
	authority managedEnvironmentAuthority
}

func (d managedOperationDeliverer) Deliver(ctx context.Context, event workspace.OutboxEvent) error {
	return d.server.deliverTaskOperation(ctx, event, d.authority)
}

func (s *Server) RunTaskOperationDispatcher(ctx context.Context) error {
	var authority managedEnvironmentAuthority
	if s.kernelManager != nil {
		authority = s.kernelManager
	}
	return s.runTaskOperationDispatcher(ctx, authority)
}

func (s *Server) runTaskOperationDispatcher(ctx context.Context, authority managedEnvironmentAuthority) error {
	const owner = "task-operation-dispatcher"
	ownedContext, cancel, acquired := s.reserveDetachedKernelObserver(ctx, owner)
	if !acquired {
		return errors.New("task operation dispatcher is already owned or draining")
	}
	defer s.releaseDetachedKernelObserver(owner, cancel)
	dispatcher, err := outbox.NewDispatcher(s.workspaceStore, managedOperationDeliverer{s, authority}, outbox.Options{
		WorkerID: "task-operation-" + uuid.NewString(), Topics: []string{workspace.TaskOperationOutboxTopic}, BatchSize: 4,
		Lease: 30 * time.Second, LongRunning: true, RetryBase: 100 * time.Millisecond, RetryMax: 30 * time.Second,
		OnError: func(err error) { log.Printf("task operation dispatcher: %v", err) },
	})
	if err != nil {
		return err
	}
	return dispatcher.Run(ownedContext)
}

func (s *Server) deliverTaskOperation(ctx context.Context, event workspace.OutboxEvent, authority managedEnvironmentAuthority) error {
	operation, err := workspace.DecodeTaskOperation(event)
	if err != nil {
		return err
	}
	var request managedOperationRequest
	decoder := json.NewDecoder(bytes.NewReader(operation.Request))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return err
	}
	if err := request.validate(operation.Tool); err != nil {
		return err
	}
	check := func(checkCtx context.Context) error {
		cancelled, err := s.workspaceStore.CheckTaskOperation(checkCtx, event)
		if err != nil {
			return err
		}
		if cancelled {
			return errTaskOperationCancelled
		}
		return nil
	}
	_, cancelled, checkErr := s.workspaceStore.BeginTaskOperation(ctx, event)
	if cancelled && checkErr == nil {
		checkErr = errTaskOperationCancelled
	}
	if checkErr != nil && !errors.Is(checkErr, errTaskOperationCancelled) {
		return checkErr
	}
	runCtx, cancel := context.WithCancelCause(ctx)
	monitorDone := make(chan struct{})
	if checkErr != nil {
		cancel(checkErr)
	}
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
			}
			if err := check(runCtx); err != nil {
				cancel(err)
				return
			}
		}
	}()
	var payload map[string]any
	if runCtx.Err() == nil {
		// Every adapter now reconciles its domain receipt or expected generation
		// under the same publication lock before repeating a side effect.
		payload, err = s.observeTaskOperation(runCtx, event, func(observedCtx context.Context) (map[string]any, error) {
			return s.runManagedOperation(observedCtx, operation.OwnerID, operation.Tool, request, authority)
		})
	}
	cause := context.Cause(runCtx)
	cancel(nil)
	<-monitorDone
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if cause != nil && !errors.Is(cause, errTaskOperationCancelled) {
		return cause
	}
	if errors.Is(cause, errTaskOperationCancelled) {
		if payload == nil {
			payload = map[string]any{"ok": false, "status": "cancelled", "tool": operation.Tool, "operation_id": request.Metadata["operation_id"]}
		}
		payload["task_cancelled"] = true
	} else if err != nil {
		payload = managedEnvironmentFailureReceipt(operation.Tool, err, request.Metadata)
	}
	if err = s.workspaceStore.SettleTaskOperation(ctx, event, payload); err != nil {
		return err
	}
	s.signalTranscriptWebDelivery()
	return outbox.ErrDeliverySettled
}
