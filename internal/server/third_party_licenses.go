package server

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"net/http"
)

const thirdPartyLicenseSource = "runtime/assets/skills/THIRD_PARTY_LICENSES.md"

//go:embed assets/THIRD_PARTY_LICENSES.md
var thirdPartyLicenseContent []byte

func (s *Server) handleThirdPartyLicenses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	digest := sha256.Sum256(thirdPartyLicenseContent)
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"title":   "Synon Biomed Third-Party Licenses",
		"source":  thirdPartyLicenseSource,
		"bytes":   len(thirdPartyLicenseContent),
		"sha256":  hex.EncodeToString(digest[:]),
		"content": string(thirdPartyLicenseContent),
	})
}
