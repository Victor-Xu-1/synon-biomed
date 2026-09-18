package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

const userTerminalCellTimeout = 10 * time.Minute

func (s *Server) registerKernelRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("GET /api/kernels", s.handleAllKernels)
	mux.HandleFunc("GET /api/frames/{rootFrameId}/kernels", s.handleSessionKernels)
	mux.HandleFunc("POST /api/frames/{frameId}/kernels/{kernelId}/stop", s.handleStopKernel)
	mux.HandleFunc("POST /api/frames/{frameId}/kernel-exec", s.handleKernelTerminalExec)
	s.registerStructureMinimizationRoutes(mux)
	s.registerStructureInteractionDiagramRoutes(mux)
	s.registerStructureElectrostaticMapRoutes(mux)
	mux.HandleFunc("POST /api/frames/{frameId}/kernel-exec/{execId}/interrupt", s.handleKernelTerminalInterrupt)
	mux.HandleFunc("POST /api/system/refresh-kernels", s.handleRefreshKernels)
}

type authorizedKernelInventory struct {
	Kernels  []kernelruntime.SessionKernel
	Machine  kernelruntime.MachineResourceSnapshot
	Detached map[string]workspace.DetachedKernelInventoryEntry
}

func (s *Server) collectAuthorizedKernelInventory(
	ctx context.Context,
	userID string,
) (authorizedKernelInventory, error) {
	detachedEntries, err := s.workspaceStore.ListActiveDetachedKernelInventory(ctx, userID, 1000)
	if err != nil {
		return authorizedKernelInventory{}, err
	}
	external := make([]kernelruntime.ExternalSessionKernel, 0, len(detachedEntries))
	detachedCandidates := make(map[string]workspace.DetachedKernelInventoryEntry, len(detachedEntries))
	for _, entry := range detachedEntries {
		live, liveErr := detachedKernelBackendLive(entry.Backend, time.Now().UTC())
		if liveErr != nil {
			return authorizedKernelInventory{}, liveErr
		}
		if !live {
			continue
		}
		projection := detachedKernelSessionProjection(entry)
		external = append(external, kernelruntime.ExternalSessionKernel{
			Kernel: projection, PID: int(entry.Backend.WorkerPID),
			PIDStartTicks: uint64(max(0, entry.Backend.WorkerPIDStartTicks)),
			DiskPath:      entry.SessionSpec.WorkspaceDir,
		})
		detachedCandidates[projection.KernelID] = entry
	}
	snapshot := s.kernelManager.ListAllSessionKernelsWithExternalResources("", external)
	result := authorizedKernelInventory{
		Kernels: make([]kernelruntime.SessionKernel, 0, len(snapshot.Kernels)),
		Machine: snapshot.Machine, Detached: make(map[string]workspace.DetachedKernelInventoryEntry),
	}
	projectNames := map[string]string{}
	rootNames := map[string]string{}
	recordsByRoot := map[string][]workspace.ExecutionLogRecord{}

	for _, kernel := range snapshot.Kernels {
		access, found, err := s.workspaceStore.GetKernelFrameAccess(kernel.FrameID)
		if err != nil {
			return authorizedKernelInventory{}, err
		}
		if !found || access.UserID != userID || access.IsHidden ||
			kernel.FrameIncarnationID != "" && access.Frame.IncarnationID != kernel.FrameIncarnationID ||
			kernel.RootFrameIncarnationID != "" && access.RootFrameIncarnationID != kernel.RootFrameIncarnationID {
			continue
		}
		rootFrameID := strings.TrimSpace(kernel.RootFrameID)
		if rootFrameID == "" {
			rootFrameID = access.Frame.RootFrameID
		}
		if rootFrameID == "" {
			rootFrameID = access.Frame.ID
		}
		rootName, ok := rootNames[rootFrameID]
		if !ok {
			rootAccess, rootFound, rootErr := s.workspaceStore.GetKernelFrameAccess(rootFrameID)
			if rootErr != nil {
				return authorizedKernelInventory{}, rootErr
			}
			if !rootFound || rootAccess.UserID != userID || rootAccess.IsHidden {
				continue
			}
			rootName = rootAccess.Frame.Name
			rootNames[rootFrameID] = rootName
		}
		projectName, ok := projectNames[access.Frame.ProjectID]
		if !ok {
			project, projectFound, projectErr := s.workspaceStore.GetProject(access.Frame.ProjectID)
			if projectErr != nil {
				return authorizedKernelInventory{}, projectErr
			}
			if !projectFound {
				continue
			}
			projectName = project.Name
			projectNames[access.Frame.ProjectID] = projectName
		}
		records, ok := recordsByRoot[rootFrameID]
		if !ok {
			records, err = s.workspaceStore.ListExecutionLog(rootFrameID, "")
			if err != nil {
				return authorizedKernelInventory{}, err
			}
			recordsByRoot[rootFrameID] = records
		}
		var lastRecord *workspace.ExecutionLogRecord
		cellCount := 0
		for index := range records {
			record := &records[index]
			if record.KernelID != kernel.KernelID {
				continue
			}
			cellCount++
			if lastRecord == nil || record.ExecutedAt.After(lastRecord.ExecutedAt) {
				lastRecord = record
			}
		}
		kernel.RootFrameID = rootFrameID
		kernel.ProjectID = access.Frame.ProjectID
		kernel.ProjectName = projectName
		kernel.SessionTitle = rootName
		if kernel.SessionTitle == "" {
			kernel.SessionTitle = access.Frame.Name
		}
		kernel.AgentName = access.Frame.AgentName
		kernel.DelegateName = nil
		if access.DelegateName != "" {
			delegateName := access.DelegateName
			kernel.DelegateName = &delegateName
		}
		kernel.CellCount = cellCount
		if lastRecord != nil {
			kernel.LastCell = &kernelruntime.LastCell{Source: lastRecord.Source, EndedAt: lastRecord.ExecutedAt}
			if kernel.CurrentCell == nil {
				kernel.LastDescription = lastRecord.Source
			}
		}
		if kernel.CurrentCell != nil {
			kernel.LastDescription = kernel.CurrentCell.HumanDescription
			if kernel.LastDescription == "" {
				kernel.LastDescription = kernel.CurrentCell.Source
			}
		}
		if detached, ok := detachedCandidates[kernel.KernelID]; ok {
			if err := s.fenceDetachedExecutionObservation(ctx, &kernel, detached); err != nil {
				return authorizedKernelInventory{}, err
			}
			result.Detached[kernel.KernelID] = detached
		}
		result.Kernels = append(result.Kernels, kernel)
	}
	recomputeUserKernelMachine(&result.Machine, result.Kernels)
	return result, nil
}

