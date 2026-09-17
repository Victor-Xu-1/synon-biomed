package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	secretstore "synon-go/internal/persistence/secrets"
)

const maxSecretPayloadCharacters = 64 * 1024

var supportedSecretProviders = map[string]struct{}{
	"generic": {}, "github": {}, "aws": {}, "gcp": {}, "azure": {},
	"literature": {}, "modal": {},
}

type secretProjection struct {
	ID                    string    `json:"id"`
	Provider              string    `json:"provider"`
	Name                  string    `json:"name,omitempty"`
	Description           string    `json:"description,omitempty"`
	CredentialType        string    `json:"credentialType,omitempty"`
	CredentialTypeLegacy  string    `json:"credential_type,omitempty"`
	Buckets               []string  `json:"buckets,omitempty"`
	Region                string    `json:"region,omitempty"`
	ValueConfigured       bool      `json:"valueConfigured"`
	CredentialsConfigured bool      `json:"credentialsConfigured"`
	CredentialFields      []string  `json:"credentialFields"`
	MaskedPreview         string    `json:"masked_preview,omitempty"`
	MaskedFields          []string  `json:"masked_fields"`
	CreatedAt             time.Time `json:"createdAt"`
	UpdatedAt             time.Time `json:"updatedAt"`
	CreatedAtLegacy       time.Time `json:"created_at"`
	UpdatedAtLegacy       time.Time `json:"updated_at"`
}

type createSecretInput struct {
	ID                   string            `json:"id"`
	Provider             string            `json:"provider"`
	Name                 string            `json:"name"`
	Value                *string           `json:"value"`
	Credentials          map[string]string `json:"credentials"`
	Description          string            `json:"description"`
	CredentialType       string            `json:"credentialType"`
	CredentialTypeLegacy string            `json:"credential_type"`
	Buckets              []string          `json:"buckets"`
	Region               string            `json:"region"`
}

type updateSecretInput struct {
	Provider             *string            `json:"provider"`
	Name                 *string            `json:"name"`
	Value                *string            `json:"value"`
	Credentials          *map[string]string `json:"credentials"`
	Description          *string            `json:"description"`
	CredentialType       *string            `json:"credentialType"`
	CredentialTypeLegacy *string            `json:"credential_type"`
	Buckets              *[]string          `json:"buckets"`
	Region               *string            `json:"region"`
}

func (s *Server) handleSecrets(w http.ResponseWriter, r *http.Request) {
	store, ok := s.secretStoreForRequest(w)
	if !ok {
		return
	}
	userID := secretUserID(r)
	switch r.Method {
	case http.MethodGet:
		items, err := store.ListForUser(userID)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		secrets := make([]secretProjection, 0, len(items))
		for _, item := range items {
			if isInternalSecret(item) {
				continue
			}
			secrets = append(secrets, projectSecret(item))
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "secrets": secrets})
	case http.MethodPost:
		var input createSecretInput
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if strings.TrimSpace(input.ID) == "" {
			input.ID = uuid.NewString()
		}
		provider := strings.ToLower(strings.TrimSpace(input.Provider))
		if provider == "" {
			provider = "generic"
		}
		credentialType := input.CredentialType
		if credentialType == "" {
			credentialType = input.CredentialTypeLegacy
		}
		if credentialType == "" && provider == "azure" {
			if _, ok := input.Credentials["connection_string"]; ok {
				credentialType = "connection_string"
			} else if _, ok := input.Credentials["client_id"]; ok {
				credentialType = "client_secret"
			}
		}
		value := ""
		if input.Value != nil {
			value = *input.Value
		}
		candidate := secretstore.Secret{
			ID: input.ID, UserID: userID, Provider: provider, Name: input.Name, Value: value,
			Credentials: input.Credentials, Description: input.Description,
			CredentialType: credentialType, Buckets: input.Buckets, Region: input.Region,
		}
		if err := validateSecret(candidate, input.Value != nil, true); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		created, err := store.Create(candidate)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "secret": projectSecret(created)})
	default:
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
	}
}

func (s *Server) handleSecret(w http.ResponseWriter, r *http.Request) {
	store, ok := s.secretStoreForRequest(w)
	if !ok {
		return
	}
	id, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/api/go/secrets/"))
	if err != nil || invalidSecretID(id) {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid secret id"})
		return
	}
	userID := secretUserID(r)
	switch r.Method {
	case http.MethodGet:
		secret, found, err := store.ResolveForUser(id, userID)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if !found || isInternalSecret(secret) {
			writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "secret not found"})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "secret": projectSecret(secret)})
	case http.MethodPatch:
		var input updateSecretInput
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		existing, found, resolveErr := store.ResolveForUser(id, userID)
		if resolveErr != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": resolveErr.Error()})
			return
		} else if !found || isInternalSecret(existing) {
			writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "secret not found"})
			return
		}
		updated, err := store.UpdateForUser(id, userID, func(secret *secretstore.Secret) error {
			applySecretUpdate(secret, input)
			return validateSecret(*secret, input.Value != nil, false)
		})
		if err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "secret": projectSecret(updated)})
	case http.MethodDelete:
		existing, found, resolveErr := store.ResolveForUser(id, userID)
		if resolveErr != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": resolveErr.Error()})
			return
		}
		if !found || isInternalSecret(existing) {
			writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "secret not found"})
			return
		}
		removed, err := store.DeleteForUser(id, userID)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if !removed {
			writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "secret not found"})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": true, "id": id})
	default:
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
	}
}

