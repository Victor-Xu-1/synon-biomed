package agentruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"synon-go/internal/assets"
)

const defaultAgentMaxToolResultChars = 50000

type CatalogOptions struct {
	Root            string
	ManifestPath    string
	AvailableSkills []string
}

type AgentDefinition struct {
	Name                    string         `json:"name"`
	DisplayName             string         `json:"displayName"`
	Description             string         `json:"description"`
	Enabled                 bool           `json:"enabled"`
	Healthy                 bool           `json:"healthy"`
	Parameters              map[string]any `json:"parameters"`
	SkillsLocked            bool           `json:"skillsLocked"`
	Source                  string         `json:"source"`
	SupportsPlanMode        bool           `json:"supportsPlanMode"`
	Unrestricted            bool           `json:"unrestricted"`
	UserHidden              bool           `json:"userHidden"`
	Greeting                string         `json:"greeting,omitempty"`
	SkillNames              []string       `json:"skillNames,omitempty"`
	ConnectorIDs            []string       `json:"connectorIds,omitempty"`
	ExcludedTools           []string       `json:"excludedTools,omitempty"`
	EnableSubtaskDelegation bool           `json:"enableSubtaskDelegation"`
	EnableWebSearch         bool           `json:"enableWebSearch"`
	EnableWebFetch          bool           `json:"enableWebFetch"`
	EnableThinking          bool           `json:"enableThinking"`
	MaxToolResultChars      int            `json:"maxToolResultChars"`
	SystemPrompt            string         `json:"-"`
	IdentityPrompt          string         `json:"-"`
	WorkingStylePrompt      string         `json:"-"`
}

func (a AgentDefinition) EffectiveSystemPrompt() string {
	base := ""
	if strings.TrimSpace(a.SystemPrompt) != "" {
		base = strings.TrimSpace(a.SystemPrompt)
	} else {
		parts := make([]string, 0, 2)
		if prompt := strings.TrimSpace(a.IdentityPrompt); prompt != "" {
			parts = append(parts, prompt)
		}
		if prompt := strings.TrimSpace(a.WorkingStylePrompt); prompt != "" {
			parts = append(parts, prompt)
		}
		base = strings.Join(parts, "\n\n")
	}
	if agentUsesSynonPlatformPolicy(a.Name) {
		base = strings.TrimSpace(base + "\n\n" + SynonPlatformPolicy())
	}
	if base == "" {
		return ""
	}
	return base + "\n"
}

type AgentCatalog struct {
	agents []AgentDefinition
	byName map[string]AgentDefinition
}

func LoadCatalog(options CatalogOptions) (*AgentCatalog, error) {
	root := strings.TrimSpace(options.Root)
	manifestPath := strings.TrimSpace(options.ManifestPath)
	if root == "" || manifestPath == "" {
		return nil, errors.New("agent catalog root and manifest path are required")
	}
	manifest, err := assets.Load(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("load agent catalog manifest: %w", err)
	}
	if len(manifest.Agents) == 0 {
		return nil, errors.New("agent catalog manifest contains no agents")
	}
	if _, err := assets.Verify(root, manifest); err != nil {
		return nil, fmt.Errorf("verify agent catalog assets: %w", err)
	}
	if err := verifyCatalogFileSet(root, manifest); err != nil {
		return nil, err
	}
	operonSkills, err := loadOperonSkills(root, manifest)
	if err != nil {
		return nil, err
	}
	capabilities, err := loadAgentCapabilities(root, manifest)
	if err != nil {
		return nil, err
	}
	availableSkills := normalizedNameSet(options.AvailableSkills)
	missing := make([]string, 0)
	for _, name := range operonSkills {
		if _, ok := availableSkills[normalizeSkillName(name)]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("agent catalog has unresolved OPERON skills: %s", strings.Join(missing, ", "))
	}

	agents := make([]AgentDefinition, 0, len(manifest.Agents))
	seenNames := map[string]string{}
	for _, directoryName := range manifest.Agents {
		if !validCatalogDirectoryName(directoryName) {
			return nil, fmt.Errorf("invalid agent catalog directory %q", directoryName)
		}
		metadataPath := filepath.Join(root, directoryName, "metadata.yaml")
		metadata, err := loadAgentMetadata(metadataPath)
		if err != nil {
			return nil, err
		}
		wantName := strings.ToUpper(strings.ReplaceAll(directoryName, "-", "_"))
		if metadata.AgentName != wantName {
			return nil, fmt.Errorf("agent metadata %q declares %q, want %q", directoryName, metadata.AgentName, wantName)
		}
		lookupName := normalizeAgentName(metadata.AgentName)
		if previous, duplicate := seenNames[lookupName]; duplicate {
			return nil, fmt.Errorf("duplicate agent name %q in %q and %q", metadata.AgentName, previous, directoryName)
		}
		seenNames[lookupName] = directoryName
		definition := metadata.definition()
		if assigned, ok := capabilities[definition.Name]; ok {
			definition.SkillNames = append([]string(nil), assigned.Skills...)
			definition.ConnectorIDs = append([]string(nil), assigned.Connectors...)
		}
		if definition.Name == "OPERON" {
			definition.SkillNames = append([]string(nil), operonSkills...)
		}
		agents = append(agents, definition)
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })
	byName := make(map[string]AgentDefinition, len(agents))
	for _, agent := range agents {
		byName[normalizeAgentName(agent.Name)] = cloneAgentDefinition(agent)
	}
	return &AgentCatalog{agents: agents, byName: byName}, nil
}

