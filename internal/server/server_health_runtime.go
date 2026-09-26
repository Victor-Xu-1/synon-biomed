package server

import (
	"net/http"

	"synon-go/internal/buildinfo"
)

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	info := buildinfo.Release()
	ready := s != nil && s.agentCatalog != nil && s.agentCatalog.Ready() && s.agentCatalogError == nil
	agentCount := 0
	if s != nil && s.agentCatalog != nil {
		agentCount = len(s.agentCatalog.Agents())
	}
	status := "healthy"
	httpStatus := http.StatusOK
	degradedComponents := s.degradedRuntimeComponents()
	activeSessionRuns, runtimeDraining := s.sessionRunActivity()
	activeDurableRunnerAttempts := s.activeDurableRunnerAttemptCount()
	activeKernelExecutions := s.activeKernelExecutionCount()
	activeKernelOperations := s.activeKernelOperationCount()
	activeRunnerWork := max(activeSessionRuns, activeDurableRunnerAttempts)
	activeKernelWork := max(activeKernelExecutions, activeKernelOperations)
	activeRuntimeWork := activeRunnerWork + activeKernelWork
	if !ready || len(degradedComponents) > 0 {
		status = "degraded"
		httpStatus = http.StatusServiceUnavailable
	}
	response := map[string]any{
		"status":                     status,
		"service":                    "gateway",
		"name":                       info.Name,
		"version":                    info.Version,
		"agents_registered":          agentCount,
		"agent_catalog_ready":        ready,
		"image_processing_available": false,
		// active_session_runs remains the backwards-compatible deployment gate.
		// It includes kernel work that can outlive one runner cycle so source and
		// deployment supervisors cannot reload through an in-flight operation.
		"active_session_runs":        activeRuntimeWork,
		"active_runner_sessions":     activeSessionRuns,
		"active_runner_attempts":     activeDurableRunnerAttempts,
		"active_kernel_executions":   activeKernelExecutions,
		"active_kernel_operations":   activeKernelOperations,
		"runtime_draining":           runtimeDraining,
		"scientific_runtime_warmups": s.scientificRuntimeWarmupsHealth(),
	}
	scientificCore := s.scientificCoreRuntimeHealth()
	response["scientific_runtime_ready"] = scientificCore["ready"]
	response["scientific_runtime_core"] = scientificCore
	if len(degradedComponents) > 0 {
		response["degraded_components"] = degradedComponents
	}
	if s != nil && s.agentCatalogError != nil {
		response["agent_catalog_error"] = s.agentCatalogError.Error()
	}
	writeJSON(w, httpStatus, response)
}

func (s *Server) handleCapabilities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.capabilities)
}
