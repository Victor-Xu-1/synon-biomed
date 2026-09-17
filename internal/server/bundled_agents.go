package server

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"synon-go/internal/agentruntime"
	"synon-go/internal/harnesscontract"
	sessionstore "synon-go/internal/persistence/sessions"
	"synon-go/internal/skills"
	"synon-go/internal/toolcontract"
)

const bundledOperonID = "3bcb40e9-b995-5b66-a209-8ed8ef1f68ef"

func loadBundledAgentCatalog(options Options, skillCatalog *skills.Catalog) (*agentruntime.AgentCatalog, error) {
	if options.AgentCatalog != nil {
		return options.AgentCatalog, nil
	}
	availableSkills := make([]string, 0)
	if skillCatalog != nil {
		for _, skill := range skillCatalog.Skills() {
			availableSkills = append(availableSkills, skill.Name)
		}
	}
	root := strings.TrimSpace(options.AgentCatalogRoot)
	manifestPath := strings.TrimSpace(options.AgentManifestPath)
	if root != "" || manifestPath != "" {
		if root == "" || manifestPath == "" {
			return nil, errors.New("agent catalog root and manifest path must be configured together")
		}
		return agentruntime.LoadCatalog(agentruntime.CatalogOptions{Root: root, ManifestPath: manifestPath, AvailableSkills: availableSkills})
	}
	for _, candidate := range defaultAgentCatalogCandidates() {
		catalog, err := agentruntime.LoadCatalog(agentruntime.CatalogOptions{
			Root: candidate.root, ManifestPath: candidate.manifest, AvailableSkills: availableSkills,
		})
		if err != nil {
			return nil, err
		}
		return catalog, nil
	}
	return nil, errors.New("bundled agent catalog assets were not found")
}

type agentCatalogCandidate struct {
	root     string
	manifest string
}

func defaultAgentCatalogCandidates() []agentCatalogCandidate {
	starts := make([]string, 0, 2)
	if cwd, err := os.Getwd(); err == nil {
		starts = append(starts, cwd)
	}
	if executable, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(executable))
	}
	seen := map[string]struct{}{}
	candidates := make([]agentCatalogCandidate, 0)
	for _, start := range starts {
		for current := filepath.Clean(start); ; current = filepath.Dir(current) {
			root := filepath.Join(current, "assets", "synonbiomed", "agents")
			manifest := filepath.Join(current, "assets", "synonbiomed", "agents.manifest.json")
			key := strings.ToLower(filepath.Clean(root))
			if _, duplicate := seen[key]; !duplicate {
				seen[key] = struct{}{}
				if rootInfo, rootErr := os.Stat(root); rootErr == nil && rootInfo.IsDir() {
					if manifestInfo, manifestErr := os.Stat(manifest); manifestErr == nil && manifestInfo.Mode().IsRegular() {
						candidates = append(candidates, agentCatalogCandidate{root: root, manifest: manifest})
					}
				}
			}
			parent := filepath.Dir(current)
			if parent == current {
				break
			}
		}
	}
	return candidates
}

