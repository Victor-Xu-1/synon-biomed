package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"synon-go/internal/agentruntime"
	sessionstore "synon-go/internal/persistence/sessions"
	"synon-go/internal/skills"
)

type runnerPromptSkillIdentity struct {
	Name     string `json:"name"`
	BodyHash string `json:"bodyHash"`
}

func (s *Server) persistSessionRunnerPromptSnapshot(
	ctx context.Context,
	session sessionstore.Session,
	options SessionRunnerChatOptions,
	advertised []agentruntime.ToolSchema,
	selectedSkills []skills.Skill,
	run *sessionRunnerChatRun,
	researchContext map[string]any,
) error {
	if s == nil || s.workspaceStore == nil || strings.TrimSpace(session.ID) == "" {
		return nil
	}
	frame, found, err := s.workspaceStore.GetFrame(session.ID)
	if err != nil || !found {
		return err
	}
	toolJSON, err := json.Marshal(advertised)
	if err != nil {
		return err
	}
	toolDigest := sha256.Sum256(toolJSON)
	systemDigest := sha256.Sum256([]byte(options.SystemPrompt))
	skillIdentities := make([]runnerPromptSkillIdentity, 0, len(selectedSkills))
	for _, skill := range selectedSkills {
		skillIdentities = append(skillIdentities, runnerPromptSkillIdentity{
			Name: strings.TrimSpace(skill.Name), BodyHash: strings.TrimSpace(skill.BodyHash),
		})
	}
	sort.Slice(skillIdentities, func(i, j int) bool { return skillIdentities[i].Name < skillIdentities[j].Name })
	payload := map[string]any{
		"schema":                  "synon.frame_system_prompt.v1",
		"agentName":               frame.AgentName,
		"runnerAttempt":           sessionRunnerAttempt(run),
		"taskIntentId":            sessionRunnerTaskIntentID(run),
		"taskIntentRevision":      sessionRunnerTaskIntentRevision(run),
		"lifecyclePhase":          sessionRunnerLifecyclePhase(run),
		"systemPrompt":            options.SystemPrompt,
		"systemPromptSha256":      hex.EncodeToString(systemDigest[:]),
		"toolSchemaSha256":        hex.EncodeToString(toolDigest[:]),
		"advertisedToolSchemas":   advertised,
		"selectedSkillIdentities": skillIdentities,
		"capabilityState":         run.capabilitySnapshot(),
	}
	if manifest := sessionRunnerResearchContextManifest(researchContext); len(manifest) > 0 {
		payload["researchContextManifest"] = manifest
	}
	_, err = s.workspaceStore.PutFrameSystemPromptSnapshot(ctx, session.ID, payload)
	return err
}

func sessionRunnerTaskIntentID(run *sessionRunnerChatRun) string {
	if run == nil {
		return ""
	}
	return strings.TrimSpace(run.TaskIntentID)
}

func sessionRunnerTaskIntentRevision(run *sessionRunnerChatRun) int64 {
	if run == nil {
		return 0
	}
	return run.TaskIntentRevision
}