func detachedKernelBackendLive(backend workspace.KernelExecutionBackend, now time.Time) (bool, error) {
	switch backend.State {
	case workspace.KernelExecutionBackendStateStarting:
		return !backend.UpdatedAt.IsZero() && now.Sub(backend.UpdatedAt.UTC()) <= 30*time.Second, nil
	case workspace.KernelExecutionBackendStateReady, workspace.KernelExecutionBackendStateDraining:
		if backend.WorkerPID <= 0 || backend.WorkerPIDStartTicks <= 0 {
			return false, nil
		}
		return kernelruntime.ProcessIdentityAlive(backend.WorkerPID, backend.WorkerPIDStartTicks)
	default:
		return false, nil
	}
}

func detachedKernelSessionProjection(entry workspace.DetachedKernelInventoryEntry) kernelruntime.SessionKernel {
	spec := entry.SessionSpec
	lastUsed := entry.Backend.UpdatedAt.UTC()
	projection := kernelruntime.SessionKernel{
		FrameID: spec.FrameID, FrameIncarnationID: spec.FrameIncarnationID,
		RootFrameID: spec.RootFrameID, RootFrameIncarnationID: spec.RootFrameIncarnationID,
		ProjectID: spec.ProjectID, AgentName: spec.AgentName, Environment: spec.Environment,
		RuntimeGeneration: spec.RuntimeGeneration, Language: spec.Language, KernelID: spec.KernelID,
		KernelKind: spec.KernelKind, Starting: entry.Backend.State == workspace.KernelExecutionBackendStateStarting,
		ExecutionCount: entry.ExecutionCount, LastUsed: lastUsed,
	}
	if spec.DelegateName != "" {
		delegateName := spec.DelegateName
		projection.DelegateName = &delegateName
	}
	if entry.Execution == nil || entry.Request == nil {
		return projection
	}
	execution, request := entry.Execution, entry.Request
	startedAt := execution.AcceptedAt.UTC()
	if execution.WorkerStartedAt != nil {
		startedAt = execution.WorkerStartedAt.UTC()
	} else {
		projection.Starting = true
	}
	if execution.UpdatedAt.After(lastUsed) {
		projection.LastUsed = execution.UpdatedAt.UTC()
	}
	projection.Busy = execution.State == workspace.DetachedKernelExecutionStateStarted ||
		execution.State == workspace.DetachedKernelExecutionStateCancelRequested
	tag := execution.ExecutionID
	projection.CurrentCellTag = &tag
	description := detachedKernelHumanDescription(entry.Operation)
	source, truncated := detachedKernelDisplaySource(entry.Operation, request.Code, description)
	projection.CurrentCell = &kernelruntime.CurrentCell{
		Source: source, Origin: request.Origin, StartedAt: startedAt,
		HumanDescription: description, Truncated: truncated,
	}
	projection.LastDescription = projection.CurrentCell.HumanDescription
	return projection
}

