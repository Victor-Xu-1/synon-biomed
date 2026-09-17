package synonlink

import (
	"fmt"
	"strings"
)

type ActionProfile struct {
	Action                  string   `json:"action"`
	Family                  string   `json:"family"`
	ClientKind              string   `json:"clientKind"`
	Capabilities            []string `json:"capabilities"`
	ReadOnly                bool     `json:"readOnly"`
	AutoEligible            bool     `json:"autoEligible"`
	ApprovalPolicy          string   `json:"approvalPolicy"`
	EffectiveApprovalPolicy string   `json:"effectiveApprovalPolicy,omitempty"`
	ResultKind              string   `json:"resultKind"`
}

type PolicyDefaults struct {
	Mode              string `json:"mode,omitempty"`
	AutoAllowReadOnly bool   `json:"autoAllowReadOnly,omitempty"`
	RequireReason     bool   `json:"requireReason,omitempty"`
	RememberDecisions bool   `json:"rememberDecisions,omitempty"`
}

func ActionProfiles() []ActionProfile {
	return []ActionProfile{
		browserProfile("run_task_loop", "browser-task", []string{"taskLoop"}, false, false, "confirm", "task"),
		browserProfile("ensure_workspace", "browser-workspace", []string{"tabs", "tabGroups"}, false, false, "confirm", "tab"),
		browserProfile("open_local_files", "browser-local-files", []string{"localFiles"}, false, false, "confirm", "local-file"),
		browserProfile("local_files_status", "browser-local-files", []string{"localFiles"}, true, true, "none", "local-files-status"),
		browserProfile("local_files_manifest", "browser-local-files", []string{"localFiles", "localFileAdvanced"}, true, false, "confirm", "local-files-manifest"),
		browserProfile("local_files_list", "browser-local-files", []string{"localFiles"}, true, false, "confirm", "file-list"),
		browserProfile("local_files_search", "browser-local-files", []string{"localFiles"}, true, false, "confirm", "file-search"),
		browserProfile("local_file_info", "browser-local-files", []string{"localFiles"}, true, false, "confirm", "file-info"),
		browserProfile("local_file_read", "browser-local-files", []string{"localFiles"}, true, false, "confirm", "file-content"),
		browserProfile("local_file_write", "browser-local-files", []string{"localFiles", "localFileOperations"}, false, false, "confirm", "file-write"),
		browserProfile("local_file_copy", "browser-local-files", []string{"localFiles", "localFileOperations"}, false, false, "confirm", "file-copy"),
		browserProfile("local_file_move", "browser-local-files", []string{"localFiles", "localFileOperations"}, false, false, "confirm", "file-move"),
		browserProfile("local_file_delete", "browser-local-files", []string{"localFiles", "localFileOperations"}, false, false, "confirm", "file-delete"),
		browserProfile("local_file_mkdir", "browser-local-files", []string{"localFiles", "localFileOperations"}, false, false, "confirm", "file-mkdir"),
		browserProfile("local_files_organize", "browser-local-files", []string{"localFiles", "localFileAdvanced"}, false, false, "confirm", "local-files-organize"),
		browserProfile("bookmarks_status", "browser-personalization", []string{"personalization"}, true, true, "none", "status"),
		browserProfile("bookmarks_profile", "browser-personalization", []string{"bookmarks", "personalization"}, true, true, "none", "status"),
		browserProfile("revoke_bookmarks_permission", "browser-personalization", []string{"permissionReset"}, false, false, "confirm", "mutation"),
		browserProfile("open_personalization", "browser-personalization", []string{"personalization"}, false, false, "confirm", "tab"),
		browserProfile("search_web", "browser-research", []string{"browserSearch"}, true, true, "none", "search-results"),
		browserProfile("extract_search_results", "browser-research", []string{"browserSearch"}, true, true, "none", "search-results"),
		browserProfile("read_logged_in_page", "browser-research", []string{"loggedInPageRead"}, true, false, "confirm", "page-content"),
		browserProfile("open_tab", "browser-navigation", []string{"tabs"}, false, false, "confirm", "tab"),
		browserProfile("get_active_tab", "browser-navigation", []string{"tabs"}, true, true, "none", "tab"),
		browserProfile("extract_page", "browser-research", []string{"humanPageRead"}, true, true, "none", "page-content"),
		browserProfile("read_page", "browser-research", []string{"humanPageRead"}, true, true, "none", "page-content"),
		browserProfile("refresh_tab", "browser-navigation", []string{"refreshTab"}, true, true, "none", "tab"),
		browserProfile("scroll_page", "browser-navigation", []string{"scrollPage"}, true, true, "none", "tab"),
		browserProfile("wait_for_page", "browser-navigation", []string{"pageWait"}, true, true, "none", "tab"),
		browserProfile("click", "browser-interaction", []string{"scripting"}, false, false, "confirm", "interaction"),
		browserProfile("click_at", "browser-interaction", []string{"visualClick"}, false, false, "confirm", "interaction"),
		browserProfile("type", "browser-interaction", []string{"scripting"}, false, false, "confirm", "interaction"),
		browserProfile("screenshot", "browser-research", []string{"captureVisibleTab"}, true, true, "none", "screenshot"),
		browserProfile("visual_element_map", "browser-research", []string{"visualElementMap"}, true, true, "none", "visual-map"),
		browserProfile("find_download_links", "browser-download", []string{"downloadDiscovery"}, true, true, "none", "download-links"),
		browserProfile("download_from_page", "browser-download", []string{"downloads", "downloadDiscovery"}, false, false, "confirm", "download"),
		browserProfile("download_file", "browser-download", []string{"downloads"}, false, false, "confirm", "download"),
		browserProfile("open_downloads_folder", "browser-download", []string{"downloadOpen"}, false, false, "confirm", "download-open"),
		browserProfile("show_downloaded_file", "browser-download", []string{"downloadOpen"}, false, false, "confirm", "download-open"),
		browserProfile("cleanup_tabs", "browser-workspace", []string{"tabCleanup"}, false, false, "confirm", "tab-cleanup"),
		browserProfile("clear_local_files", "browser-local-files", []string{"permissionReset"}, false, false, "confirm", "mutation"),
		browserProfile("close_tab", "browser-navigation", []string{"tabs"}, false, false, "confirm", "tab"),
	}
}

