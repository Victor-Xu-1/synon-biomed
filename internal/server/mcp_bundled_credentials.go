package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	secretstore "synon-go/internal/persistence/secrets"
)

const maxBundledMCPAPIKeyBytes = 16 * 1024

type bundledMCPAPIKeyRequest struct {
	APIKey string `json:"apiKey"`
}

func bundledMCPAPIKeySecretID(userID, connectorID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(userID) + "\x00" + strings.TrimSpace(connectorID)))
	return "mcp-bundled-api-key-" + hex.EncodeToString(digest[:])
}

func (s *Server) bundledMCPAPIKey(userID, connectorID string) (string, bool, error) {
	if s == nil || s.secretStore == nil {
		return "", false, errors.New("encrypted secret store is unavailable")
	}
	secret, found, err := s.secretStore.ResolveForUser(bundledMCPAPIKeySecretID(userID, connectorID), userID)
	if err != nil || !found {
		return "", found, err
	}
	value := strings.TrimSpace(secret.Value)
	return value, value != "", nil
}

func (s *Server) saveBundledMCPAPIKey(userID, connectorID, value string) error {
	if s == nil || s.secretStore == nil {
		return errors.New("encrypted secret store is unavailable")
	}
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxBundledMCPAPIKeyBytes || strings.ContainsAny(value, "\r\n") {
		return errors.New("API key must be non-empty, single-line, and at most 16 KiB")
	}
	id := bundledMCPAPIKeySecretID(userID, connectorID)
	if _, found, err := s.secretStore.ResolveForUser(id, userID); err != nil {
		return err
	} else if found {
		_, err = s.secretStore.UpdateForUser(id, userID, func(secret *secretstore.Secret) error {
			secret.Value = value
			return nil
		})
		return err
	}
	_, err := s.secretStore.Create(secretstore.Secret{
		ID: id, UserID: userID, Provider: "mcp-bundled",
		Name: connectorID, Value: value,
		Description: "Encrypted owner-scoped API key for a bundled remote MCP connector",
	})
	return err
}

func (s *Server) deleteBundledMCPAPIKey(userID, connectorID string) error {
	if s == nil || s.secretStore == nil {
		return errors.New("encrypted secret store is unavailable")
	}
	_, err := s.secretStore.DeleteForUser(bundledMCPAPIKeySecretID(userID, connectorID), userID)
	return err
}

func (s *Server) handleBundledMCPAPIKey(w http.ResponseWriter, r *http.Request, userID, connectorID string) {
	if _, found := s.mcpDirectory.BundledAPIKeySpec(connectorID); !found {
		writeError(w, http.StatusBadRequest, "MCP_API_KEY_UNSUPPORTED", "connector does not support API-key configuration")
		return
	}
	var body bundledMCPAPIKeyRequest
	if err := decodeMCPDirectoryBody(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	if err := s.saveBundledMCPAPIKey(userID, connectorID, body.APIKey); err != nil {
		writeMCPDirectoryError(w, err)
		return
	}
	connector, err := s.mcpDirectory.SetUnifiedEnabled(r.Context(), userID, connectorID, true)
	if err != nil {
		writeMCPDirectoryError(w, err)
		return
	}
	if _, err := s.publishUserEvent(userID, "connector_status", map[string]any{
		"connector_id": connectorID, "status": connector.ConnectionStatus, "credential": "configured",
	}); err != nil {
		writeDomainEventError(w, "MCP connector credential update", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"apiKeyConfigured": true, "connector": connector})
}