func (s *Server) handleBundledAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if s == nil || s.agentCatalog == nil || s.agentCatalogError != nil || !s.agentCatalog.Ready() {
		message := "bundled agent catalog is unavailable"
		if s != nil && s.agentCatalogError != nil {
			message += ": " + s.agentCatalogError.Error()
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": message})
		return
	}
	wanted := requestedAgentNames(r)
	includeMetadata := queryBoolean(r, "include_metadata")
	lite := queryBoolean(r, "lite")
	response := make([]map[string]any, 0)
	for _, agent := range s.agentCatalog.Agents() {
		if len(wanted) > 0 {
			if _, ok := wanted[normalizeBundledAgentName(agent.Name)]; !ok {
				continue
			}
		}
		projection := bundledAgentProjection(agent, includeMetadata, lite, s.skillCatalog)
		if s.workspaceStore != nil {
			userID := compatAgentUserID(r)
			profile, found, err := s.workspaceStore.GetAgent(userID, agent.Name)
			if err != nil {
				writeJSON(w, workspaceStatus(err), map[string]any{"detail": err.Error()})
				return
			}
			if found {
				projection["skillNames"] = s.effectiveBundledAgentSkillNames(agent.Name, profile.SkillNames, profile.SkillTombstones)
				projection["skillTombstones"] = append([]string(nil), profile.SkillTombstones...)
				projection["connectorTombstones"] = append([]string(nil), profile.ConnectorTombstones...)
				connectorIDs, connectorErr := s.bundledAgentConnectorIDsForProfile(userID, agent.Name, profile)
				if connectorErr != nil {
					writeJSON(w, workspaceStatus(connectorErr), map[string]any{"detail": connectorErr.Error()})
					return
				}
				if len(connectorIDs) > 0 || len(agent.ConnectorIDs) > 0 {
					projection["connectorIds"] = connectorIDs
				}
			}
		}
		response = append(response, projection)
	}
	if s.workspaceStore != nil {
		profiles, err := s.workspaceStore.ListAgents(compatAgentUserID(r))
		if err != nil {
			writeJSON(w, workspaceStatus(err), map[string]any{"detail": err.Error()})
			return
		}
		for _, profile := range profiles {
			if _, bundled := s.agentCatalog.Agent(profile.Name); bundled {
				continue
			}
			if len(wanted) > 0 {
				if _, ok := wanted[normalizeBundledAgentName(profile.Name)]; !ok {
					continue
				}
			}
			projection := userAgentProjection(profile)
			if lite {
				delete(projection, "parameters")
				delete(projection, "systemPrompt")
			}
			response = append(response, projection)
		}
		sort.Slice(response, func(i, j int) bool {
			return response[i]["name"].(string) < response[j]["name"].(string)
		})
	}
	writeJSON(w, http.StatusOK, response)
}

func bundledAgentProjection(agent agentruntime.AgentDefinition, includeMetadata, lite bool, skillCatalog *skills.Catalog) map[string]any {
	capabilities, specialist := capabilitiesFromAgent(agent)
	response := map[string]any{
		"description":      agent.Description,
		"displayName":      agent.DisplayName,
		"enabled":          agent.Enabled,
		"healthy":          agent.Healthy,
		"name":             agent.Name,
		"skillsLocked":     agent.SkillsLocked,
		"source":           agent.Source,
		"supportsPlanMode": agent.SupportsPlanMode,
		"unrestricted":     agent.Unrestricted,
		"userHidden":       agent.UserHidden,
	}
	if !lite {
		response["parameters"] = map[string]any{}
		response["systemPrompt"] = nil
	}
	if strings.TrimSpace(agent.Greeting) != "" {
		response["greeting"] = agent.Greeting
	}
	if agent.Name == "OPERON" {
		response["id"] = bundledOperonID
		response["iconKey"] = "lightning"
		response["colorKey"] = "accent-main"
		response["skillNames"] = append([]string(nil), agent.SkillNames...)
		response["skillTombstones"] = []string{}
		response["connectorTombstones"] = []string{}
		if !lite {
			response["systemPrompt"] = ""
		}
	} else if specialist {
		response["skillNames"] = append([]string(nil), capabilities.Skills...)
		response["skillTombstones"] = []string{}
		response["connectorTombstones"] = []string{}
		response["connectorIds"] = append([]string(nil), capabilities.Connectors...)
	}
	if includeMetadata {
		metadata := map[string]any{
			"description":    "",
			"displayName":    agent.DisplayName,
			"enabled":        agent.Enabled,
			"excluded_tools": append([]string{}, agent.ExcludedTools...),
			"mcp_servers":    []string{},
			"name":           agent.Name,
			"parameters":     map[string]any{},
			"skills":         []string{},
			"source":         agent.Source,
			"tags":           []string{},
			"tools":          []string{},
		}
		if !lite {
			metadata["system_prompt"] = agent.EffectiveSystemPrompt()
		}
		if agent.Name == "OPERON" {
			metadata["id"] = bundledOperonID
			metadata["iconKey"] = "lightning"
			metadata["colorKey"] = "accent-main"
			metadata["skills"] = bundledAgentSkillMetadata(agent.SkillNames, skillCatalog)
		} else if specialist {
			metadata["skills"] = bundledAgentSkillMetadata(capabilities.Skills, skillCatalog)
			metadata["mcp_servers"] = append([]string(nil), capabilities.Connectors...)
		}
		response["metadata"] = metadata
	}
	return response
}

