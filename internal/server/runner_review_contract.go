package server

import (
	"fmt"
	"path/filepath"
	"strings"

	"synon-go/internal/agentruntime"
	"synon-go/internal/assets"
)

type sessionRunnerReviewExecutionSpec struct {
	ReviewKind     string
	ProfileName    string
	ReviewTrigger  string
	SubmissionMode sessionReviewerSubmissionMode
	SystemPrompt   string
}

func (s *Server) sessionCompletionReviewExecutionSpec() (sessionRunnerReviewExecutionSpec, error) {
	return s.sessionFixedJobExecutionSpec("REVIEWER", "reviewer", "runner_completion_review")
}

func (s *Server) sessionCompletionBookmarkerExecutionSpec() (sessionRunnerReviewExecutionSpec, error) {
	return s.sessionFixedJobExecutionSpec("BOOKMARKER", "bookmarker", "runner_completion_bookmark")
}

// sessionFixedJobExecutionSpec loads one fixed-job authority from the bundled
// Synon catalog. REVIEWER and BOOKMARKER share the same completion checkpoint
// orchestrator but retain separate prompts, hidden frames, and output schemas.
func (s *Server) sessionFixedJobExecutionSpec(profileName, directory, reviewKind string) (sessionRunnerReviewExecutionSpec, error) {
	if s == nil {
		return sessionRunnerReviewExecutionSpec{}, fmt.Errorf("bundled %s catalog is unavailable", profileName)
	}
	catalog := s.agentCatalog
	var profile agentruntime.AgentDefinition
	found := false
	if catalog != nil && s.agentCatalogError == nil {
		profile, found = catalog.Agent(profileName)
	} else {
		for _, candidate := range defaultAgentCatalogCandidates() {
			manifest, err := assets.Load(candidate.manifest)
			if err != nil {
				continue
			}
			if _, err := assets.Verify(candidate.root, manifest); err != nil {
				continue
			}
			profile, err = agentruntime.LoadAgentDefinitionFile(filepath.Join(candidate.root, directory, "metadata.yaml"))
			if err == nil {
				found = true
				break
			}
		}
	}
	if !found {
		return sessionRunnerReviewExecutionSpec{}, fmt.Errorf("bundled %s catalog is unavailable", profileName)
	}
	if !profile.Enabled || !profile.Healthy || profile.Name != profileName {
		return sessionRunnerReviewExecutionSpec{}, fmt.Errorf("bundled %s profile is unavailable or unhealthy", profileName)
	}
	prompt := strings.TrimSpace(profile.EffectiveSystemPrompt())
	if prompt == "" {
		return sessionRunnerReviewExecutionSpec{}, fmt.Errorf("bundled %s profile has no system prompt", profileName)
	}
	return sessionRunnerReviewExecutionSpec{
		ReviewKind:     reviewKind,
		ProfileName:    profileName,
		ReviewTrigger:  "auto",
		SubmissionMode: sessionReviewerSubmissionCompletion,
		SystemPrompt:   prompt,
	}, nil
}
