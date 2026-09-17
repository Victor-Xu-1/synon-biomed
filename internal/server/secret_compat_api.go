package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	secretstore "synon-go/internal/persistence/secrets"
)

type compatibilityNullable[T any] struct {
	Present bool
	Null    bool
	Value   T
}

func (s *Server) handleCompatibilitySecrets(w http.ResponseWriter, r *http.Request) {
	store, ok := s.secretStoreForCompatibilityRequest(w)
	if !ok {
		return
	}
	userID := compatAgentUserID(r)
	switch r.Method {
	case http.MethodGet:
		items, err := store.ListForUser(userID)
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		result := make([]compatibilitySecretProjection, 0, len(items))
		for _, item := range items {
			if isInternalSecret(item) {
				continue
			}
			result = append(result, projectCompatibilitySecret(item))
		}
		writeJSON(w, http.StatusOK, result)
	case http.MethodPost:
		body, err := decodeCompatibilitySecretBody(r)
		if err != nil {
			writeV11Detail(w, http.StatusBadRequest, "Invalid arguments: "+err.Error())
			return
		}
		created, err := createCompatibilitySecret(store, userID, body)
		if err != nil {
			writeCompatibilitySecretError(w, err, "")
			return
		}
		writeJSON(w, http.StatusCreated, projectCompatibilitySecret(created))
	default:
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (s *Server) handleCompatibilitySecretMutation(w http.ResponseWriter, r *http.Request) {
	store, ok := s.secretStoreForCompatibilityRequest(w)
	if !ok {
		return
	}
	id, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/api/secrets/"))
	if err != nil || strings.TrimSpace(id) == "" || strings.Contains(id, "/") {
		writeV11Detail(w, http.StatusBadRequest, "Invalid secret id")
		return
	}
	if r.Method != http.MethodDelete && r.Method != http.MethodPatch {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	userID := compatAgentUserID(r)
	existing, found, resolveErr := store.ResolveForUser(id, userID)
	if resolveErr != nil {
		writeV11StoreError(w, resolveErr)
		return
	}
	if !found || isInternalSecret(existing) {
		writeV11Detail(w, http.StatusNotFound, "Secret "+id+" not found")
		return
	}
	if r.Method == http.MethodDelete {
		removed, err := store.DeleteForUser(id, userID)
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		if !removed {
			writeV11Detail(w, http.StatusNotFound, "Secret "+id+" not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
		return
	}
	body, err := decodeCompatibilitySecretBody(r)
	if err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid arguments: "+err.Error())
		return
	}
	updated, err := updateCompatibilitySecret(store, userID, id, body)
	if err != nil {
		writeCompatibilitySecretError(w, err, id)
		return
	}
	writeJSON(w, http.StatusOK, projectCompatibilitySecret(updated))
}

func (s *Server) secretStoreForCompatibilityRequest(w http.ResponseWriter) (*secretstore.Store, bool) {
	if s == nil || s.secretStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Secret vault is not configured")
		return nil, false
	}
	return s.secretStore, true
}

func createCompatibilitySecret(store *secretstore.Store, userID string, body map[string]json.RawMessage) (secretstore.Secret, error) {
	provider, err := compatibilityOptionalString(body, "provider", false)
	if err != nil {
		return secretstore.Secret{}, err
	}
	providerName := "generic"
	if provider.Present {
		providerName = provider.Value
	}
	name, err := compatibilityOptionalString(body, "name", true)
	if err != nil {
		return secretstore.Secret{}, err
	}
	value, err := compatibilityOptionalString(body, "value", true)
	if err != nil {
		return secretstore.Secret{}, err
	}
	credentials, err := compatibilityOptionalObject(body, "credentials")
	if err != nil {
		return secretstore.Secret{}, err
	}
	description, err := compatibilityOptionalString(body, "description", true)
	if err != nil {
		return secretstore.Secret{}, err
	}
	credentialType, err := compatibilityOptionalString(body, "credential_type", true)
	if err != nil {
		return secretstore.Secret{}, err
	}
	buckets, err := compatibilityOptionalStrings(body, "buckets")
	if err != nil {
		return secretstore.Secret{}, err
	}
	region, err := compatibilityOptionalString(body, "region", true)
	if err != nil {
		return secretstore.Secret{}, err
	}
	if err := validateCompatibilitySecretEnvelope(providerName, name, value, credentials, description, credentialType, region); err != nil {
		return secretstore.Secret{}, err
	}
	if _, ok := compatibilitySecretProviders[providerName]; !ok {
		return secretstore.Secret{}, compatibilitySecretErrorf("Invalid provider: %s", providerName)
	}

	candidate := secretstore.Secret{ID: uuid.NewString(), UserID: userID, Provider: providerName}
	if description.Present && !description.Null {
		candidate.Description, candidate.DescriptionPresent = description.Value, true
	}
	if buckets.Present && !buckets.Null {
		candidate.Buckets, candidate.BucketsPresent = append([]string{}, buckets.Value...), true
	}
	if region.Present && !region.Null {
		candidate.Region, candidate.RegionPresent = region.Value, true
	}
	if providerName == "generic" {
		if !name.Present || name.Null || !value.Present || value.Null {
			return secretstore.Secret{}, compatibilitySecretErrorf("Generic secrets require both 'name' and 'value'")
		}
		canonical, err := compatibilityGenericSecretName(name.Value)
		if err != nil {
			return secretstore.Secret{}, err
		}
		candidate.Name, candidate.Value = canonical, value.Value
	} else {
		if !credentials.Present || credentials.Null {
			return secretstore.Secret{}, compatibilitySecretErrorf("Provider '%s' requires 'credentials'", providerName)
		}
		if err := validateCompatibilityProviderCredentials(providerName, credentials.Value); err != nil {
			return secretstore.Secret{}, err
		}
		if providerName == "literature" {
			delete(credentials.Value, "email")
		}
		if name.Present && !name.Null {
			candidate.Name = compatibilityProviderSecretName(name.Value)
		} else {
			candidate.Name = compatibilitySecretProviders[providerName]
		}
		if err := candidate.SetCredentialObject(credentials.Value); err != nil {
			return secretstore.Secret{}, err
		}
	}
	if credentialType.Present && !credentialType.Null {
		candidate.CredentialType, candidate.CredentialTypePresent = credentialType.Value, true
	} else if providerName == "azure" && credentials.Present && !credentials.Null {
		if _, ok := credentials.Value["connection_string"].(string); ok {
			candidate.CredentialType, candidate.CredentialTypePresent = "connection_string", true
		} else if _, ok := credentials.Value["client_id"].(string); ok {
			candidate.CredentialType, candidate.CredentialTypePresent = "client_secret", true
		}
	}
	autoSuffix := compatibilityMultiSecretProviders[providerName] && (!name.Present || name.Null)
	return store.CreateWithOptions(candidate, secretstore.CreateOptions{
		PreserveNameWhitespace: true,
		Validate: func(existing []secretstore.Secret, candidate *secretstore.Secret) error {
			if providerName != "generic" && !compatibilityMultiSecretProviders[providerName] {
				for _, item := range existing {
					if item.UserID == userID && item.Provider == providerName {
						return compatibilitySecretErrorf("A %s secret already exists for this user", compatibilitySecretProviders[providerName])
					}
				}
			}
			if autoSuffix {
				base := candidate.Name
				for suffix := 1; compatibilitySecretNameExists(existing, userID, candidate.Name, ""); suffix++ {
					candidate.Name = fmt.Sprintf("%s %d", base, suffix+1)
				}
				return nil
			}
			if compatibilitySecretNameExists(existing, userID, candidate.Name, "") {
				return compatibilitySecretErrorf("Secret '%s' already exists", candidate.Name)
			}
			return nil
		},
	})
}

func updateCompatibilitySecret(
	store *secretstore.Store, userID, id string, body map[string]json.RawMessage,
) (secretstore.Secret, error) {
	value, err := compatibilityOptionalString(body, "value", true)
	if err != nil {
		return secretstore.Secret{}, err
	}
	credentials, err := compatibilityOptionalObject(body, "credentials")
	if err != nil {
		return secretstore.Secret{}, err
	}
	name, err := compatibilityOptionalString(body, "name", true)
	if err != nil {
		return secretstore.Secret{}, err
	}
	description, err := compatibilityOptionalString(body, "description", true)
	if err != nil {
		return secretstore.Secret{}, err
	}
	buckets, err := compatibilityOptionalStrings(body, "buckets")
	if err != nil {
		return secretstore.Secret{}, err
	}
	region, err := compatibilityOptionalString(body, "region", true)
	if err != nil {
		return secretstore.Secret{}, err
	}
	if err := validateCompatibilitySecretUpdateEnvelope(value, credentials, name, description, region); err != nil {
		return secretstore.Secret{}, err
	}
	return store.UpdateForUserWithOptions(id, userID, func(candidate *secretstore.Secret) error {
		if credentials.Present && !credentials.Null && candidate.Provider != "generic" {
			updatedCredentials := credentials.Value
			if compatibilityCredentialsMerge(candidate.Provider, candidate.CredentialType) {
				updatedCredentials = candidate.CredentialObject()
				for key, item := range credentials.Value {
					updatedCredentials[key] = item
				}
			}
			if err := validateCompatibilityProviderCredentials(candidate.Provider, updatedCredentials); err != nil {
				return err
			}
			if candidate.Provider == "literature" {
				delete(updatedCredentials, "email")
			}
			if err := candidate.SetCredentialObject(updatedCredentials); err != nil {
				return err
			}
		} else if value.Present && !value.Null {
			if candidate.Provider != "generic" {
				return compatibilitySecretErrorf("Cannot set 'value' on a non-generic secret")
			}
			candidate.Value = value.Value
		}
		if name.Present && !name.Null {
			if candidate.Provider == "generic" {
				canonical, err := compatibilityGenericSecretName(name.Value)
				if err != nil {
					return err
				}
				candidate.Name = canonical
			} else {
				candidate.Name = strings.TrimSpace(compatibilityProviderSecretName(name.Value))
				if candidate.Name == "" {
					return compatibilitySecretErrorf("name must not be empty")
				}
			}
		}
		if description.Present {
			candidate.DescriptionPresent = !description.Null
			candidate.Description = description.Value
		}
		if buckets.Present {
			candidate.BucketsPresent = !buckets.Null
			candidate.Buckets = nil
			if !buckets.Null {
				candidate.Buckets = append([]string{}, buckets.Value...)
			}
		}
		if region.Present {
			candidate.RegionPresent = !region.Null
			candidate.Region = region.Value
		}
		return nil
	}, secretstore.UpdateOptions{
		SkipNoop:               true,
		PreserveNameWhitespace: true,
		Validate: func(existing []secretstore.Secret, original secretstore.Secret, candidate *secretstore.Secret) error {
			if candidate.Name != original.Name && compatibilitySecretNameExists(existing, userID, candidate.Name, original.ID) {
				return compatibilitySecretErrorf("Secret '%s' already exists", candidate.Name)
			}
			return nil
		},
	})
}

func decodeCompatibilitySecretBody(r *http.Request) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	var body map[string]json.RawMessage
	if err := decoder.Decode(&body); err != nil {
		return nil, err
	}
	if body == nil {
		return nil, errors.New("request body must be an object")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, errors.New("request body must contain one JSON value")
		}
		return nil, err
	}
	return body, nil
}

func compatibilityOptionalString(body map[string]json.RawMessage, key string, nullable bool) (compatibilityNullable[string], error) {
	raw, present := body[key]
	if !present {
		return compatibilityNullable[string]{}, nil
	}
	result := compatibilityNullable[string]{Present: true}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if !nullable {
			return result, compatibilitySecretErrorf("%s must be a string", key)
		}
		result.Null = true
		return result, nil
	}
	if err := json.Unmarshal(raw, &result.Value); err != nil {
		return result, compatibilitySecretErrorf("%s must be a string", key)
	}
	return result, nil
}