func detachedKernelDisplaySource(
	operation *workspace.KernelLocalOperation,
	source, description string,
) (string, bool) {
	if operation != nil && strings.EqualFold(strings.TrimSpace(operation.Tool), "software_runtime") && description != "" {
		return description, false
	}
	const maximumBytes = 32 * 1024
	if len(source) <= maximumBytes {
		return source, false
	}
	end := maximumBytes
	for end > 0 && !utf8.ValidString(source[:end]) {
		end--
	}
	return source[:end], true
}

func detachedKernelHumanDescription(operation *workspace.KernelLocalOperation) string {
	if operation == nil {
		return "Running detached kernel operation"
	}
	var input struct {
		Capability string   `json:"capability"`
		Executable string   `json:"executable"`
		Args       []string `json:"args"`
	}
	_ = json.Unmarshal(operation.InputJSON, &input)
	parts := make([]string, 0, len(input.Args)+1)
	if executable := strings.TrimSpace(input.Executable); executable != "" {
		parts = append(parts, filepath.Base(executable))
	}
	for _, value := range input.Args {
		if value = strings.TrimSpace(value); value != "" {
			parts = append(parts, filepath.Base(value))
		}
	}
	command := strings.Join(parts, " ")
	capability := strings.ReplaceAll(strings.TrimSpace(input.Capability), "-", " ")
	if capability != "" && command != "" {
		return capability + " · " + command
	}
	if command != "" {
		return command
	}
	tool := strings.ReplaceAll(strings.TrimSpace(operation.Tool), "_", " ")
	if tool == "" {
		return "Running detached kernel operation"
	}
	return "Running " + tool
}

func recomputeUserKernelMachine(machine *kernelruntime.MachineResourceSnapshot, kernels []kernelruntime.SessionKernel) {
	machine.KernelCount = len(kernels)
	machine.BusyCount = 0
	machine.KernelRSSBytes = 0
	var cpu float64
	hasCPU := false
	for _, kernel := range kernels {
		if kernel.Busy || kernel.Starting {
			machine.BusyCount++
		}
		if kernel.RSSBytes != nil {
			machine.KernelRSSBytes += *kernel.RSSBytes
		}
		if kernel.CPUPct != nil {
			cpu += *kernel.CPUPct
			hasCPU = true
		}
	}
	if len(kernels) == 0 {
		zero := float64(0)
		machine.KernelCPUPct = &zero
	} else if hasCPU {
		machine.KernelCPUPct = &cpu
	} else {
		machine.KernelCPUPct = nil
	}
}

func (s *Server) kernelInventoryReady(w http.ResponseWriter) bool {
	if s.workspaceStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Workspace runtime is not configured")
		return false
	}
	if s.kernelManager == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Verified kernel runtime is not available")
		return false
	}
	return true
}

func (s *Server) handleAllKernels(w http.ResponseWriter, r *http.Request) {
	if !s.kernelInventoryReady(w) {
		return
	}
	inventory, err := s.collectAuthorizedKernelInventory(r.Context(), compatAgentUserID(r))
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"kernels": inventory.Kernels,
		"machine": inventory.Machine,
	})
}