func bundledAgentSkillMetadata(names []string, catalog *skills.Catalog) []map[string]any {
	byName := map[string]skills.Skill{}
	if catalog != nil {
		for _, skill := range catalog.Skills() {
			byName[strings.ToLower(strings.TrimSpace(skill.Name))] = skill
		}
	}
	metadata := make([]map[string]any, 0, len(names))
	for _, name := range names {
		skill, found := byName[strings.ToLower(strings.TrimSpace(name))]
		description := ""
		if found {
			description = strings.TrimSpace(skill.Description)
		}
		metadata = append(metadata, map[string]any{
			"content":     nil,
			"description": description,
			"examples":    []string{},
			"name":        name,
		})
	}
	return metadata
}

func requestedAgentNames(r *http.Request) map[string]struct{} {
	values := make([]string, 0)
	for _, value := range r.URL.Query()["names"] {
		values = append(values, strings.Split(value, ",")...)
	}
	wanted := make(map[string]struct{}, len(values))
	for _, value := range values {
		if normalized := normalizeBundledAgentName(value); normalized != "" {
			wanted[normalized] = struct{}{}
		}
	}
	return wanted
}

func normalizeBundledAgentName(name string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(name), "-", "_"))
}

func queryBoolean(r *http.Request, name string) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (s *Server) applySessionRunnerBundledAgent(
	session sessionstore.Session,
	options SessionRunnerChatOptions,
	runtimeToolUniverse ...[]string,
) (SessionRunnerChatOptions, string) {
	if s == nil || s.agentCatalog == nil || s.agentCatalogError != nil {
		return options, ""
	}
	agentName := ""
	if s.workspaceStore != nil {
		if frame, found, err := s.workspaceStore.GetFrame(session.ID); err == nil && found {
			agentName = frame.AgentName
		}
	}
	if strings.TrimSpace(agentName) == "" {
		agentName = sessionConfiguredAgentName(session)
	}
	agent, found := s.agentCatalog.Agent(agentName)
	if !found {
		return options, ""
	}
	if prompt := agent.EffectiveSystemPrompt(); strings.TrimSpace(prompt) != "" {
		options.SystemPrompt = prompt
	}
	// OPERON's signed catalog is a discovery allowlist. Even if an older
	// catalog marks it as locked, injecting every catalog entry as an explicit
	// per-turn selection can exceed the runtime selection limit and prevents
	// the model from discovering the current skill set.
	operonDynamicDiscovery := strings.EqualFold(strings.TrimSpace(agent.Name), "OPERON")
	options.DisableSkillDiscovery = (agent.SkillsLocked && !operonDynamicDiscovery) || agentExcludesSkillDiscovery(agent.ExcludedTools)
	options.DisableThinking = !agent.EnableThinking
	if agent.MaxToolResultChars > 0 && (options.OutputLimitBytes <= 0 || options.OutputLimitBytes > int64(agent.MaxToolResultChars)) {
		options.OutputLimitBytes = int64(agent.MaxToolResultChars)
	}
	excluded := append([]string(nil), agent.ExcludedTools...)
	if !agent.SupportsPlanMode {
		excluded = append(excluded, generatePlanToolName)
	}
	// Delegation is a kernel-host capability. The legacy direct Agent/TaskRun
	// model tools are never admitted to a new snapshot; the conversation toggle
	// only adds guidance for the single host.delegate authority.
	delegationEnabled := sessionRunnerDelegationEnabled(session)
	if delegationEnabled {
		options.SystemPrompt = appendSessionRunnerDelegationGuidance(options.SystemPrompt)
	}
	excluded = append(excluded, "Agent", "TaskRun", "agent_run", "task_run")
	if !agent.EnableWebSearch {
		excluded = append(excluded, "web_search", "web_research")
	}
	if !agent.EnableWebFetch {
		excluded = append(excluded, "web_fetch")
	}
	if agent.SkillsLocked && !operonDynamicDiscovery {
		excluded = append(excluded, "skill", "search_skills", "Skill", "SkillSearch", "skill_search")
		options.SelectedSkillNames = appendUniqueFolded(options.SelectedSkillNames, agent.SkillNames...)
		options.AllowedSkillNames = appendUniqueFolded(options.AllowedSkillNames, agent.SkillNames...)
		options.RestrictSkillDiscovery = true
	}
	if len(runtimeToolUniverse) > 0 {
		if operonDynamicDiscovery && s != nil && s.skillCatalog != nil {
			// OPERON follows the reference Harness shape: a stable compact base
			// tool set plus exact task-scoped Skill additions. A partial caller
			// allowlist must not accidentally remove repl, environment management,
			// file editing, or artifact publication while leaving their instructions
			// in the prompt. Explicit exclusions and delegated-agent policy still
			// apply below.
			options.AllowedTools = appendAvailableModelRootToolNames(
				options.AllowedTools, runtimeToolUniverse[0],
			)
			options.AllowedTools = appendAvailableSkillDeclaredToolNames(
				options.AllowedTools,
				runtimeToolUniverse[0],
				s.skillCatalog.Skills(),
				agent.SkillNames,
			)
		}
		options.AllowedTools = appendAvailableAgentFrameToolNames(options.AllowedTools, runtimeToolUniverse[0])
	}
	if len(excluded) > 0 {
		available := s.registeredToolNames()
		if len(runtimeToolUniverse) > 0 {
			available = runtimeToolUniverse[0]
		}
		options.AllowedTools = filterAgentAllowedTools(options.AllowedTools, available, excluded)
	}
	return options, agent.Name
}