func (s *Server) secretStoreForRequest(w http.ResponseWriter) (*secretstore.Store, bool) {
	if s.secretStore == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "secret vault is not configured"})
		return nil, false
	}
	return s.secretStore, true
}

func secretUserID(r *http.Request) string {
	userID := strings.TrimSpace(resolveUserID(r, nil))
	if userID == "" {
		return secretstore.DefaultUserID
	}
	return userID
}

func applySecretUpdate(secret *secretstore.Secret, input updateSecretInput) {
	if input.Provider != nil {
		secret.Provider = strings.ToLower(strings.TrimSpace(*input.Provider))
	}
	if input.Name != nil {
		secret.Name = *input.Name
	}
	if input.Value != nil {
		secret.Value = *input.Value
	}
	if input.Credentials != nil {
		secret.Credentials = *input.Credentials
	}
	if input.Description != nil {
		secret.Description = *input.Description
	}
	if input.CredentialType != nil {
		secret.CredentialType = *input.CredentialType
	} else if input.CredentialTypeLegacy != nil {
		secret.CredentialType = *input.CredentialTypeLegacy
	}
	if input.Buckets != nil {
		secret.Buckets = *input.Buckets
	}
	if input.Region != nil {
		secret.Region = *input.Region
	}
}

func validateSecret(secret secretstore.Secret, valueProvided, creating bool) error {
	if invalidSecretID(secret.ID) || isReservedInternalSecretID(secret.ID) {
		return errorsForSecretField("id", 128)
	}
	if _, ok := supportedSecretProviders[strings.ToLower(strings.TrimSpace(secret.Provider))]; !ok {
		return fmt.Errorf("invalid provider: %s", secret.Provider)
	}
	if creating && valueProvided && secret.Value == "" {
		return fmt.Errorf("value must be at least 1 character")
	}
	if utf8.RuneCountInString(secret.Value) > maxSecretPayloadCharacters {
		return fmt.Errorf("value payload too large (max %d characters)", maxSecretPayloadCharacters)
	}
	credentials, err := json.Marshal(secret.Credentials)
	if err != nil {
		return fmt.Errorf("encode credentials: %w", err)
	}
	if secret.Credentials != nil && utf8.RuneCount(credentials) > maxSecretPayloadCharacters {
		return fmt.Errorf("credentials payload too large (max %d characters)", maxSecretPayloadCharacters)
	}
	for field := range secret.Credentials {
		if field == "" || utf8.RuneCountInString(field) > 128 || containsControl(field) {
			return fmt.Errorf("credential field names must be 1-128 printable characters")
		}
	}
	if utf8.RuneCountInString(secret.Name) > 128 {
		return errorsForSecretField("name", 128)
	}
	if utf8.RuneCountInString(secret.Description) > 256 {
		return errorsForSecretField("description", 256)
	}
	if utf8.RuneCountInString(secret.CredentialType) > 32 {
		return errorsForSecretField("credential_type", 32)
	}
	if utf8.RuneCountInString(secret.Region) > 64 {
		return errorsForSecretField("region", 64)
	}
	return nil
}

func errorsForSecretField(field string, limit int) error {
	return fmt.Errorf("%s must be at most %d characters", field, limit)
}

func invalidSecretID(id string) bool {
	id = strings.TrimSpace(id)
	return id == "" || utf8.RuneCountInString(id) > 128 || strings.Contains(id, "/") || containsControl(id)
}

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func projectSecret(secret secretstore.Secret) secretProjection {
	fields := make([]string, 0, len(secret.Credentials))
	for field := range secret.Credentials {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	maskedPreview := ""
	if secret.Value != "" || len(secret.Credentials) > 0 {
		maskedPreview = "configured"
	}
	return secretProjection{
		ID: secret.ID, Provider: secret.Provider, Name: secret.Name,
		Description: secret.Description, CredentialType: secret.CredentialType,
		CredentialTypeLegacy: secret.CredentialType,
		Buckets:              append([]string(nil), secret.Buckets...), Region: secret.Region,
		ValueConfigured: secret.Value != "", CredentialsConfigured: len(secret.Credentials) > 0,
		CredentialFields: append([]string(nil), fields...), MaskedPreview: maskedPreview,
		MaskedFields: fields, CreatedAt: secret.CreatedAt, UpdatedAt: secret.UpdatedAt,
		CreatedAtLegacy: secret.CreatedAt, UpdatedAtLegacy: secret.UpdatedAt,
	}
}