func (s *Server) handleSessionKernels(w http.ResponseWriter, r *http.Request) {
	if !s.kernelInventoryReady(w) {
		return
	}
	rootFrameID := strings.TrimSpace(r.PathValue("rootFrameId"))
	requestAccess, found, err := s.workspaceStore.GetKernelFrameAccess(rootFrameID)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	userID := compatAgentUserID(r)
	if !found || requestAccess.UserID != userID {
		writeV11Detail(w, http.StatusNotFound, "Frame "+rootFrameID+" not found")
		return
	}
	inventory, err := s.collectAuthorizedKernelInventory(r.Context(), userID)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	kernels := make([]kernelruntime.SessionKernel, 0)
	for _, kernel := range inventory.Kernels {
		if kernel.RootFrameID == rootFrameID {
			kernels = append(kernels, kernel)
		}
	}
	records, err := s.workspaceStore.ListExecutionLog(rootFrameID, "")
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"kernels": kernels, "has_history": len(records) > 0,
	})
}

func (s *Server) handleStopKernel(w http.ResponseWriter, r *http.Request) {
	if !s.kernelInventoryReady(w) {
		return
	}
	frameID := strings.TrimSpace(r.PathValue("frameId"))
	kernelID := strings.TrimSpace(r.PathValue("kernelId"))
	access, ok := s.kernelFrameRequestAccess(w, r, frameID, true)
	if !ok {
		return
	}
	inventory, err := s.collectAuthorizedKernelInventory(r.Context(), access.UserID)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	var target *kernelruntime.SessionKernel
	for index := range inventory.Kernels {
		kernel := &inventory.Kernels[index]
		if kernel.KernelID == kernelID && kernel.FrameID == frameID {
			target = kernel
			break
		}
	}
	if target == nil {
		writeV11Detail(w, http.StatusNotFound, "Kernel "+kernelID+" not found")
		return
	}
	var body struct {
		Mode       string `json:"mode"`
		Reason     string `json:"reason"`
		Force      bool   `json:"force"`
		AttachOnly bool   `json:"attach_only"`
	}
	if err := decodeAgentCompatJSON(r, &body); err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid kernel stop request: "+err.Error())
		return
	}
	mode := strings.ToLower(strings.TrimSpace(body.Mode))
	if mode != "interrupt" && mode != "clear" {
		writeV11Detail(w, http.StatusBadRequest, "mode must be 'interrupt' or 'clear'")
		return
	}
	if utf8.RuneCountInString(body.Reason) > 500 {
		writeV11Detail(w, http.StatusBadRequest, "reason must be at most 500 characters")
		return
	}
	reason := sanitizeKernelStopReason(body.Reason)
	explicitReason := reason != ""
	if !explicitReason && mode == "interrupt" && !body.Force {
		reason = "stopped by the user"
	}
	if body.AttachOnly {
		if _, detached := inventory.Detached[kernelID]; !detached {
			s.kernelManager.AttachKernelStopReason(kernelID, reason)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "mode": mode, "interrupted": false,
		})
		return
	}
	if detached, ok := inventory.Detached[kernelID]; ok {
		effectiveMode := mode
		if body.Force {
			effectiveMode = "clear"
		}
		response, err := s.stopDetachedKernel(r.Context(), detached, effectiveMode, reason)
		if err != nil {
			status := http.StatusInternalServerError
			if s.kernelExecutionBackend == nil {
				status = http.StatusServiceUnavailable
			}
			writeV11Detail(w, status, "Failed to stop detached kernel: "+err.Error())
			return
		}
		response["mode"] = mode
		writeJSON(w, http.StatusOK, response)
		return
	}
	if mode == "interrupt" && !body.Force {
		result := s.kernelManager.InterruptKernelWithReason(kernelID, reason)
		response := map[string]any{
			"ok": true, "mode": mode, "interrupted": result.Interrupted,
		}
		if !result.Interrupted && result.Reason != "" {
			response["reason"] = result.Reason
		}
		writeJSON(w, http.StatusOK, response)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var stopErr error
	if body.Force {
		stopErr = s.kernelManager.TerminateKernel(ctx, kernelID, reason)
	} else {
		stopErr = s.kernelManager.CloseKernelWithReason(ctx, kernelID, reason)
	}
	if stopErr != nil {
		writeV11Detail(w, http.StatusInternalServerError, "Failed to stop kernel: "+stopErr.Error())
		return
	}
	response := map[string]any{"ok": true, "mode": mode, "interrupted": target.Busy}
	writeJSON(w, http.StatusOK, response)
}

