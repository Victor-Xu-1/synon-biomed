package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"synon-go/internal/cloudstore"
	"synon-go/internal/compute"
	secretstore "synon-go/internal/persistence/secrets"
	workspace "synon-go/internal/persistence/workspace"
)

type cloudCredentialProjection struct {
	ID             string `json:"id"`
	Provider       string `json:"provider"`
	Name           string `json:"name"`
	CredentialType string `json:"credential_type"`
	Connected      bool   `json:"is_connected"`
	DefaultBucket  any    `json:"default_bucket"`
	Region         string `json:"region,omitempty"`
	CreatedAt      any    `json:"created_at"`
	UpdatedAt      any    `json:"updated_at"`
}

type cloudImportInput struct {
	Bucket    string `json:"bucket"`
	Key       string `json:"key"`
	ProjectID string `json:"project_id"`
}

type cloudExportInput struct {
	ArtifactID string `json:"artifact_id"`
	Bucket     string `json:"bucket"`
	Key        string `json:"key"`
}

func (s *Server) handleCloudCredentials(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/cloud-credentials" {
		writeCloudError(w, http.StatusNotFound, "cloud credential endpoint not found")
		return
	}
	if r.Method != http.MethodGet {
		writeCloudError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	store, userID, ok := s.cloudRequestContext(w, r)
	if !ok {
		return
	}
	items, err := store.ListForUser(userID)
	if err != nil {
		writeCloudStoreError(w, err)
		return
	}
	result := make([]cloudCredentialProjection, 0, len(items))
	for _, item := range items {
		if isCloudProvider(item.Provider) {
			result = append(result, projectCloudCredential(item))
		}
	}
	writeWorkspaceJSON(w, http.StatusOK, result)
}