// ActionNames returns the public Synon Link action contract in stable profile
// order. The current contract is browser-only; callers must not maintain a
// second action allowlist that can accidentally re-enable removed transports.
func ActionNames() []string {
	profiles := ActionProfiles()
	actions := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		actions = append(actions, profile.Action)
	}
	return actions
}

func browserProfile(action string, family string, capabilities []string, readOnly bool, autoEligible bool, approvalPolicy string, resultKind string) ActionProfile {
	return ActionProfile{
		Action:         action,
		Family:         family,
		ClientKind:     "browser",
		Capabilities:   capabilities,
		ReadOnly:       readOnly,
		AutoEligible:   autoEligible,
		ApprovalPolicy: approvalPolicy,
		ResultKind:     resultKind,
	}
}

func PolicyMatrix() map[string]ActionProfile {
	matrix := make(map[string]ActionProfile)
	for _, profile := range ActionProfiles() {
		matrix[profile.Action] = profile
	}
	return matrix
}

func ValidateCommandPolicy(client Client, action string, payload map[string]any) error {
	return ValidateCommandPolicyWithDefaults(client, action, payload, PolicyDefaults{})
}

func ValidateCommandPolicyWithDefaults(client Client, action string, payload map[string]any, defaults PolicyDefaults) error {
	profile, ok := PolicyMatrix()[action]
	if !ok {
		return fmt.Errorf("unsupported action %s", action)
	}
	if client.Kind != "" && profile.ClientKind != "" && client.Kind != profile.ClientKind {
		return fmt.Errorf("action %s requires %s client", action, profile.ClientKind)
	}
	if len(client.SupportedActions) > 0 && !containsString(client.SupportedActions, action) {
		return fmt.Errorf("client %s does not support action %s", client.ID, action)
	}
	for _, capability := range profile.Capabilities {
		if !containsString(client.Capabilities, capability) {
			return fmt.Errorf("client %s missing capability %s for action %s", client.ID, capability, action)
		}
	}
	if commandApproved, hasReason := commandApprovalState(action, payload); commandApproved {
		if normalizePolicyMode(defaults.Mode) != "" && defaults.RequireReason && !hasReason {
			return fmt.Errorf("action %s approval requires reason", action)
		}
		return nil
	}
	if commandAutoAllowed(profile, defaults) {
		return nil
	}
	switch normalizePolicyMode(defaults.Mode) {
	case "allow":
		return nil
	case "deny":
		return fmt.Errorf("action %s denied by approval defaults", action)
	default:
		return fmt.Errorf("action %s requires approval", action)
	}
}