func sanitizeKernelStopReason(value string) string {
	var builder strings.Builder
	for _, character := range value {
		if unicode.IsControl(character) || character == '\u2028' || character == '\u2029' ||
			character >= '\u202a' && character <= '\u202e' || character >= '\u2066' && character <= '\u2069' ||
			character >= '\u200b' && character <= '\u200f' || character == '\ufeff' {
			builder.WriteByte(' ')
			continue
		}
		builder.WriteRune(character)
	}
	return strings.TrimSpace(builder.String())
}

func (s *Server) stopDetachedKernel(
	ctx context.Context,
	entry workspace.DetachedKernelInventoryEntry,
	mode, reason string,
) (map[string]any, error) {
	if s == nil || s.kernelExecutionBackend == nil {
		return nil, errors.New("detached kernel control is unavailable")
	}
	backend := entry.Backend
	session := kernelruntime.BackendSessionRef{
		BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration,
		KernelID: backend.KernelID, KernelGeneration: backend.KernelGeneration,
		ExecutorInstanceID: backend.ExecutorInstanceID, SocketPath: backend.SocketPath,
	}
	if mode == "clear" {
		if entry.Execution != nil {
			_, err := s.cancelDetachedKernelExecution(ctx, entry, reason)
			if err != nil {
				return nil, err
			}
			response := map[string]any{
				"ok": true, "mode": "clear", "interrupted": true,
			}
			return response, nil
		}
		stopCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		lease, err := s.kernelExecutionBackend.AcquireSessionControl(stopCtx, session, 30*time.Second)
		if err != nil {
			return nil, err
		}
		if err := s.kernelExecutionBackend.CloseSession(stopCtx, session, lease); err != nil {
			return nil, err
		}
		response := map[string]any{"ok": true, "mode": "clear", "interrupted": false}
		return response, nil
	}
	if entry.Execution == nil {
		return map[string]any{"ok": true, "mode": "interrupt", "interrupted": false}, nil
	}
	_, err := s.cancelDetachedKernelExecution(ctx, entry, reason)
	if err != nil {
		return nil, err
	}
	response := map[string]any{"ok": true, "mode": "interrupt", "interrupted": true}
	return response, nil
}

func (s *Server) cancelDetachedKernelExecution(
	ctx context.Context,
	entry workspace.DetachedKernelInventoryEntry,
	reason string,
) (kernelruntime.BackendCancelReceipt, error) {
	if s == nil || s.kernelExecutionBackend == nil || entry.Execution == nil {
		return kernelruntime.BackendCancelReceipt{}, errors.New("detached kernel execution control is unavailable")
	}
	execution := entry.Execution
	ref := kernelruntime.BackendExecutionRef{
		ExecutionID: execution.ExecutionID, OperationID: execution.OperationID,
		BackendID: execution.BackendID, BackendGeneration: execution.BackendGeneration,
		RequestSHA256: execution.RequestSHA256, ConfinementSHA256: execution.ConfinementSHA256,
	}
	stopCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	lease, snapshot, err := s.kernelExecutionBackend.AcquireControl(stopCtx, ref, 30*time.Second)
	if err != nil {
		return kernelruntime.BackendCancelReceipt{}, err
	}
	receipt, err := s.kernelExecutionBackend.Cancel(stopCtx, ref, lease, kernelruntime.BackendCancelRequest{
		CancelRequestID: "kernel-ui-stop-" + uuid.NewString(), ExpectedVersion: snapshot.StateVersion,
		Reason: reason,
	})
	if err != nil {
		return kernelruntime.BackendCancelReceipt{}, err
	}
	return receipt, nil
}

