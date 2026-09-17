package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"synon-go/internal/memorypolicy"
	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) handleMemoryCompatibility(w http.ResponseWriter, r *http.Request) {
	store, userID, ok := s.memoryRequestContext(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		projectID := strings.TrimSpace(r.URL.Query().Get("subject_project_id"))
		if projectID != "" && !workspaceProjectOwned(w, store, projectID, userID) {
			return
		}
		includeSuperseded, err := parseOptionalBoolean(r.URL.Query().Get("include_superseded"), false)
		if err != nil {
			writeV11Detail(w, http.StatusBadRequest, err.Error())
			return
		}
		memories, err := store.ListMemoriesForUser(r.Context(), userID, r.URL.Query().Get("origin"), projectID, includeSuperseded)
		if err != nil {
			writeMemoryCompatibilityError(w, err)
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, memories)
	case http.MethodPost:
		input, err := decodeMemoryCompatibilityCreateInput(r)
		if err != nil {
			writeV11Detail(w, http.StatusBadRequest, err.Error())
			return
		}
		body, err := workspace.PrepareUserMemoryBody(input.Text)
		if err != nil {
			writeMemoryCompatibilityError(w, err)
			return
		}
		projectID, artifactID, err := store.ResolveMemoryEntity(r.Context(), userID, input.Entity)
		if err != nil {
			writeMemoryCompatibilityError(w, err)
			return
		}
		categoryID := ""
		if strings.TrimSpace(input.Category) != "" {
			categoryID, err = store.MemoryCategoryIDByName(r.Context(), userID, input.Category)
			if err != nil {
				writeMemoryCompatibilityError(w, err)
				return
			}
		}
		memory, err := store.CreateMemoryOwned(workspaceMutationContext(r), workspace.CreateMemoryInput{
			ID: "mem_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16], UserID: userID,
			Body: body, Origin: "user", Evidence: input.Evidence,
			SubjectProjectID: projectID, SubjectArtifactID: artifactID, CategoryID: categoryID,
		}, userID)
		if err != nil {
			writeMemoryCompatibilityError(w, err)
			return
		}
		writeWorkspaceJSON(w, http.StatusCreated, memory)
	case http.MethodDelete:
		deleted, err := store.DeleteAllMemoriesForUser(r.Context(), userID)
		if err != nil {
			writeMemoryCompatibilityError(w, err)
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
	default:
		writeV11Detail(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleMemoryRecordCompatibility(w http.ResponseWriter, r *http.Request) {
	store, userID, ok := s.memoryRequestContext(w, r)
	if !ok {
		return
	}
	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/memories/"))
	if len(segments) != 1 {
		writeV11Detail(w, http.StatusNotFound, "memory endpoint not found")
		return
	}
	memoryID, err := url.PathUnescape(segments[0])
	if err != nil || strings.TrimSpace(memoryID) == "" {
		writeV11Detail(w, http.StatusBadRequest, "invalid memory id")
		return
	}
	switch r.Method {
	case http.MethodPut:
		input, err := decodeMemoryCompatibilityUpdateInput(r)
		if err != nil {
			writeV11Detail(w, http.StatusBadRequest, err.Error())
			return
		}
		patch := workspace.UpdateMemoryInput{}
		if input.Text != nil {
			text, err := workspace.PrepareUserMemoryBody(*input.Text)
			if err != nil {
				writeMemoryCompatibilityError(w, err)
				return
			}
			patch.Body = &text
		}
		if input.Evidence != nil {
			patch.Evidence = input.Evidence
		}
		if input.Entity != nil {
			projectID, artifactID, err := store.ResolveMemoryEntity(r.Context(), userID, *input.Entity)
			if err != nil {
				writeMemoryCompatibilityError(w, err)
				return
			}
			patch.SubjectProjectID, patch.SubjectArtifactID = &projectID, &artifactID
		}
		if input.ClearCategory {
			patch.ClearCategory = true
		} else if input.Category != nil {
			categoryID, err := store.MemoryCategoryIDByName(r.Context(), userID, *input.Category)
			if err != nil {
				writeMemoryCompatibilityError(w, err)
				return
			}
			patch.CategoryID = &categoryID
		}
		memory, err := store.UpdateMemoryOwned(r.Context(), memoryID, userID, patch)
		if err != nil {
			writeMemoryCompatibilityError(w, err)
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, memory)
	case http.MethodDelete:
		deleted, err := store.DeleteMemoryOwned(r.Context(), memoryID, userID)
		if err != nil {
			writeMemoryCompatibilityError(w, err)
			return
		}
		if !deleted {
			writeV11Detail(w, http.StatusNotFound, fmt.Sprintf("Memory %s not found", memoryID))
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"deleted": true})
	default:
		writeV11Detail(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

type memoryCompatibilityCreateInput struct {
	Text     string
	Entity   string
	Evidence string
	Category string
}

func decodeMemoryCompatibilityCreateInput(r *http.Request) (memoryCompatibilityCreateInput, error) {
	raw, err := decodeMemoryCompatibilityObject(r)
	if err != nil {
		return memoryCompatibilityCreateInput{}, err
	}
	text, _, _, err := memoryCompatibilitySchemaStringField(raw, "text", memoryCompatibilityStringSchema{
		Required: true, MinUTF16: 1, MaxUTF16: memorypolicy.TextMaxUTF16Units,
	})
	if err != nil {
		return memoryCompatibilityCreateInput{}, err
	}
	entity, _, _, err := memoryCompatibilitySchemaStringField(raw, "entity", memoryCompatibilityStringSchema{})
	if err != nil {
		return memoryCompatibilityCreateInput{}, err
	}
	evidence, evidenceSet, err := memoryCompatibilityEvidenceField(raw)
	if err != nil {
		return memoryCompatibilityCreateInput{}, err
	}
	if !evidenceSet {
		evidence = "stated"
	}
	category, _, _, err := memoryCompatibilitySchemaStringField(raw, "category", memoryCompatibilityStringSchema{
		Nullable: true, MinUTF16: 1,
	})
	if err != nil {
		return memoryCompatibilityCreateInput{}, err
	}
	return memoryCompatibilityCreateInput{
		Text: text, Entity: entity, Evidence: evidence, Category: category,
	}, nil
}

type memoryCompatibilityUpdateInput struct {
	Text          *string
	Evidence      *string
	Entity        *string
	Category      *string
	ClearCategory bool
}

func decodeMemoryCompatibilityUpdateInput(r *http.Request) (memoryCompatibilityUpdateInput, error) {
	raw, err := decodeMemoryCompatibilityObject(r)
	if err != nil {
		return memoryCompatibilityUpdateInput{}, err
	}
	result := memoryCompatibilityUpdateInput{}
	if _, exists := raw["text"]; exists {
		text, _, _, err := memoryCompatibilitySchemaStringField(raw, "text", memoryCompatibilityStringSchema{
			MinUTF16: 1, MaxUTF16: memorypolicy.TextMaxUTF16Units,
		})
		if err != nil {
			return memoryCompatibilityUpdateInput{}, err
		}
		result.Text = &text
	}
	if _, exists := raw["evidence"]; exists {
		evidence, _, err := memoryCompatibilityEvidenceField(raw)
		if err != nil {
			return memoryCompatibilityUpdateInput{}, err
		}
		result.Evidence = &evidence
	}
	if _, exists := raw["entity"]; exists {
		entity, _, _, err := memoryCompatibilitySchemaStringField(raw, "entity", memoryCompatibilityStringSchema{})
		if err != nil {
			return memoryCompatibilityUpdateInput{}, err
		}
		result.Entity = &entity
	}
	if _, exists := raw["category"]; exists {
		category, _, isNull, err := memoryCompatibilitySchemaStringField(raw, "category", memoryCompatibilityStringSchema{
			Nullable: true, MinUTF16: 1,
		})
		if err != nil {
			return memoryCompatibilityUpdateInput{}, err
		}
		if isNull {
			result.ClearCategory = true
		} else {
			result.Category = &category
		}
	}
	return result, nil
}

type memoryCompatibilityStringSchema struct {
	Required bool
	Nullable bool
	MinUTF16 int
	MaxUTF16 int
}

func memoryCompatibilitySchemaStringField(raw map[string]json.RawMessage, name string, schema memoryCompatibilityStringSchema) (string, bool, bool, error) {
	value, exists := raw[name]
	if !exists {
		if schema.Required {
			return "", false, false, fmt.Errorf("Invalid arguments: %s: Required", name)
		}
		return "", false, false, nil
	}
	if memoryCompatibilityJSONNull(value) {
		if schema.Nullable {
			return "", true, true, nil
		}
		return "", true, true, fmt.Errorf("Invalid arguments: %s: Expected string, received null", name)
	}
	var decoded string
	if err := json.Unmarshal(value, &decoded); err != nil {
		return "", true, false, fmt.Errorf(
			"Invalid arguments: %s: Expected string, received %s",
			name,
			memoryCompatibilityJSONType(value),
		)
	}
	length := memorypolicy.UTF16Length(decoded)
	if schema.MinUTF16 > 0 && length < schema.MinUTF16 {
		return "", true, false, fmt.Errorf("Invalid arguments: %s: String must contain at least %d character(s)", name, schema.MinUTF16)
	}
	if schema.MaxUTF16 > 0 && length > schema.MaxUTF16 {
		return "", true, false, fmt.Errorf("Invalid arguments: %s: String must contain at most %d character(s)", name, schema.MaxUTF16)
	}
	return decoded, true, false, nil
}

func memoryCompatibilityEvidenceField(raw map[string]json.RawMessage) (string, bool, error) {
	value, exists := raw["evidence"]
	if !exists {
		return "", false, nil
	}
	expected := "'stated' | 'observed' | 'inferred'"
	if memoryCompatibilityJSONNull(value) {
		return "", true, fmt.Errorf("Invalid arguments: evidence: Expected %s, received null", expected)
	}
	var decoded string
	if err := json.Unmarshal(value, &decoded); err != nil {
		return "", true, fmt.Errorf(
			"Invalid arguments: evidence: Expected %s, received %s",
			expected,
			memoryCompatibilityJSONType(value),
		)
	}
	if decoded != "stated" && decoded != "observed" && decoded != "inferred" {
		return "", true, fmt.Errorf("Invalid arguments: evidence: Invalid enum value. Expected %s, received '%s'", expected, decoded)
	}
	return decoded, true, nil
}

func decodeMemoryCompatibilityObject(r *http.Request) (map[string]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := decodeWorkspaceJSON(r, &raw); err != nil {
		if errors.Is(err, io.EOF) {
			return map[string]json.RawMessage{}, nil
		}
		return nil, err
	}
	if raw == nil {
		return nil, errors.New("Invalid arguments: Expected object, received null")
	}
	return raw, nil
}

func memoryCompatibilityJSONType(value json.RawMessage) string {
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return "unknown"
	}
	switch decoded.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}

func memoryCompatibilityJSONNull(value json.RawMessage) bool {
	return strings.TrimSpace(string(value)) == "null"
}

func (s *Server) handleMemoryCategoriesCompatibility(w http.ResponseWriter, r *http.Request) {
	store, userID, ok := s.memoryRequestContext(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		categories, err := store.ListMemoryCategories(r.Context(), userID)
		if err != nil {
			writeMemoryCompatibilityError(w, err)
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, categories)
	case http.MethodPost:
		var input struct {
			Name       string `json:"name"`
			Guidance   string `json:"guidance"`
			AutoRecall *bool  `json:"auto_recall"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeV11Detail(w, http.StatusBadRequest, err.Error())
			return
		}
		autoRecall := true
		if input.AutoRecall != nil {
			autoRecall = *input.AutoRecall
		}
		category, err := store.CreateMemoryCategory(r.Context(), userID, input.Name, input.Guidance, autoRecall)
		if err != nil {
			writeMemoryCompatibilityError(w, err)
			return
		}
		writeWorkspaceJSON(w, http.StatusCreated, category)
	default:
		writeV11Detail(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleMemoryCategoryCompatibility(w http.ResponseWriter, r *http.Request) {
	store, userID, ok := s.memoryRequestContext(w, r)
	if !ok {
		return
	}
	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/memory/categories/"))
	if len(segments) != 1 {
		writeV11Detail(w, http.StatusNotFound, "memory category endpoint not found")
		return
	}
	categoryID, err := url.PathUnescape(segments[0])
	if err != nil || strings.TrimSpace(categoryID) == "" {
		writeV11Detail(w, http.StatusBadRequest, "invalid category id")
		return
	}
	switch r.Method {
	case http.MethodPut:
		var input struct {
			Name       *string `json:"name"`
			Guidance   *string `json:"guidance"`
			AutoRecall *bool   `json:"auto_recall"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeV11Detail(w, http.StatusBadRequest, err.Error())
			return
		}
		category, err := store.UpdateMemoryCategory(r.Context(), userID, categoryID, workspace.UpdateMemoryCategoryInput{
			Name: input.Name, Guidance: input.Guidance, AutoRecall: input.AutoRecall,
		})
		if err != nil {
			writeMemoryCompatibilityError(w, err)
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, category)
	case http.MethodDelete:
		deleteFacts, err := parseOptionalBoolean(r.URL.Query().Get("delete_facts"), false)
		if err != nil {
			writeV11Detail(w, http.StatusBadRequest, "Unrecognized delete_facts value. Use 'true' or 'false'.")
			return
		}
		result, err := store.DeleteMemoryCategory(r.Context(), userID, categoryID, deleteFacts)
		if err != nil {
			writeMemoryCompatibilityError(w, err)
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, result)
	default:
		writeV11Detail(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleMemoryEnabledCompatibility(w http.ResponseWriter, r *http.Request) {
	store, userID, ok := s.memoryRequestContext(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		enabled, err := store.MemoryEnabledWithDefault(r.Context(), userID, s.memoryConfig.Enabled)
		if err != nil {
			writeMemoryCompatibilityError(w, err)
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"enabled": enabled})
	case http.MethodPut:
		var input struct {
			Enabled *bool `json:"enabled"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil || input.Enabled == nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"error": "body.enabled must be boolean"})
			return
		}
		if err := store.SetMemoryEnabled(r.Context(), userID, *input.Enabled); err != nil {
			writeMemoryCompatibilityError(w, err)
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"enabled": *input.Enabled})
	default:
		writeV11Detail(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleMemoryAutoExtractionEnabledCompatibility(w http.ResponseWriter, r *http.Request) {
	_, userID, ok := s.memoryRequestContext(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		enabled, err := s.memoryAutoExtractionEnabled(userID)
		if err != nil {
			writeMemoryCompatibilityError(w, err)
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"enabled": enabled})
	case http.MethodPut:
		var input struct {
			Enabled *bool `json:"enabled"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil || input.Enabled == nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"error": "body.enabled must be boolean"})
			return
		}
		if err := s.setMemoryAutoExtractionEnabled(userID, *input.Enabled); err != nil {
			writeMemoryCompatibilityError(w, err)
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"enabled": *input.Enabled})
	default:
		writeV11Detail(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleMemoryContextCompatibility(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeV11Detail(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	store, userID, ok := s.memoryRequestContext(w, r)
	if !ok {
		return
	}
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	var projectEnabled *bool
	if projectID != "" {
		var err error
		projectEnabled, err = store.ProjectMemoryEnabledSetting(r.Context(), projectID, userID)
		if err != nil {
			writeMemoryCompatibilityError(w, err)
			return
		}
	}
	rows, err := store.ListMemoryContextRows(r.Context(), userID, projectID)
	if err != nil {
		writeMemoryCompatibilityError(w, err)
		return
	}
	groups, err := store.GroupMemoryEntities(r.Context(), rows)
	if err != nil {
		writeMemoryCompatibilityError(w, err)
		return
	}
	sessions, err := store.ListMemorySessions(r.Context(), userID, projectID)
	if err != nil {
		writeMemoryCompatibilityError(w, err)
		return
	}
	categories, err := store.ListMemoryCategories(r.Context(), userID)
	if err != nil {
		writeMemoryCompatibilityError(w, err)
		return
	}
	allRows, err := store.ListMemoriesForUser(r.Context(), userID, "", "", false)
	if err != nil {
		writeMemoryCompatibilityError(w, err)
		return
	}
	payload := map[string]any{
		"profile_md": renderMemoryProfile(groups), "listing_md": renderMemoryListing(groups),
		"entities": groups, "sessions": sessions, "categories": categories, "total_user_rows": len(allRows),
	}
	if projectEnabled == nil {
		payload["memory_enabled"] = nil
	} else {
		payload["memory_enabled"] = *projectEnabled
	}
	writeWorkspaceJSON(w, http.StatusOK, payload)
}

func (s *Server) handleMemorySessionCompatibility(w http.ResponseWriter, r *http.Request) {
	store, userID, ok := s.memoryRequestContext(w, r)
	if !ok {
		return
	}
	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/memory/sessions/"))
	if len(segments) != 1 {
		writeV11Detail(w, http.StatusNotFound, "memory session endpoint not found")
		return
	}
	frameID, err := url.PathUnescape(segments[0])
	if err != nil || strings.TrimSpace(frameID) == "" {
		writeV11Detail(w, http.StatusBadRequest, "invalid frame id")
		return
	}
	switch r.Method {
	case http.MethodGet:
		rows, err := store.ListFrameMemories(r.Context(), userID, frameID)
		if err != nil {
			writeMemoryCompatibilityError(w, err)
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"rows": rows})
	case http.MethodDelete:
		deleted, err := store.ClearFrameMemories(r.Context(), userID, frameID)
		if err != nil {
			writeMemoryCompatibilityError(w, err)
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
	default:
		writeV11Detail(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleProjectMemoryEnabledCompatibility(w http.ResponseWriter, r *http.Request, projectID string) {
	if r.Method != http.MethodPut {
		writeV11Detail(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	store, userID, ok := s.memoryRequestContext(w, r)
	if !ok {
		return
	}
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeWorkspaceJSON(r, &input); err != nil || input.Enabled == nil {
		writeV11Detail(w, http.StatusBadRequest, "enabled: boolean required")
		return
	}
	if err := store.SetProjectMemoryEnabled(r.Context(), projectID, userID, *input.Enabled); err != nil {
		writeMemoryCompatibilityError(w, err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"project_id": projectID, "memory_enabled": *input.Enabled})
}

func (s *Server) memoryRequestContext(w http.ResponseWriter, r *http.Request) (*workspace.Store, string, bool) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return nil, "", false
	}
	userID, ok := attachmentUserID(w, r)
	return store, userID, ok
}

func writeMemoryCompatibilityError(w http.ResponseWriter, err error) {
	status := workspaceStatus(err)
	if errors.Is(err, sql.ErrNoRows) {
		status = http.StatusNotFound
	}
	writeV11Detail(w, status, err.Error())
}

func parseOptionalBoolean(raw string, fallback bool) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return fallback, nil
	case "true", "1":
		return true, nil
	case "false", "0":
		return false, nil
	default:
		return false, fmt.Errorf("unrecognized boolean value %q", raw)
	}
}

func renderMemoryProfile(groups []workspace.MemoryEntityGroup) string {
	for _, group := range groups {
		if group.EntityKey != "profile" || len(group.Rows) == 0 {
			continue
		}
		lines := make([]string, 0, len(group.Rows)+1)
		lines = append(lines, "### Profile")
		for index, memory := range group.Rows {
			if index == 32 {
				lines = append(lines, fmt.Sprintf("- ...%d more", len(group.Rows)-index))
				break
			}
			lines = append(lines, fmt.Sprintf("- [%s] [%s] %s  [%s]", memoryRelativeAge(memory.UpdatedAt), memory.Evidence, compactMemoryText(memory.Body, 300), memory.ID))
		}
		return strings.Join(lines, "\n")
	}
	return ""
}

func renderMemoryListing(groups []workspace.MemoryEntityGroup) string {
	if len(groups) == 0 {
		return ""
	}
	rows := append([]workspace.MemoryEntityGroup(nil), groups...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].EntityKey < rows[j].EntityKey })
	lines := []string{"<memory_listing>"}
	for index, group := range rows {
		if index == 64 {
			lines = append(lines, fmt.Sprintf("...%d more entities", len(rows)-index))
			break
		}
		newest := ""
		if len(group.Rows) > 0 {
			newest = compactMemoryText(group.Rows[0].Body, 60)
		}
		lines = append(lines, fmt.Sprintf("%s - %s (%d)", group.EntityKey, newest, len(group.Rows)))
	}
	lines = append(lines, "</memory_listing>")
	return strings.Join(lines, "\n")
}

func compactMemoryText(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if len([]rune(value)) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit-1]) + "..."
}

func memoryRelativeAge(updated time.Time) string {
	age := time.Since(updated)
	switch {
	case age < time.Hour:
		return "recently"
	case age < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(age.Hours()))
	case age < 14*24*time.Hour:
		return fmt.Sprintf("%d days ago", int(age.Hours()/24))
	default:
		return fmt.Sprintf("%d weeks ago", int(age.Hours()/(24*7)))
	}
}
