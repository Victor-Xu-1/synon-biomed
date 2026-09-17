package server

import (
	"net/http"

	"synon-go/internal/account"
)

// handleAccountManagement exposes the versioned read model used by the
// account workbench. It deliberately composes existing stores through this
// adapter; the UI never reads the private JSON stores directly. A future
// central identity service can replace this adapter without changing the
// workbench contract.
func (s *Server) handleAccountManagement(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"success": false,
			"message": "GET is required",
		})
		return
	}

	user, ok := s.webUser(r)
	if !ok {
		writeWorkspaceJSON(w, http.StatusUnauthorized, map[string]any{
			"success": false,
			"message": "authentication required",
		})
		return
	}

	if s.accountManagementReader == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{
			"success": false,
			"message": "account management is not configured",
		})
		return
	}
	snapshot, err := s.accountManagementReader.Read(r.Context(), account.ManagementReadRequest{
		AccountID: user.ID, SessionToken: webSessionCookieToken(r),
	})
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{
			"success": false,
			"message": "account management is temporarily unavailable",
		})
		return
	}
	if err := snapshot.Validate(); err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{
			"success": false,
			"message": "account management contract is invalid",
		})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"data":    snapshot,
	})
}
