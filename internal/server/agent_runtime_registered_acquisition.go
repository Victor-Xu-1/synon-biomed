package server

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strings"

	"synon-go/internal/sciencecapability"
)

func (g serverAgentRuntimeToolGateway) agentRuntimeRegisteredAcquisitionPreflight(tool string, input map[string]any) map[string]any {
	if tool != "web_fetch" && tool != "download_public_scientific_file" {
		return nil
	}
	return g.server.registeredAcquisitionPreflight(g.taskRun, tool, input)
}

// The catalog owns software bytes; public-source permission does not authorize
// substituting a different release or source tree for a selected execution pack.
// Unrelated repositories and normal data/page URLs retain their existing route.
func (s *Server) registeredAcquisitionPreflight(run *sessionRunnerChatRun, tool string, input map[string]any) map[string]any {
	if s == nil {
		return nil
	}
	source := strings.TrimSpace(stringValue(input["url"]))
	if source == "" {
		return nil
	}
	var matches []sciencecapability.ExecutionDownload
	for _, download := range s.registeredExecutionDownloadHints() {
		if download.URL == source {
			matches = append(matches, download)
		}
	}
	exact := len(matches) > 0
	if !exact {
		repository := registeredAcquisitionRepository(source)
		if repository == "" {
			return nil
		}
		for _, download := range s.selectedExecutionDownloadHints(run) {
			if registeredAcquisitionRepository(download.URL) == repository {
				matches = append(matches, download)
			}
		}
	}
	if len(matches) == 0 {
		return nil
	}
	if tool == "download_public_scientific_file" && exact {
		for _, download := range matches {
			if stringValue(input["filename"]) == download.Filename &&
				strings.EqualFold(stringValue(input["expected_sha256"]), download.SHA256) {
				return nil
			}
		}
	}
	status := "registered_execution_download_required"
	if tool == "web_fetch" && exact {
		status = "registered_binary_download_preflight_required"
	}
	result := map[string]any{
		"ok": false, "status": status, "executed": false,
		"next_tool": "download_public_scientific_file",
		"message":   "The registered execution pack requires its exact immutable download, not a page fetch, guessed release, or repository source archive.",
	}
	options := make([]map[string]any, 0, len(matches))
	for _, download := range matches {
		options = append(options, map[string]any{
			"url": download.URL, "filename": download.Filename,
			"expected_sha256":   strings.ToLower(download.SHA256),
			"human_description": fmt.Sprintf("Downloading %s", download.Filename),
		})
	}
	if len(options) == 1 {
		result["download"] = options[0]
		raw, _ := json.Marshal(options[0]) // String-only fields from the validated catalog.
		result["recovery"] = "Call download_public_scientific_file with exactly these arguments: " + string(raw) + ". The existing download service reuses checksum-verified owner-scoped content before any network request."
	} else {
		result["downloads"] = options
		result["recovery"] = "Use the matching declared download from downloads and its exact checksum; do not choose an arbitrary release or switch the selected implementation."
	}
	return result
}

// Recognize repository asset routes, not arbitrary same-host links. This does
// not authorize URLs or redirects; it only identifies an attempted replacement
// of software already pinned by the task's selected execution contract.
func registeredAcquisitionRepository(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil ||
		(u.Port() != "" && u.Port() != "443" && u.Port() != "80") {
		return ""
	}
	parts := strings.Split(strings.Trim(path.Clean(u.Path), "/"), "/")
	if len(parts) < 4 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	asset := false
	switch strings.ToLower(u.Hostname()) {
	case "github.com":
		asset = parts[2] == "archive" || (len(parts) >= 6 && parts[2] == "releases" && parts[3] == "download")
	case "codeload.github.com":
		asset = parts[2] == "zip" || parts[2] == "tar.gz" || parts[2] == "legacy.zip" || parts[2] == "legacy.tar.gz"
	}
	if !asset {
		return ""
	}
	return "github.com/" + strings.ToLower(parts[0]+"/"+parts[1])
}
