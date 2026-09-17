package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	workspace "synon-go/internal/persistence/workspace"
)

const maxSynonBiomedSeedManifestBytes = 4 << 20

type synonBiomedCountedNames struct {
	Count int      `json:"count"`
	Names []string `json:"names"`
}

type synonBiomedSeedProject struct {
	Slug            string `json:"slug"`
	Name            string `json:"name"`
	ManifestPath    string `json:"manifestPath"`
	RootFrameID     string `json:"rootFrameId"`
	ArtifactCount   int    `json:"artifactCount"`
	ChildFrameCount int    `json:"childFrameCount"`
	FolderCount     int    `json:"folderCount"`
}

type synonBiomedSeedProjectCatalog struct {
	Count    int                      `json:"count"`
	Projects []synonBiomedSeedProject `json:"projects"`
}

type synonBiomedBackendProject struct {
	ProjectID         string  `json:"projectId"`
	Name              string  `json:"name"`
	Description       *string `json:"description"`
	ConversationCount int     `json:"conversationCount"`
	ArtifactCount     int     `json:"artifactCount"`
	CreatedAt         *string `json:"createdAt"`
	UpdatedAt         *string `json:"updatedAt"`
	LastActiveAt      *string `json:"lastActiveAt"`
}

type synonBiomedBackendProjectCatalog struct {
	Count    int                         `json:"count"`
	Projects []synonBiomedBackendProject `json:"projects"`
}

func (s *Server) handleSynonBiomedCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	userID := strings.TrimSpace(r.Header.Get("X-Synon-User-Id"))
	if userID == "" {
		writeWorkspaceJSON(w, http.StatusUnauthorized, map[string]any{"message": "authentication required"})
		return
	}
	projects, err := s.synonBiomedProjectCatalog(userID)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"message": "unable to load project catalog"})
		return
	}
	mcpNames, err := s.synonBiomedMCPNames(r, userID)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"message": "unable to load MCP catalog"})
		return
	}
	seedProjects, err := collectSynonBiomedSeedProjects(s.runtimeAssetsDir)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"message": "unable to load seed project catalog"})
		return
	}
	thirdPartyAssets, err := collectSynonBiomedThirdPartyAssets(s.runtimeAssetsDir)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"message": "unable to load runtime asset catalog"})
		return
	}

	agentNames := make([]string, 0)
	if s.agentCatalog != nil {
		for _, agent := range s.agentCatalog.Agents() {
			agentNames = append(agentNames, agent.Name)
		}
	}
	skillNames := make([]string, 0)
	if s.skillCatalog != nil {
		for _, skill := range s.skillCatalog.Skills() {
			skillNames = append(skillNames, skill.Name)
		}
	}
	agentNames = uniqueSortedNames(agentNames)
	skillNames = uniqueSortedNames(skillNames)
	status := "healthy"
	if s.agentCatalog == nil || !s.agentCatalog.Ready() || s.agentCatalogError != nil {
		status = "degraded"
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"product": "Synon Biomed",
		"runtime": map[string]any{
			"runtimeAssetsDir": s.runtimeAssetsDir,
			"agents":           countedNames(agentNames),
			"skills":           countedNames(skillNames),
			"mcpServers":       countedNames(mcpNames),
			"thirdPartyAssets": countedNames(thirdPartyAssets),
			"seedProjects":     seedProjects,
		},
		"backend": map[string]any{
			"baseUrl": requestOrigin(r),
			"health": map[string]any{
				"status": status, "service": "gateway", "agentsRegistered": len(agentNames),
			},
			"agents":   countedNames(agentNames),
			"projects": projects,
		},
	})
}