func compatibilityOptionalObject(body map[string]json.RawMessage, key string) (compatibilityNullable[map[string]any], error) {
	raw, present := body[key]
	if !present {
		return compatibilityNullable[map[string]any]{}, nil
	}
	result := compatibilityNullable[map[string]any]{Present: true}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		result.Null = true
		return result, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&result.Value); err != nil || result.Value == nil {
		return result, compatibilitySecretErrorf("%s must be an object", key)
	}
	return result, nil
}

func compatibilityOptionalStrings(body map[string]json.RawMessage, key string) (compatibilityNullable[[]string], error) {
	raw, present := body[key]
	if !present {
		return compatibilityNullable[[]string]{}, nil
	}
	result := compatibilityNullable[[]string]{Present: true}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		result.Null = true
		return result, nil
	}
	if err := json.Unmarshal(raw, &result.Value); err != nil {
		return result, compatibilitySecretErrorf("%s must be an array of strings", key)
	}
	if result.Value == nil {
		result.Value = []string{}
	}
	return result, nil
}

func writeCompatibilitySecretError(w http.ResponseWriter, err error, id string) {
	var validation compatibilitySecretValidationError
	switch {
	case errors.Is(err, secretstore.ErrSecretNotFound):
		writeV11Detail(w, http.StatusNotFound, "Secret "+id+" not found")
	case errors.As(err, &validation):
		writeV11Detail(w, http.StatusBadRequest, validation.Error())
	default:
		writeV11StoreError(w, err)
	}
}