func (s *Server) applySessionRunnerAgentProfile(
	session sessionstore.Session,
	options SessionRunnerChatOptions,
	runtimeToolUniverse ...[]string,
) (SessionRunnerChatOptions, string, error) {
	options, selected := s.applySessionRunnerBundledAgent(session, options, runtimeToolUniverse...)
	if s == nil || s.workspaceStore == nil {
		return options, selected, nil
	}
	frameContext, found, err := s.workspaceStore.GetFrameRealtimeContext(session.ID)
	if err != nil {
		return options, selected, err
	}
	if !found {
		return options, selected, nil
	}
	userID := strings.TrimSpace(frameContext.UserID)
	agentName := strings.TrimSpace(frameContext.Frame.AgentName)
	profile, profileFound, err := s.workspaceStore.GetAgent(userID, agentName)
	if err != nil {
		return options, selected, err
	}
	if profileFound {
		if !profile.Enabled {
			return options, selected, errors.New("selected agent is disabled")
		}
		selected = profile.Name
		if prompt := strings.TrimSpace(profile.SystemPrompt); prompt != "" {
			options.SystemPrompt = prompt
		}
		options.ExcludedSkillNames = appendUniqueFolded(options.ExcludedSkillNames, profile.SkillTombstones...)
		if strings.EqualFold(agentName, "OPERON") || strings.EqualFold(strings.TrimSpace(profile.Name), "OPERON") {
			// OPERON's signed skill manifest is a discovery allowlist, not a
			// request to inject every skill into every turn.
			options.AllowedSkillNames = appendUniqueFolded(nil, profile.SkillNames...)
			options.RestrictSkillDiscovery = true
		} else if profile.Unrestricted {
			options.RestrictSkillDiscovery = false
			options.AllowedSkillNames = nil
		} else {
			options.SelectedSkillNames = appendUniqueFolded(options.SelectedSkillNames, profile.SkillNames...)
			options.AllowedSkillNames = appendUniqueFolded(nil, profile.SkillNames...)
			options.RestrictSkillDiscovery = true
		}
	}
	options, assistantName, err := s.applyWebAssistantRuntimeOptions(session, userID, agentName, options)
	if err != nil {
		return options, selected, err
	}
	if assistantName != "" {
		selected = assistantName
	}
	options = applyStructuredOnboardingRuntime(structuredOnboardingSessionConfig(session.Orchestration), agentName, options)
	return options, selected, nil
}