func (s *Server) handleSynonBiomedProjects(w http.ResponseWriter, r *http.Request) {
	userID := strings.TrimSpace(r.Header.Get("X-Synon-User-Id"))
	if userID == "" {
		writeWorkspaceJSON(w, http.StatusUnauthorized, map[string]any{"message": "authentication required"})
		return
	}
	if s.workspaceStore == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "workspace storage is not configured"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		projects, err := s.synonBiomedProjectCatalog(userID)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"message": "unable to list projects"})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, projects)
	case http.MethodPost:
		var input struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Context     string `json:"context"`
		}
		if err := decodeAgentCompatJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "invalid project request"})
			return
		}
		input.Name = strings.TrimSpace(input.Name)
		input.Description = strings.TrimSpace(input.Description)
		input.Context = strings.TrimSpace(input.Context)
		if input.Name == "" || len(input.Name) > 256 || !utf8.ValidString(input.Name) {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "project name is required and must be at most 256 bytes"})
			return
		}
		if len(input.Description) > 16<<10 || len(input.Context) > 64<<10 ||
			!utf8.ValidString(input.Description) || !utf8.ValidString(input.Context) {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "project metadata is invalid or too large"})
			return
		}
		projectID, err := newCompatibilityProjectID()
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"message": "unable to create project"})
			return
		}
		project, created, err := s.workspaceStore.CreateCompatibilityProject(workspace.CreateCompatibilityProjectInput{
			ID: projectID, UserID: userID, Name: input.Name, Description: input.Description,
			ContextData: input.Context, FindOrCreate: true,
		})
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"message": "unable to create project"})
			return
		}
		if created {
			if _, err := s.publishProjectEvent(project.ID, "frame_update", map[string]any{"action": "project_created"}); err != nil {
				writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"message": "project created but realtime publication failed"})
				return
			}
		}
		lastActive, err := s.workspaceStore.CompatibilityProjectLastActiveAt(userID, project.ID)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"message": "unable to read created project"})
			return
		}
		writeWorkspaceJSON(w, http.StatusCreated, map[string]any{
			"project": projectToSynonBiomed(project, lastActive),
		})
	default:
		w.Header().Set("Allow", "GET, POST")
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
	}
}

func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"cache_dir": systemDirectory("SYNON_AI_CACHE_DIR", filepath.Join(s.fileRoot, "cache")),
		"work_dir":  systemDirectory("SYNON_AI_WORK_DIR", filepath.Join(s.fileRoot, "work")),
		"log_dir":   systemDirectory("SYNON_AI_LOG_DIR", filepath.Join(s.fileRoot, "logs")),
		"platform":  runtime.GOOS,
		"arch":      runtime.GOARCH,
	})
}

func (s *Server) synonBiomedProjectCatalog(userID string) (synonBiomedBackendProjectCatalog, error) {
	if s.workspaceStore == nil {
		return synonBiomedBackendProjectCatalog{}, errors.New("workspace storage is not configured")
	}
	projects, err := s.workspaceStore.ListCompatibilityProjects(userID, 1000, 0)
	if err != nil {
		return synonBiomedBackendProjectCatalog{}, err
	}
	total, err := s.workspaceStore.CountCompatibilityProjects(userID)
	if err != nil {
		return synonBiomedBackendProjectCatalog{}, err
	}
	items := make([]synonBiomedBackendProject, 0, len(projects))
	for _, project := range projects {
		lastActive, err := s.workspaceStore.CompatibilityProjectLastActiveAt(userID, project.ID)
		if err != nil {
			return synonBiomedBackendProjectCatalog{}, err
		}
		items = append(items, projectToSynonBiomed(project, lastActive))
	}
	return synonBiomedBackendProjectCatalog{Count: total, Projects: items}, nil
}

func (s *Server) synonBiomedMCPNames(r *http.Request, userID string) ([]string, error) {
	if s.mcpDirectory == nil {
		return []string{}, nil
	}
	connectors, err := s.mcpDirectory.ListUnifiedConnectors(r.Context(), userID)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(connectors))
	for _, connector := range connectors {
		names = append(names, connector.Name)
	}
	return uniqueSortedNames(names), nil
}

func projectToSynonBiomed(project workspace.CompatibilityProject, lastActive *time.Time) synonBiomedBackendProject {
	return synonBiomedBackendProject{
		ProjectID: project.ID, Name: project.Name, Description: optionalString(project.Description),
		ConversationCount: project.ConversationCount, ArtifactCount: project.ArtifactCount,
		CreatedAt: optionalTime(project.CreatedAt), UpdatedAt: optionalTime(project.UpdatedAt),
		LastActiveAt: optionalTimePointer(lastActive),
	}
}