func (s *Server) handleKernelTerminalExec(w http.ResponseWriter, r *http.Request) {
	frameID := strings.TrimSpace(r.PathValue("frameId"))
	access, ok := s.kernelFrameRequestAccess(w, r, frameID, true)
	if !ok {
		return
	}
	var body map[string]any
	if err := decodeAgentCompatJSON(r, &body); err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid kernel execution request: "+err.Error())
		return
	}
	code, codeOK := body["code"].(string)
	if !codeOK {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "code must be a string"})
		return
	}
	language, languageOK := body["language"].(string)
	environment, environmentOK := body["environment"].(string)
	language = strings.ToLower(strings.TrimSpace(language))
	if !languageOK || language != "python" && language != "r" {
		writeV11Detail(w, http.StatusBadRequest, "language must be 'python' or 'r'")
		return
	}
	if !environmentOK || !kernelruntime.ValidEnvironmentName(environment) {
		writeV11Detail(w, http.StatusBadRequest, "environment must be 1-100 bytes using letters, digits, dot, underscore, or dash")
		return
	}
	if code == "" {
		writeV11Detail(w, http.StatusBadRequest, "code must not be empty")
		return
	}
	execID, toolUseID, err := s.submitKernelTerminalExecution(access, frameID, language, environment, code)
	if err != nil {
		writeV11Detail(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"exec_id": execID, "tool_use_id": toolUseID})
}

func (s *Server) handleKernelTerminalInterrupt(w http.ResponseWriter, r *http.Request) {
	frameID := strings.TrimSpace(r.PathValue("frameId"))
	access, ok := s.kernelFrameRequestAccess(w, r, frameID, true)
	if !ok {
		return
	}
	result := s.kernelManager.InterruptSession(frameID, access.Frame.IncarnationID, access.RootFrameIncarnationID, strings.TrimSpace(r.PathValue("execId")))
	writeJSON(w, http.StatusOK, result)
}
func (s *Server) submitKernelTerminalExecution(access workspace.KernelFrameAccess, frameID, language, environment, code string) (string, string, error) {
	execID := uuid.NewString()
	toolUseID := "user-" + execID
	handle, err := s.kernelManager.Submit(kernelruntime.SubmitRequest{
		OwnerID: access.UserID, ProjectID: access.Frame.ProjectID, FrameID: frameID,
		FrameIncarnationID: access.Frame.IncarnationID, RootFrameIncarnationID: access.RootFrameIncarnationID,
		Language: language, Environment: environment,
		ExecID: execID, ToolUseID: toolUseID, Code: code, Origin: "user",
		Timeout: userTerminalCellTimeout,
	})
	if err != nil {
		if strings.HasPrefix(strings.ToLower(err.Error()), "no running ") {
			return "", "", fmt.Errorf(
				"No running %s kernel for environment '%s' on this frame \u2014 the terminal attaches to live kernels only. Ask the agent to run a cell first (or pick another kernel).",
				language, environment,
			)
		}
		return "", "", err
	}
	go s.observeKernelTerminalExecution(access, handle)
	return execID, toolUseID, nil
}

func (s *Server) compatKernelTerminalAck(ctx context.Context, userID string, message map[string]any) map[string]any {
	requestID, _ := message["request_id"].(string)
	ack := map[string]any{"type": "kernel_terminal_ack", "request_id": nil, "ok": false}
	if requestID != "" {
		ack["request_id"] = requestID
	}
	if s.workspaceStore == nil || s.kernelManager == nil {
		ack["error"] = "Verified kernel runtime is not available"
		return ack
	}
	frameID := strings.TrimSpace(stringValue(message["frame_id"]))
	access, found, err := s.workspaceStore.GetKernelFrameAccess(frameID)
	if err != nil {
		ack["error"] = "internal error"
		return ack
	}
	if !found || access.UserID != strings.TrimSpace(userID) || access.IsHidden {
		ack["error"] = "Frame " + frameID + " not found"
		return ack
	}
	switch stringValue(message["type"]) {
	case "kernel_user_exec":
		code, ok := message["code"].(string)
		if !ok {
			ack["error"] = "code must be a string"
			return ack
		}
		language := strings.ToLower(strings.TrimSpace(stringValue(message["language"])))
		environment := stringValue(message["environment"])
		if language != "python" && language != "r" {
			ack["error"] = "language must be 'python' or 'r'"
			return ack
		}
		if !kernelruntime.ValidEnvironmentName(environment) {
			ack["error"] = "environment must be 1-100 bytes using letters, digits, dot, underscore, or dash"
			return ack
		}
		if code == "" {
			ack["error"] = "code must not be empty"
			return ack
		}
		execID, toolUseID, err := s.submitKernelTerminalExecution(access, frameID, language, environment, code)
		if err != nil {
			ack["error"] = err.Error()
			return ack
		}
		ack["ok"], ack["exec_id"], ack["tool_use_id"] = true, execID, toolUseID
		return ack
	case "kernel_user_interrupt":
		result := s.kernelManager.InterruptSession(frameID, access.Frame.IncarnationID, access.RootFrameIncarnationID, strings.TrimSpace(stringValue(message["exec_id"])))
		ack["ok"] = true
		ack["interrupted"] = result.Interrupted
		ack["via"] = result.Via
		ack["dequeued"] = result.Dequeued
		ack["reason"] = result.Reason
		return ack
	default:
		ack["error"] = "unsupported kernel terminal request"
		return ack
	}
}