func hasCommandApproval(action string, payload map[string]any) bool {
	approved, _ := commandApprovalState(action, payload)
	return approved
}

func commandApprovalState(action string, payload map[string]any) (bool, bool) {
	if payload == nil {
		return false, false
	}
	if boolValue(payload["approved"]) || boolValue(payload["approvalGranted"]) {
		return true, approvalReasonPresent(payload)
	}
	if approval, ok := payload["approval"].(map[string]any); ok {
		if boolValue(approval["approved"]) || boolValue(approval["granted"]) {
			return true, approvalReasonPresent(approval)
		}
	}
	if isLocalMutationAction(action) && boolValue(payload["allowApply"]) {
		if dryRun, ok := payload["dryRun"].(bool); ok && !dryRun {
			return true, approvalReasonPresent(payload)
		}
	}
	return false, false
}

func approvalReasonPresent(payload map[string]any) bool {
	return approvalReasonValue(payload) != ""
}

func approvalReasonValue(payload map[string]any) string {
	for _, key := range []string{"reason", "approvalReason", "decisionReason"} {
		if value := strings.TrimSpace(stringValue(payload[key])); value != "" {
			return value
		}
	}
	if approval, ok := payload["approval"].(map[string]any); ok {
		for _, key := range []string{"reason", "approvalReason", "decisionReason"} {
			if value := strings.TrimSpace(stringValue(approval[key])); value != "" {
				return value
			}
		}
	}
	return ""
}

func ActionProfilesWithDefaults(defaults PolicyDefaults) []ActionProfile {
	profiles := ActionProfiles()
	for index := range profiles {
		profiles[index].EffectiveApprovalPolicy = effectiveApprovalPolicy(profiles[index], defaults)
	}
	return profiles
}

func PolicyMatrixWithDefaults(defaults PolicyDefaults) map[string]ActionProfile {
	matrix := make(map[string]ActionProfile)
	for _, profile := range ActionProfilesWithDefaults(defaults) {
		matrix[profile.Action] = profile
	}
	return matrix
}

func effectiveApprovalPolicy(profile ActionProfile, defaults PolicyDefaults) string {
	if commandAutoAllowed(profile, defaults) {
		return "none"
	}
	switch normalizePolicyMode(defaults.Mode) {
	case "allow":
		return "none"
	case "deny":
		return "deny"
	default:
		return profile.ApprovalPolicy
	}
}

func commandAutoAllowed(profile ActionProfile, defaults PolicyDefaults) bool {
	if profile.AutoEligible {
		return true
	}
	return defaults.AutoAllowReadOnly && profile.ReadOnly
}

func NormalizePolicyDefaults(value any) (PolicyDefaults, error) {
	raw, ok := value.(map[string]any)
	if !ok {
		return PolicyDefaults{}, fmt.Errorf("approval defaults must be a JSON object")
	}
	defaults := PolicyDefaults{}
	for key, rawValue := range raw {
		switch key {
		case "mode":
			mode := normalizePolicyMode(stringValue(rawValue))
			switch mode {
			case "", "confirm", "ask", "deny", "allow":
				defaults.Mode = mode
			default:
				return PolicyDefaults{}, fmt.Errorf("approval defaults mode must be confirm, ask, deny, or allow")
			}
		case "autoAllowReadOnly":
			typed, ok := rawValue.(bool)
			if !ok {
				return PolicyDefaults{}, fmt.Errorf("approval defaults %s must be boolean", key)
			}
			defaults.AutoAllowReadOnly = typed
		case "requireReason":
			typed, ok := rawValue.(bool)
			if !ok {
				return PolicyDefaults{}, fmt.Errorf("approval defaults %s must be boolean", key)
			}
			defaults.RequireReason = typed
		case "rememberDecisions":
			typed, ok := rawValue.(bool)
			if !ok {
				return PolicyDefaults{}, fmt.Errorf("approval defaults %s must be boolean", key)
			}
			defaults.RememberDecisions = typed
		default:
			return PolicyDefaults{}, fmt.Errorf("unknown approval defaults field: %s", key)
		}
	}
	return defaults, nil
}

func normalizePolicyMode(mode string) string {
	return strings.ToLower(strings.TrimSpace(mode))
}

func isLocalMutationAction(action string) bool {
	switch action {
	case "local_file_write", "local_file_copy", "local_file_move", "local_file_delete", "local_file_mkdir", "local_files_organize", "clear_local_files":
		return true
	default:
		return false
	}
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return ""
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