// applySessionRunnerAgentModelAuthority resolves the selected agent, assistant,
// prompt, skill policy, and model overrides before provider selection without
// freezing a partial tool allowlist. The authoritative tool binding happens
// once later, after dynamic workspace and MCP schemas have been discovered.
func (s *Server) applySessionRunnerAgentModelAuthority(
	session sessionstore.Session,
	options SessionRunnerChatOptions,
) (SessionRunnerChatOptions, string, error) {
	configuredAllowedTools := append([]string(nil), options.AllowedTools...)
	resolved, selected, err := s.applySessionRunnerAgentProfile(session, options)
	resolved.AllowedTools = configuredAllowedTools
	return resolved, selected, err
}

func sessionRunnerDelegationEnabled(session sessionstore.Session) bool {
	config, ok := session.Orchestration["sessionConfig"].(map[string]any)
	if !ok {
		return false
	}
	return boolValue(config["ultraMode"], false) || boolValue(config["ultra_mode"], false)
}

func appendSessionRunnerDelegationGuidance(prompt string) string {
	const guidance = "Parallel delegation is enabled for this conversation. When the task has independent subtasks, use the persistent Python or repl kernel and the single host.delegate supervision API; use one list call for parallel fan-out, host.collect for asynchronous results, and host.send_message for follow-up work on an existing child. Synthesize every child result and report child failures explicitly. Do not use legacy direct delegation tool names and do not delegate trivial one-step work."
	if strings.Contains(prompt, guidance) {
		return prompt
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return guidance
	}
	return prompt + "\n\n" + guidance
}