func (s *Server) kernelFrameRequestAccess(w http.ResponseWriter, r *http.Request, frameID string, requireVisible bool) (workspace.KernelFrameAccess, bool) {
	if s.workspaceStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Workspace runtime is not configured")
		return workspace.KernelFrameAccess{}, false
	}
	if s.kernelManager == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Verified kernel runtime is not available")
		return workspace.KernelFrameAccess{}, false
	}
	access, found, err := s.workspaceStore.GetKernelFrameAccess(frameID)
	if err != nil {
		writeV11StoreError(w, err)
		return workspace.KernelFrameAccess{}, false
	}
	if !found || access.UserID != compatAgentUserID(r) || requireVisible && access.IsHidden {
		writeV11Detail(w, http.StatusNotFound, "Frame "+frameID+" not found")
		return workspace.KernelFrameAccess{}, false
	}
	return access, true
}

func (s *Server) observeKernelTerminalExecution(access workspace.KernelFrameAccess, handle *kernelruntime.ExecutionHandle) {
	started, startedOK := <-handle.Started()
	if startedOK {
		if _, err := s.publishCompatEvent(workspace.RealtimeEventInput{
			ID:     "kernel-execution:" + started.ExecID + ":start",
			UserID: access.UserID, ProjectID: access.Frame.ProjectID,
			RootFrameID: access.Frame.RootFrameID, FrameID: access.Frame.ID,
			Type: "execution_cell_update",
			Payload: map[string]any{
				"phase": "start", "origin": started.Origin,
				"language": started.Language, "environment": started.Environment,
				"kernel_target": "analysis", "source": started.Code,
				"started_at":  started.StartedAt.UTC().Format(time.RFC3339Nano),
				"tool_use_id": started.ToolUseID,
			},
		}); err != nil {
			log.Printf("persist kernel start event %s: %v", started.ExecID, err)
		}
	}
	outcome, outcomeOK := <-handle.Done()
	if !outcomeOK || !startedOK || outcome.Dequeued {
		return
	}
	cancelled := outcome.Response.Interrupted || outcome.TimedOut
	exitStatus := "ok"
	var exitCode any = 0
	stderr := outcome.Response.Stderr
	if outcome.Response.Error != "" {
		if stderr != "" {
			stderr += "\n"
		}
		stderr += outcome.Response.Error
	}
	if cancelled {
		exitStatus, exitCode = "cancelled", nil
	} else if outcome.Err != nil || outcome.Response.Error != "" {
		exitStatus, exitCode = "error", -1
		if outcome.Err != nil {
			if stderr != "" {
				stderr += "\n"
			}
			stderr += outcome.Err.Error()
		}
	}
	if _, err := s.workspaceStore.SaveExecutionLog(workspace.SaveExecutionLogInput{
		Record: workspace.ExecutionLogRecord{
			ID: started.ExecID, FrameID: started.FrameID, CellIndex: outcome.CellIndex,
			KernelID: started.KernelID, KernelKind: "analysis", CondaEnv: started.Environment,
			Language: started.Language, Source: started.Code, Stdout: outcome.Response.Stdout,
			Stderr: stderr, ExitStatus: exitStatus, Origin: started.Origin,
			ExecutedAt: started.StartedAt, ErrorLine: kernelErrorLine(outcome.Response.Trace),
		},
		ExpectedOwnerID: access.UserID, ExpectedProjectID: access.Frame.ProjectID,
		ExpectedFrameIncarnationID:     access.Frame.IncarnationID,
		ExpectedRootFrameIncarnationID: access.RootFrameIncarnationID,
	}); err != nil {
		log.Printf("persist kernel execution log %s: %v", started.ExecID, err)
		return
	}
	duration := outcome.FinishedAt.Sub(started.StartedAt).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	if _, err := s.publishCompatEvent(workspace.RealtimeEventInput{
		ID:     "kernel-execution:" + started.ExecID + ":done",
		UserID: access.UserID, ProjectID: access.Frame.ProjectID,
		RootFrameID: access.Frame.RootFrameID, FrameID: access.Frame.ID,
		Type: "execution_cell_update",
		Payload: map[string]any{
			"phase": "done", "origin": started.Origin,
			"language": started.Language, "environment": started.Environment,
			"kernel_target": "analysis", "cell_id": started.ExecID,
			"exit_code": exitCode, "stdout": outcome.Response.Stdout,
			"stderr": stderr, "cancelled": cancelled, "duration_ms": duration,
			"tool_use_id": started.ToolUseID,
		},
	}); err != nil {
		log.Printf("persist kernel done event %s: %v", started.ExecID, err)
	}
}

