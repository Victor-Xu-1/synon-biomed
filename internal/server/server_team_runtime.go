package server

import (
	"encoding/json"
	"errors"
	"fmt"

	"os"

	"path/filepath"

	"strings"

	"time"

	"synon-go/internal/toolcontract"
)

const teamRuntimeNamespace = "teams"

func (s *Server) executeAskUserQuestionTool(toolName string, input map[string]any) (any, error) {
	canonical, err := canonicalRuntimeToolName(toolName)
	if err != nil || canonical != toolcontract.AskUser {
		return nil, errors.New("ask_user tool name is invalid")
	}
	toolName = canonical
	input, err = normalizeAskUserToolInput(input)
	if err != nil {
		return nil, err
	}
	if err := s.validateRegisteredTool(toolName, input); err != nil {
		return nil, err
	}
	questions, err := askUserQuestionValue(input)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"questions": questions,
		"answers":   map[string]any{},
	}
	return result, nil
}

func (s *Server) executeTeamTool(toolName string, input map[string]any) (any, error) {
	if err := s.validateRegisteredTool(toolName, input); err != nil {
		return nil, err
	}
	if s.runtimeStore == nil {
		return nil, errors.New("team runtime store is not configured")
	}
	switch toolName {
	case "TeamCreate":
		if existing, ok, err := s.runtimeStore.Get(teamRuntimeNamespace, "current"); err != nil {
			return nil, err
		} else if ok {
			teamName := stringValue(mapValue(existing.Value)["team_name"])
			if teamName != "" {
				return nil, fmt.Errorf("Already leading team %q. A leader can only manage one team at a time. Use TeamDelete to end the current team before creating a new one.", teamName)
			}
		}
		teamName := sanitizeTeamName(stringValue(input["team_name"]))
		if teamName == "" {
			return nil, errors.New("team_name is required for TeamCreate")
		}
		teamName = s.uniqueTeamName(teamName)
		description := stringValue(input["description"])
		agentType := firstNonEmpty(stringValue(input["agent_type"]), "team-lead")
		leadAgentID := "team-lead-" + teamName
		teamFilePath := filepath.ToSlash(filepath.Join(".synon", "teams", teamName+".json"))
		now := time.Now().UTC().Format(time.RFC3339Nano)
		teamFile := map[string]any{
			"name":          teamName,
			"description":   description,
			"createdAt":     now,
			"leadAgentId":   leadAgentID,
			"leadSessionId": "",
			"members": []any{
				map[string]any{
					"agentId":       leadAgentID,
					"name":          "team-lead",
					"agentType":     agentType,
					"model":         "",
					"joinedAt":      now,
					"tmuxPaneId":    "",
					"cwd":           s.fileRoot,
					"subscriptions": []any{},
				},
			},
		}
		if err := writeTeamFile(s.fileRoot, teamFilePath, teamFile); err != nil {
			return nil, err
		}
		record := map[string]any{
			"team_name":      teamName,
			"description":    description,
			"agent_type":     agentType,
			"team_file_path": teamFilePath,
			"lead_agent_id":  leadAgentID,
			"createdAt":      now,
		}
		if _, err := s.runtimeStore.Set(teamRuntimeNamespace, "current", record); err != nil {
			return nil, err
		}
		if _, err := s.runtimeStore.Set(teamRuntimeNamespace, teamName, record); err != nil {
			return nil, err
		}
		return map[string]any{"data": map[string]any{"team_name": teamName, "team_file_path": teamFilePath, "lead_agent_id": leadAgentID}, "team": record}, nil
	case "TeamDelete":
		entry, ok, err := s.runtimeStore.Get(teamRuntimeNamespace, "current")
		if err != nil {
			return nil, err
		}
		if !ok {
			return map[string]any{"data": map[string]any{"success": true, "message": "No team name found, nothing to clean up", "team_name": nil}}, nil
		}
		record := mapValue(entry.Value)
		teamName := stringValue(record["team_name"])
		teamFilePath := stringValue(record["team_file_path"])
		if teamFilePath != "" {
			if err := removeTeamFile(s.fileRoot, teamFilePath); err != nil {
				return nil, err
			}
		}
		_, _ = s.runtimeStore.Delete(teamRuntimeNamespace, "current")
		if teamName != "" {
			_, _ = s.runtimeStore.Delete(teamRuntimeNamespace, teamName)
		}
		return map[string]any{"data": map[string]any{"success": true, "message": fmt.Sprintf("Cleaned up directories and worktrees for team %q", teamName), "team_name": teamName}}, nil
	default:
		return nil, fmt.Errorf("unknown team tool: %s", toolName)
	}
}

func (s *Server) uniqueTeamName(name string) string {
	candidate := name
	for i := 2; ; i++ {
		path := filepath.Join(s.fileRoot, ".synon", "teams", candidate+".json")
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", name, i)
	}
}

func sanitizeTeamName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	var builder strings.Builder
	lastDash := false
	for _, r := range name {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if valid {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

func writeTeamFile(root string, relativePath string, value map[string]any) error {
	target := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := ensurePathWithinRoot(root, target, "team file"); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(target, append(raw, 10), 0o600)
}

func removeTeamFile(root string, relativePath string) error {
	target := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := ensurePathWithinRoot(root, target, "team file"); err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