func (c *AgentCatalog) Ready() bool {
	return c != nil && len(c.agents) > 0
}

func (c *AgentCatalog) Agents() []AgentDefinition {
	if c == nil {
		return nil
	}
	out := make([]AgentDefinition, len(c.agents))
	for index, agent := range c.agents {
		out[index] = cloneAgentDefinition(agent)
	}
	return out
}

func (c *AgentCatalog) Agent(name string) (AgentDefinition, bool) {
	if c == nil {
		return AgentDefinition{}, false
	}
	agent, found := c.byName[normalizeAgentName(name)]
	return cloneAgentDefinition(agent), found
}

type agentMetadata struct {
	AgentName               string   `yaml:"agent_name"`
	Description             string   `yaml:"description"`
	DisplayName             string   `yaml:"display_name"`
	EnablePlanMode          *bool    `yaml:"enable_plan_mode"`
	EnableSubtaskDelegation *bool    `yaml:"enable_subtask_delegation"`
	EnableThinking          *bool    `yaml:"enable_thinking"`
	EnableWebFetch          *bool    `yaml:"enable_web_fetch"`
	EnableWebSearch         *bool    `yaml:"enable_web_search"`
	ExcludedTools           []string `yaml:"excluded_tools"`
	Greeting                string   `yaml:"greeting"`
	IdentityPrompt          string   `yaml:"identity_prompt"`
	Internal                bool     `yaml:"internal"`
	UserHidden              bool     `yaml:"user_hidden"`
	MaxToolResultChars      int      `yaml:"max_tool_result_chars"`
	SkillsLocked            bool     `yaml:"skills_locked"`
	SystemPrompt            string   `yaml:"system_prompt"`
	WorkingStylePrompt      string   `yaml:"working_style_prompt"`
}

func loadAgentMetadata(path string) (agentMetadata, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return agentMetadata{}, fmt.Errorf("read agent metadata %q: %w", path, err)
	}
	if !utf8.Valid(raw) {
		return agentMetadata{}, fmt.Errorf("agent metadata %q is not valid UTF-8", path)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var metadata agentMetadata
	if err := decoder.Decode(&metadata); err != nil {
		return agentMetadata{}, fmt.Errorf("decode agent metadata %q: %w", path, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return agentMetadata{}, fmt.Errorf("agent metadata %q must contain one YAML document", path)
		}
		return agentMetadata{}, fmt.Errorf("decode trailing agent metadata %q: %w", path, err)
	}
	metadata.AgentName = strings.TrimSpace(metadata.AgentName)
	metadata.DisplayName = strings.TrimSpace(metadata.DisplayName)
	if metadata.AgentName == "" || metadata.DisplayName == "" || strings.TrimSpace(metadata.Description) == "" {
		return agentMetadata{}, fmt.Errorf("agent metadata %q requires agent_name, display_name, and description", path)
	}
	if strings.TrimSpace(metadata.SystemPrompt) == "" && (strings.TrimSpace(metadata.IdentityPrompt) == "" || strings.TrimSpace(metadata.WorkingStylePrompt) == "") {
		return agentMetadata{}, fmt.Errorf("agent metadata %q requires system_prompt or identity_prompt plus working_style_prompt", path)
	}
	return metadata, nil
}

// LoadAgentDefinitionFile decodes one content-addressed bundled agent
// metadata file. Callers remain responsible for verifying the owning catalog
// manifest before using the returned definition.
func LoadAgentDefinitionFile(path string) (AgentDefinition, error) {
	metadata, err := loadAgentMetadata(path)
	if err != nil {
		return AgentDefinition{}, err
	}
	return metadata.definition(), nil
}