func kernelErrorLine(trace map[string]any) *int {
	if trace == nil {
		return nil
	}
	switch value := trace["error_lineno"].(type) {
	case float64:
		line := int(value)
		return &line
	case int:
		line := value
		return &line
	default:
		return nil
	}
}

func (s *Server) handleRefreshKernels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	if s.kernelManager == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "verified kernel runtime is not available"})
		return
	}
	refreshContext, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	closed, err := s.kernelManager.Refresh(refreshContext)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error(), "closed_count": closed})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"closed_count": closed})
}

func (s *Server) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("server close context is required")
	}
	if s.mcpAppResourceTickets != nil {
		s.mcpAppResourceTickets.Close()
	}
	if s.mcpApps != nil {
		s.mcpApps.Close()
	}
	var closeErr error
	if s.mcpDirectoryOwned && s.mcpDirectory != nil {
		if err := s.mcpDirectory.Close(ctx); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
	}
	if err := s.stopTranscriptWebDelivery(ctx); err != nil {
		closeErr = errors.Join(closeErr, err)
	}
	if err := s.stopDataLifecycle(ctx); err != nil {
		closeErr = errors.Join(closeErr, err)
	}
	if err := s.Drain(ctx); err != nil {
		closeErr = errors.Join(closeErr, err)
	}
	if s.memoryExtraction != nil {
		drained := s.memoryExtraction.Drain(ctx)
		if drained.TimedOut || drained.Remaining > 0 {
			closeErr = errors.Join(closeErr, fmt.Errorf("memory extraction drain incomplete: remaining=%d", drained.Remaining))
		}
	}
	// Prevent a detached observer from being registered while the remaining
	// kernel and background workers are being shut down. The observers are
	// waited on after their child executions have received cancellation so they
	// cannot write terminal settlement into a store that is already closing.
	s.beginDetachedKernelObserverShutdown()
	if err := s.stopAllBackgroundShells(ctx); err != nil {
		closeErr = errors.Join(closeErr, err)
	}
	if err := s.stopAllWebOfficeWatches(ctx); err != nil {
		closeErr = errors.Join(closeErr, err)
	}
	if err := s.cancelAllKernelChildRuns(ctx); err != nil {
		closeErr = errors.Join(closeErr, err)
	}
	if err := s.waitDetachedKernelObservers(ctx); err != nil {
		closeErr = errors.Join(closeErr, err)
	}
	if s.kernelManager != nil {
		_, err := s.kernelManager.Refresh(ctx)
		if err != nil {
			closeErr = errors.Join(closeErr, err)
		}
	}
	if s.runtimeStoreOwned && s.runtimeStore != nil {
		if err := s.runtimeStore.Close(); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
		s.runtimeStore = nil
	}
	return closeErr
}