func (s *Server) handleCloudCredential(w http.ResponseWriter, r *http.Request) {
	store, userID, ok := s.cloudRequestContext(w, r)
	if !ok {
		return
	}
	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/cloud-credentials/"))
	if len(segments) < 1 || len(segments) > 2 {
		writeCloudError(w, http.StatusNotFound, "cloud credential endpoint not found")
		return
	}
	id, err := url.PathUnescape(segments[0])
	if err != nil || strings.TrimSpace(id) == "" {
		writeCloudError(w, http.StatusBadRequest, "invalid cloud credential id")
		return
	}
	secret, found, err := store.ResolveForUser(id, userID)
	if err != nil {
		writeCloudStoreError(w, err)
		return
	}
	if !found || !isCloudProvider(secret.Provider) {
		writeCloudError(w, http.StatusNotFound, "Cloud credential not found")
		return
	}
	if len(segments) == 1 {
		switch r.Method {
		case http.MethodGet:
			writeWorkspaceJSON(w, http.StatusOK, projectCloudCredential(secret))
		case http.MethodDelete:
			removed, err := store.DeleteForUser(id, userID)
			if err != nil {
				writeCloudStoreError(w, err)
				return
			}
			writeWorkspaceJSON(w, http.StatusOK, map[string]any{"deleted": removed, "id": id})
		default:
			writeCloudError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	client, err := s.cloudClient(secret)
	if err != nil {
		writeCloudError(w, http.StatusBadRequest, err.Error())
		return
	}
	action := segments[1]
	switch action {
	case "test":
		s.handleCloudTest(w, r, secret, client)
	case "buckets":
		s.handleCloudBuckets(w, r, client)
	case "objects":
		s.handleCloudObjects(w, r, client)
	case "folder":
		s.handleCloudFolder(w, r, client)
	case "download":
		s.handleCloudDownload(w, r, client)
	case "import":
		s.handleCloudImport(w, r, userID, client)
	case "export":
		s.handleCloudExport(w, r, userID, client)
	default:
		writeCloudError(w, http.StatusNotFound, "cloud credential endpoint not found")
	}
}

func (s *Server) handleCloudTest(w http.ResponseWriter, r *http.Request, secret secretstore.Secret, client cloudstore.Client) {
	if r.Method != http.MethodPost {
		writeCloudError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if len(secret.Buckets) > 0 && strings.TrimSpace(secret.Buckets[0]) != "" {
		bucket := strings.TrimSpace(secret.Buckets[0])
		if _, err := client.ListPage(r.Context(), bucket, "", "", 1, ""); err != nil {
			writeWorkspaceJSON(w, http.StatusOK, map[string]any{"success": false, "message": "Connection failed: " + err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Connected to bucket '" + bucket + "'", "buckets": secret.Buckets})
		return
	}
	buckets, err := client.ListBuckets(r.Context())
	if err != nil {
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"success": false, "message": "Connection failed: " + err.Error()})
		return
	}
	total := len(buckets)
	if total > 20 {
		buckets = buckets[:20]
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"success": true, "message": fmt.Sprintf("Connected. Found %d accessible bucket(s).", total), "buckets": buckets,
	})
}

func (s *Server) handleCloudBuckets(w http.ResponseWriter, r *http.Request, client cloudstore.Client) {
	if r.Method != http.MethodGet {
		writeCloudError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	buckets, err := client.ListBuckets(r.Context())
	if err != nil {
		writeCloudRemoteError(w, err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, buckets)
}

func (s *Server) handleCloudObjects(w http.ResponseWriter, r *http.Request, client cloudstore.Client) {
	if r.Method != http.MethodGet {
		writeCloudError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	bucket := r.URL.Query().Get("bucket")
	if bucket == "" {
		writeCloudError(w, http.StatusBadRequest, "bucket query param is required")
		return
	}
	limit, err := cloudQueryLimit(r, "max_keys", cloudstore.DefaultObjectLimit, cloudstore.MaxPageSize)
	if err != nil {
		writeCloudError(w, http.StatusBadRequest, err.Error())
		return
	}
	page, err := client.ListPage(r.Context(), bucket, r.URL.Query().Get("prefix"), "", limit, "")
	if err != nil {
		writeCloudRemoteError(w, err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, page.Objects)
}

func (s *Server) handleCloudFolder(w http.ResponseWriter, r *http.Request, client cloudstore.Client) {
	if r.Method != http.MethodGet {
		writeCloudError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	bucket := r.URL.Query().Get("bucket")
	if bucket == "" {
		writeCloudError(w, http.StatusBadRequest, "bucket query param is required")
		return
	}
	limit, err := cloudQueryLimit(r, "page_limit", cloudstore.DefaultFolderLimit, cloudstore.DefaultFolderLimit)
	if err != nil {
		writeCloudError(w, http.StatusBadRequest, err.Error())
		return
	}
	prefix, token := r.URL.Query().Get("prefix"), ""
	page := cloudstore.Page{Objects: []cloudstore.Object{}, Prefixes: []string{}}
	seenTokens := map[string]bool{}
	for len(page.Objects)+len(page.Prefixes) < limit {
		remaining := limit - len(page.Objects) - len(page.Prefixes)
		current, err := client.ListPage(r.Context(), bucket, prefix, "/", min(remaining, cloudstore.MaxPageSize), token)
		if err != nil {
			writeCloudRemoteError(w, err)
			return
		}
		for _, folder := range current.Prefixes {
			if len(page.Objects)+len(page.Prefixes) >= limit {
				break
			}
			page.Prefixes = append(page.Prefixes, folder)
		}
		for _, object := range current.Objects {
			if len(page.Objects)+len(page.Prefixes) >= limit {
				break
			}
			if object.Key != prefix {
				page.Objects = append(page.Objects, object)
			}
		}
		if current.NextToken == "" || seenTokens[current.NextToken] {
			break
		}
		seenTokens[current.NextToken] = true
		token = current.NextToken
	}
	sort.Strings(page.Prefixes)
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"folders": page.Prefixes, "files": page.Objects})
}

func (s *Server) handleCloudDownload(w http.ResponseWriter, r *http.Request, client cloudstore.Client) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeCloudError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	bucket, key := r.URL.Query().Get("bucket"), r.URL.Query().Get("key")
	if bucket == "" || key == "" {
		writeCloudError(w, http.StatusBadRequest, "bucket and key are required")
		return
	}
	if r.Method == http.MethodHead {
		info, err := client.HeadObject(r.Context(), bucket, key)
		if err != nil {
			writeCloudRemoteError(w, err)
			return
		}
		writeCloudDownloadHeaders(w, r, key, info)
		return
	}
	reader, info, err := client.OpenObject(r.Context(), bucket, key)
	if err != nil {
		writeCloudRemoteError(w, err)
		return
	}
	defer reader.Close()
	writeCloudDownloadHeaders(w, r, key, info)
	if _, err := io.Copy(w, reader); err != nil {
		return
	}
}

func (s *Server) handleCloudImport(w http.ResponseWriter, r *http.Request, userID string, client cloudstore.Client) {
	if r.Method != http.MethodPost {
		writeCloudError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.workspaceStore == nil {
		writeCloudError(w, http.StatusServiceUnavailable, "workspace runtime is not configured")
		return
	}
	var input cloudImportInput
	if err := decodeAttachmentJSON(r, &input); err != nil {
		writeCloudError(w, http.StatusBadRequest, err.Error())
		return
	}
	if input.Bucket == "" || input.Key == "" || input.ProjectID == "" {
		writeCloudError(w, http.StatusBadRequest, "bucket, key, and project_id are required")
		return
	}
	owned, err := s.workspaceStore.ProjectOwnedBy(input.ProjectID, userID)
	if err != nil {
		writeCloudStoreError(w, err)
		return
	}
	if !owned {
		writeCloudError(w, http.StatusNotFound, "project not found")
		return
	}
	head, err := client.HeadObject(r.Context(), input.Bucket, input.Key)
	if err != nil {
		writeCloudRemoteError(w, err)
		return
	}
	if head.Size > compute.RemoteImportMaxBytes {
		writeCloudImportTooLarge(w)
		return
	}
	reader, info, err := client.OpenObject(r.Context(), input.Bucket, input.Key)
	if err != nil {
		writeCloudRemoteError(w, err)
		return
	}
	defer reader.Close()
	if info.Size > compute.RemoteImportMaxBytes {
		writeCloudImportTooLarge(w)
		return
	}
	filename := safeArchiveFilename(path.Base(input.Key), "object")
	contentType := strings.TrimSpace(info.ContentType)
	if contentType == "" {
		contentType = mime.TypeByExtension(path.Ext(filename))
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	artifactID := uuid.NewString()
	if key := strings.TrimSpace(firstNonEmpty(r.Header.Get("Idempotency-Key"), r.Header.Get("X-Synon-Idempotency-Key"))); key != "" {
		artifactID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("synon-cloud-import-v1\x00"+userID+"\x00"+input.ProjectID+"\x00"+key)).String()
	}
	artifact, version, err := s.workspaceStore.SaveArtifactVersionFromReaderRealtime(workspaceMutationContext(r), workspace.SaveArtifactVersionReaderInput{
		ArtifactID: artifactID, ProjectID: input.ProjectID, Name: filename,
		Kind: contentType, Content: reader, CreatedBy: "cloud-import", MaxBytes: compute.RemoteImportMaxBytes,
	}, userID)
	if err != nil {
		var tooLarge *workspace.ArtifactContentTooLargeError
		if errors.As(err, &tooLarge) {
			writeCloudImportTooLarge(w)
			return
		}
		writeCloudStoreError(w, err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"id": artifact.ID, "version_id": version.ID, "version_number": version.VersionNumber,
		"project_id": artifact.ProjectID, "filename": artifact.Name, "content_type": artifact.Kind,
		"size_bytes": version.SizeBytes, "created_at": version.CreatedAt, "is_user_upload": true,
	})
}

func writeCloudImportTooLarge(w http.ResponseWriter) {
	writeWorkspaceJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
		"detail":     fmt.Sprintf("cloud object exceeds the %d byte import limit", compute.RemoteImportMaxBytes),
		"remoteKind": "too_large",
	})
}

func (s *Server) handleCloudExport(w http.ResponseWriter, r *http.Request, userID string, client cloudstore.Client) {
	if r.Method != http.MethodPost {
		writeCloudError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.workspaceStore == nil {
		writeCloudError(w, http.StatusServiceUnavailable, "workspace runtime is not configured")
		return
	}
	var input cloudExportInput
	if err := decodeAttachmentJSON(r, &input); err != nil {
		writeCloudError(w, http.StatusBadRequest, err.Error())
		return
	}
	if input.ArtifactID == "" || input.Bucket == "" || input.Key == "" {
		writeCloudError(w, http.StatusBadRequest, "artifact_id, bucket, and key are required")
		return
	}
	artifact, version, reader, found, err := s.workspaceStore.OpenCurrentArtifactContent(input.ArtifactID)
	if err != nil {
		writeCloudStoreError(w, err)
		return
	}
	if !found {
		writeCloudError(w, http.StatusNotFound, "Artifact "+input.ArtifactID+" not found")
		return
	}
	defer reader.Close()
	owned, err := s.workspaceStore.ProjectOwnedBy(artifact.ProjectID, userID)
	if err != nil {
		writeCloudStoreError(w, err)
		return
	}
	if !owned {
		writeCloudError(w, http.StatusNotFound, "Artifact "+input.ArtifactID+" not found")
		return
	}
	uploaded, err := client.PutObject(r.Context(), input.Bucket, input.Key, reader, version.SizeBytes, artifact.Kind)
	if err != nil {
		writeCloudRemoteError(w, err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"success": true, "message": fmt.Sprintf("Uploaded %d bytes", uploaded), "key": input.Key,
	})
}

func (s *Server) cloudRequestContext(w http.ResponseWriter, r *http.Request) (*secretstore.Store, string, bool) {
	store, ok := s.secretStoreForRequest(w)
	if !ok {
		return nil, "", false
	}
	userID := secretUserID(r)
	if s.cloudFactory == nil {
		writeCloudError(w, http.StatusServiceUnavailable, "cloud storage runtime is not configured")
		return nil, "", false
	}
	return store, userID, true
}

func (s *Server) cloudClient(secret secretstore.Secret) (cloudstore.Client, error) {
	values := map[string]string{}
	if strings.TrimSpace(secret.Value) != "" {
		if err := json.Unmarshal([]byte(secret.Value), &values); err != nil {
			return nil, errors.New("cloud credential value must be a JSON object")
		}
	}
	for key, value := range secret.Credentials {
		values[key] = value
	}
	if secret.Provider == "azure" && secret.Region != "" && values["storage_account"] == "" {
		values["storage_account"] = secret.Region
	}
	return s.cloudFactory.New(secret.Provider, cloudstore.Credentials{Values: values, Region: secret.Region})
}

func isCloudProvider(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "aws", "gcp", "azure":
		return true
	default:
		return false
	}
}

func projectCloudCredential(secret secretstore.Secret) cloudCredentialProjection {
	provider := strings.ToLower(strings.TrimSpace(secret.Provider))
	if provider == "aws" {
		provider = "s3"
	} else if provider == "gcp" {
		provider = "gcs"
	}
	var defaultBucket any
	if len(secret.Buckets) > 0 && strings.TrimSpace(secret.Buckets[0]) != "" {
		defaultBucket = strings.TrimSpace(secret.Buckets[0])
	}
	credentialType := strings.TrimSpace(secret.CredentialType)
	if credentialType == "" {
		credentialType = "access_key"
	}
	return cloudCredentialProjection{
		ID: secret.ID, Provider: provider, Name: secret.Name, CredentialType: credentialType,
		Connected: secret.Value != "" || len(secret.Credentials) > 0, DefaultBucket: defaultBucket,
		Region: secret.Region, CreatedAt: secret.CreatedAt, UpdatedAt: secret.UpdatedAt,
	}
}

func cloudQueryLimit(r *http.Request, key string, fallback, maximum int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > maximum {
		return 0, fmt.Errorf("%s must be between 1 and %d", key, maximum)
	}
	return value, nil
}

func writeCloudDownloadHeaders(w http.ResponseWriter, r *http.Request, key string, info cloudstore.ObjectInfo) {
	filename := safeArchiveFilename(path.Base(key), "object")
	contentType := "application/octet-stream"
	disposition := "attachment"
	if r.URL.Query().Get("disposition") == "inline" {
		disposition = "inline"
		if strings.TrimSpace(info.ContentType) != "" {
			contentType = info.ContentType
		} else if detected := mime.TypeByExtension(path.Ext(filename)); detected != "" {
			contentType = detected
		}
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": filename}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if info.Size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	}
}

func writeCloudRemoteError(w http.ResponseWriter, err error) {
	var remote *cloudstore.HTTPError
	if errors.As(err, &remote) && remote.StatusCode == http.StatusNotFound {
		writeCloudError(w, http.StatusNotFound, err.Error())
		return
	}
	writeCloudError(w, http.StatusBadGateway, err.Error())
}

func writeCloudStoreError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	message := err.Error()
	if errors.Is(err, workspace.ErrInvalidMutationIdempotencyKey) {
		status = http.StatusBadRequest
	} else if errors.Is(err, workspace.ErrMutationIdempotencyConflict) {
		status = http.StatusConflict
	} else if strings.Contains(message, "not found") || strings.Contains(message, "does not exist") {
		status = http.StatusNotFound
	} else if strings.Contains(message, "required") || strings.Contains(message, "invalid") {
		status = http.StatusBadRequest
	}
	writeCloudError(w, status, message)
}

func writeCloudError(w http.ResponseWriter, status int, message string) {
	writeWorkspaceJSON(w, status, map[string]any{"detail": message})
}