func collectSynonBiomedSeedProjects(root string) (synonBiomedSeedProjectCatalog, error) {
	result := synonBiomedSeedProjectCatalog{Projects: []synonBiomedSeedProject{}}
	if root == "" {
		return result, nil
	}
	seedDir := filepath.Join(root, "seed")
	entries, err := os.ReadDir(seedDir)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "manifest_") || !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(seedDir, name)
		document, err := readBoundedJSONMap(path, maxSynonBiomedSeedManifestBytes)
		if err != nil {
			return result, fmt.Errorf("read seed manifest %s: %w", name, err)
		}
		slug := strings.TrimSuffix(strings.TrimPrefix(name, "manifest_"), ".json")
		rootFrameID := ""
		if frame, ok := document["root_frame"].(map[string]any); ok {
			rootFrameID, _ = frame["id"].(string)
		}
		result.Projects = append(result.Projects, synonBiomedSeedProject{
			Slug: slug, Name: seedProjectDisplayName(slug), ManifestPath: path, RootFrameID: rootFrameID,
			ArtifactCount:   collectionSize(document["artifacts"]),
			ChildFrameCount: collectionSize(document["child_frames"]),
			FolderCount:     collectionSize(document["folders"]),
		})
	}
	sort.Slice(result.Projects, func(i, j int) bool { return result.Projects[i].Slug < result.Projects[j].Slug })
	result.Count = len(result.Projects)
	return result, nil
}

func collectSynonBiomedThirdPartyAssets(root string) ([]string, error) {
	if root == "" {
		return []string{}, nil
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		switch entry.Name() {
		case "agents", "skills", "seed", "mcp-servers":
			continue
		case "vendor", "sharp-runtime":
			children, childErr := os.ReadDir(filepath.Join(root, entry.Name()))
			if childErr != nil {
				return nil, childErr
			}
			for _, child := range children {
				if child.IsDir() {
					names = append(names, child.Name())
				}
			}
		default:
			names = append(names, entry.Name())
		}
	}
	return uniqueSortedNames(names), nil
}

func readBoundedJSONMap(path string, limit int64) (map[string]any, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("manifest exceeds size limit")
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	return document, nil
}

func cleanExistingDirectory(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return ""
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(absolute)
}

func requestOrigin(r *http.Request) string {
	if r == nil || strings.TrimSpace(r.Host) == "" {
		return ""
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + strings.TrimSpace(r.Host)
}

func countedNames(names []string) synonBiomedCountedNames {
	if names == nil {
		names = []string{}
	}
	return synonBiomedCountedNames{Count: len(names), Names: names}
}

func uniqueSortedNames(values []string) []string {
	seen := map[string]string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; !exists {
			seen[key] = value
		}
	}
	result := make([]string, 0, len(seen))
	for _, value := range seen {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i]) < strings.ToLower(result[j]) })
	return result
}

func optionalString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func optionalTime(value time.Time) *string {
	if value.IsZero() {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339Nano)
	return &formatted
}

func optionalTimePointer(value *time.Time) *string {
	if value == nil {
		return nil
	}
	return optionalTime(*value)
}

func collectionSize(value any) int {
	switch typed := value.(type) {
	case []any:
		return len(typed)
	case map[string]any:
		return len(typed)
	default:
		return 0
	}
}

func seedProjectDisplayName(slug string) string {
	parts := strings.FieldsFunc(slug, func(value rune) bool { return value == '_' || value == '-' })
	for index, part := range parts {
		if strings.EqualFold(part, "crispr") {
			parts[index] = "CRISPR"
			continue
		}
		if part != "" {
			parts[index] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, " ")
}

func systemDirectory(environmentKey, fallback string) string {
	value := strings.TrimSpace(os.Getenv(environmentKey))
	if value == "" {
		value = fallback
	}
	if value == "" {
		return ""
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return filepath.Clean(value)
	}
	return filepath.Clean(absolute)
}