func (m agentMetadata) definition() AgentDefinition {
	maxResultChars := m.MaxToolResultChars
	if maxResultChars <= 0 {
		maxResultChars = defaultAgentMaxToolResultChars
	}
	return AgentDefinition{
		Name:             m.AgentName,
		DisplayName:      m.DisplayName,
		Description:      m.Description,
		Enabled:          true,
		Healthy:          true,
		Parameters:       map[string]any{},
		SkillsLocked:     m.SkillsLocked,
		Source:           "bundled",
		SupportsPlanMode: boolDefault(m.EnablePlanMode, true),
		Unrestricted:     false,
		// Internal agents are hidden from user-facing catalogs. user_hidden is
		// a separate reversible curation flag for bundled agents that remain
		// available to existing sessions and compatibility lookups.
		UserHidden:              m.Internal || m.UserHidden || m.AgentName == "BOOKMARKER",
		Greeting:                strings.TrimSpace(m.Greeting),
		ExcludedTools:           append([]string(nil), m.ExcludedTools...),
		EnableSubtaskDelegation: boolDefault(m.EnableSubtaskDelegation, true),
		EnableWebSearch:         boolDefault(m.EnableWebSearch, true),
		EnableWebFetch:          boolDefault(m.EnableWebFetch, true),
		EnableThinking:          boolDefault(m.EnableThinking, true),
		MaxToolResultChars:      maxResultChars,
		SystemPrompt:            m.SystemPrompt,
		IdentityPrompt:          m.IdentityPrompt,
		WorkingStylePrompt:      m.WorkingStylePrompt,
	}
}

type operonSkillManifest struct {
	SchemaVersion int      `json:"schemaVersion"`
	Agent         string   `json:"agent"`
	Skills        []string `json:"skills"`
}

func loadOperonSkills(root string, manifest assets.Manifest) ([]string, error) {
	const relativePath = "operon-skills.json"
	if !manifestContainsPath(manifest, relativePath) {
		return nil, errors.New("agent catalog manifest does not include operon-skills.json")
	}
	raw, err := os.ReadFile(filepath.Join(root, relativePath))
	if err != nil {
		return nil, fmt.Errorf("read OPERON skill manifest: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var decoded operonSkillManifest
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode OPERON skill manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("OPERON skill manifest must contain one JSON value")
	}
	if decoded.SchemaVersion != 1 || !strings.EqualFold(decoded.Agent, "operon") || len(decoded.Skills) == 0 {
		return nil, errors.New("invalid OPERON skill manifest metadata")
	}
	seen := map[string]struct{}{}
	for _, name := range decoded.Skills {
		normalized := normalizeSkillName(name)
		if normalized == "" {
			return nil, errors.New("OPERON skill manifest contains an empty skill name")
		}
		if _, duplicate := seen[normalized]; duplicate {
			return nil, fmt.Errorf("OPERON skill manifest contains duplicate %q", name)
		}
		seen[normalized] = struct{}{}
	}
	if !sort.StringsAreSorted(decoded.Skills) {
		return nil, errors.New("OPERON skill manifest must be sorted")
	}
	return append([]string(nil), decoded.Skills...), nil
}

func verifyCatalogFileSet(root string, manifest assets.Manifest) error {
	declared := make(map[string]struct{}, len(manifest.Files))
	for _, entry := range manifest.Files {
		declared[filepath.ToSlash(entry.Path)] = struct{}{}
	}
	for _, directoryName := range manifest.Agents {
		required := filepath.ToSlash(filepath.Join(directoryName, "metadata.yaml"))
		if _, ok := declared[required]; !ok {
			return fmt.Errorf("agent catalog manifest is missing %q", required)
		}
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("agent catalog contains symbolic link %q", path)
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if _, ok := declared[relative]; !ok {
			return fmt.Errorf("agent catalog contains unmanifested file %q", relative)
		}
		return nil
	})
}

func manifestContainsPath(manifest assets.Manifest, path string) bool {
	for _, entry := range manifest.Files {
		if filepath.ToSlash(entry.Path) == path {
			return true
		}
	}
	return false
}

func validCatalogDirectoryName(name string) bool {
	if strings.TrimSpace(name) == "" || filepath.Base(name) != name || name == "." || name == ".." {
		return false
	}
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' {
			continue
		}
		return false
	}
	return true
}

func normalizedNameSet(names []string) map[string]struct{} {
	out := make(map[string]struct{}, len(names))
	for _, name := range names {
		if normalized := normalizeSkillName(name); normalized != "" {
			out[normalized] = struct{}{}
		}
	}
	return out
}

func normalizeSkillName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func normalizeAgentName(name string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(name), "-", "_"))
}

func boolDefault(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func cloneAgentDefinition(agent AgentDefinition) AgentDefinition {
	agent.Parameters = map[string]any{}
	agent.SkillNames = append([]string(nil), agent.SkillNames...)
	agent.ConnectorIDs = append([]string(nil), agent.ConnectorIDs...)
	agent.ExcludedTools = append([]string(nil), agent.ExcludedTools...)
	return agent
}
