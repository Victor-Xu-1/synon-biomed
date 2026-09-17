package server

import (
	"net/http"

	"synon-go/internal/buildinfo"
	"synon-go/internal/vmresources"
)

type setVMResourcesInput struct {
	MemoryGB int `json:"memoryGB"`
	CPUCount int `json:"cpuCount"`
}

func (s *Server) handleVMResources(w http.ResponseWriter, r *http.Request) {
	controller, ok := s.vmResourcesForRequest(w)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		resources, err := controller.Get()
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		resources.IsRestarting = s.vmRestart != nil && s.vmRestart.IsRestarting()
		writeWorkspaceJSON(w, http.StatusOK, resources)
	case http.MethodPut:
		var input setVMResourcesInput
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		resources, err := controller.Set(input.MemoryGB, input.CPUCount)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		resources.IsRestarting = s.vmRestart != nil && s.vmRestart.IsRestarting()
		writeWorkspaceJSON(w, http.StatusOK, resources)
	default:
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (s *Server) handleVMRestart(w http.ResponseWriter, r *http.Request) {
	if s.vmRestart == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "WSL VM restart manager is not configured"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		status, err := s.vmRestart.Status()
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, status)
	case http.MethodPost:
		status, err := s.vmRestart.Restart(r.Context())
		if err != nil {
			writeWorkspaceJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusAccepted, map[string]any{
			"ok": true, "status": status,
			"message": "Windows host restart supervisor launched; WSL will shut down and the managed " + buildinfo.Release().Name + " service will recover automatically.",
		})
	default:
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (s *Server) vmResourcesForRequest(w http.ResponseWriter) (*vmresources.Controller, bool) {
	if s.vmResources == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "VM resource controller is not configured"})
		return nil, false
	}
	return s.vmResources, true
}
