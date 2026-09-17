package harnesscontract

import (
	"runtime"
	"sort"
	"strings"
)

type ToolSurface string

const (
	ToolSurfaceModelRoot        ToolSurface = "model_root"
	ToolSurfaceFixedJob         ToolSurface = "fixed_job"
	ToolSurfaceDynamicMCP       ToolSurface = "dynamic_mcp"
	ToolSurfaceInternalControl  ToolSurface = "internal_control_api"
	ToolSurfaceCompatibilityAPI ToolSurface = "compatibility_api"
)

var rootModelTools = []string{
	"ask_about_compute", "ask_user", "compute_details", "compute_provider",
	"delete_host_files", "edit_file", "fetch_article_fulltext", "generate_plan",
	"list_compute", "list_host_grants", "manage_environments", "manage_packages", "download_public_scientific_file",
	"python", "r", "read_file", "read_memory", "repl", "request_host_access",
	"request_network_access", "save_artifacts", "search_memory", "search_skills",
	"skill", "update_step_status", "wait_for_notification", "web_fetch", "web_search", "write_memory",
}

var fixedJobModelTools = []string{
	"submit_output", "verdict", "record_summary", "summarize_conversation",
	"emit_memories", "report_input_files", "select_relevant_inputs", "select_skills",
	"create_work_item", "read_onboarding_attachment",
}

func RootModelTools() []string {
	output := append([]string(nil), rootModelTools...)
	if runtime.GOOS == "windows" {
		output = append(output, "powershell")
	} else {
		output = append(output, "bash")
	}
	sort.Strings(output)
	return output
}

func FixedJobModelTools() []string {
	output := append([]string(nil), fixedJobModelTools...)
	sort.Strings(output)
	return output
}

func ModelToolAllowed(name string) bool {
	key := strings.TrimSpace(name)
	if key == "" {
		return false
	}
	for _, candidate := range rootModelTools {
		if key == candidate {
			return true
		}
	}
	if key == "bash" {
		return runtime.GOOS != "windows"
	}
	if key == "powershell" {
		return runtime.GOOS == "windows"
	}
	for _, candidate := range fixedJobModelTools {
		if key == candidate {
			return true
		}
	}
	return false
}

func ClassifyToolSurface(name string) ToolSurface {
	key := strings.TrimSpace(name)
	if strings.HasPrefix(key, "mcp__") {
		return ToolSurfaceDynamicMCP
	}
	for _, candidate := range RootModelTools() {
		if key == candidate {
			return ToolSurfaceModelRoot
		}
	}
	for _, candidate := range fixedJobModelTools {
		if key == candidate {
			return ToolSurfaceFixedJob
		}
	}
	for _, prefix := range []string{
		"approval_remembered_", "artifact_", "pairing_", "runtime_", "session_", "settings_", "task_",
	} {
		if strings.HasPrefix(key, prefix) {
			return ToolSurfaceInternalControl
		}
	}
	if key == "cron_tick" || key == "im_config" || key == "im_message" {
		return ToolSurfaceInternalControl
	}
	return ToolSurfaceCompatibilityAPI
}