func sessionConfiguredAgentName(session sessionstore.Session) string {
	config, ok := session.Orchestration["sessionConfig"].(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"agentName", "agent_name", "agent"} {
		if value, ok := config[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func appendUniqueFolded(existing []string, values ...string) []string {
	seen := make(map[string]struct{}, len(existing)+len(values))
	result := make([]string, 0, len(existing)+len(values))
	for _, group := range [][]string{existing, values} {
		for _, value := range group {
			value = strings.TrimSpace(value)
			key := strings.ToLower(value)
			if key == "" {
				continue
			}
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, value)
		}
	}
	return result
}

func filterAgentAllowedTools(parentAllowed, registered, excluded []string) []string {
	excludedSet := make(map[string]struct{}, len(excluded))
	for _, name := range excluded {
		addAgentToolExclusions(excludedSet, name)
	}
	candidates := append([]string(nil), parentAllowed...)
	if len(candidates) == 0 {
		candidates = append(candidates, registered...)
	}
	filtered := make([]string, 0, len(candidates))
	seen := map[string]struct{}{}
	for _, name := range candidates {
		normalized := normalizeAgentToolName(name)
		if _, blocked := excludedSet[normalized]; blocked {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		filtered = append(filtered, name)
	}
	if len(filtered) == 0 {
		return []string{noChatToolsAllowedSentinel}
	}
	return filtered
}

func appendAvailableAgentFrameToolNames(allowed, available []string) []string {
	if len(allowed) == 0 || len(available) == 0 {
		return allowed
	}
	for _, name := range allowed {
		if strings.EqualFold(strings.TrimSpace(name), noChatToolsAllowedSentinel) {
			return allowed
		}
	}
	dynamic := make([]string, 0, len(available))
	for _, name := range available {
		if isAgentFrameToolName(name) {
			dynamic = append(dynamic, name)
		}
	}
	return appendUniqueFolded(allowed, dynamic...)
}

func appendAvailableModelRootToolNames(allowed, available []string) []string {
	if len(allowed) == 0 || len(available) == 0 {
		return allowed
	}
	for _, name := range allowed {
		if strings.EqualFold(strings.TrimSpace(name), noChatToolsAllowedSentinel) {
			return allowed
		}
	}
	root := make([]string, 0, len(available))
	for _, name := range available {
		if harnesscontract.ClassifyToolSurface(strings.TrimSpace(name)) == harnesscontract.ToolSurfaceModelRoot {
			root = append(root, name)
		}
	}
	return appendUniqueFolded(allowed, root...)
}

// appendAvailableSkillDeclaredToolNames keeps the model's root tool surface
// compact without making Skill guidance non-executable. OPERON starts from a
// deliberately small fixed allowlist; tools declared by an allowed catalog
// Skill are admitted to the runner authority here, then exposed to the model
// only when that exact Skill is selected or auto-referenced. Missing tools and
// flattened MCP methods never gain authority through a Skill declaration.
func appendAvailableSkillDeclaredToolNames(
	allowed, available []string,
	catalogSkills []skills.Skill,
	allowedSkillNames []string,
) []string {
	if len(available) == 0 || len(catalogSkills) == 0 || len(allowedSkillNames) == 0 {
		return allowed
	}
	availableSet := make(map[string]string, len(available))
	for _, name := range available {
		canonical, ok := toolcontract.NormalizeRuntimeName(name)
		if !ok || strings.HasPrefix(canonical, "mcp__") {
			continue
		}
		availableSet[strings.ToLower(canonical)] = canonical
	}
	allowedSkills := normalizedSkillNameSet(allowedSkillNames)
	declared := make([]string, 0)
	for _, skill := range catalogSkills {
		if _, ok := allowedSkills[strings.ToLower(strings.TrimSpace(skill.Name))]; !ok {
			continue
		}
		for _, name := range skill.Tools {
			canonical, ok := toolcontract.NormalizeRuntimeName(name)
			if !ok || strings.HasPrefix(canonical, "mcp__") {
				continue
			}
			if availableName, exists := availableSet[strings.ToLower(canonical)]; exists {
				declared = append(declared, availableName)
			}
		}
	}
	return appendUniqueFolded(allowed, declared...)
}

func isAgentFrameToolName(name string) bool {
	// Dynamic MCP methods are resolved from the owner-scoped connector snapshot
	// at turn start. They remain internal kernel-host authorities (the model only
	// sees the single repl surface), but must survive an explicit root allowlist
	// so host.mcp can execute the exact connected method selected at runtime.
	if strings.HasPrefix(strings.TrimSpace(name), "mcp__") {
		return true
	}
	if strings.TrimSpace(name) == softwareRuntimeToolName {
		return false
	}
	switch strings.TrimSpace(name) {
	case "read_memory", "write_memory", "search_memory",
		"save_artifacts", "search_rcsb_structures", "download_rcsb_file", "download_public_scientific_file", "wait_for_notification", onboardingReadAttachmentToolName:
		return true
	default:
		return isAgentEnvironmentManagementTool(name) || isAgentKernelToolName(name) || isAgentWorkspaceFileTool(name)
	}
}

func agentExcludesSkillDiscovery(excluded []string) bool {
	for _, name := range excluded {
		switch normalizeAgentToolName(name) {
		case "skill", "skillsearch", "searchskills":
			return true
		}
	}
	return false
}

func addAgentToolExclusions(target map[string]struct{}, name string) {
	normalized := normalizeAgentToolName(name)
	if normalized == "" {
		return
	}
	target[normalized] = struct{}{}
	aliases := []string{}
	switch normalized {
	case "bash", "shell", "shellexec":
		aliases = []string{"Shell", "shell_exec", "powershell", "Bash", "TaskOutput", "TaskStop"}
	case "websearch", "searchweb":
		aliases = []string{"web_search", "web_research"}
	case "webfetch", "fetchweb":
		aliases = []string{"web_fetch"}
	case "searchskills", "skillsearch", "skill":
		aliases = []string{"skill", "search_skills", "skill_search"}
	case "agent", "agentrun", "taskrun":
		aliases = []string{"Agent", "TaskRun", "TaskCreate", "TeamCreate", "agent_run", "task_run", "task_create"}
	case "editfile", "writefile":
		aliases = []string{"Edit", "Patch", "Write", "NotebookEdit", "file_patch", "file_replace", "file_write", "json_patch"}
	case "readfile":
		aliases = []string{"Read", "ReadBatch", "file_read", "file_list", "file_info", "file_search", "Glob", "Grep", "code_index", "code_references"}
	case "saveartifacts":
		aliases = []string{"artifact_register", "artifact_save", "save_artifacts"}
	}
	for _, alias := range aliases {
		target[normalizeAgentToolName(alias)] = struct{}{}
	}
}

func normalizeAgentToolName(name string) string {
	var builder strings.Builder
	for _, char := range strings.ToLower(strings.TrimSpace(name)) {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			builder.WriteRune(char)
		}
	}
	return builder.String()
}
