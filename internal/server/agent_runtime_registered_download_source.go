package server

import (
	"fmt"
	"strings"

	"synon-go/internal/sciencecapability"
)

// registeredExecutionDownloadSource authorizes only an exact immutable source
// declared by a currently selected primary implementation or auxiliary
// evidence resolver. It does not trust Skill prose, task text, filenames, or
// arbitrary loaded Skills.
func (s *Server) registeredExecutionDownloadSource(
	run *sessionRunnerChatRun,
	request agentPublicScientificFileRequest,
) (string, bool) {
	_, source, found := s.registeredExecutionDownload(run, request)
	return source, found
}

func (s *Server) registeredExecutionDownload(
	run *sessionRunnerChatRun,
	request agentPublicScientificFileRequest,
) (sciencecapability.ExecutionDownload, string, bool) {
	if s == nil || s.skillCatalog == nil || s.scienceCapabilities == nil || run == nil {
		return sciencecapability.ExecutionDownload{}, "", false
	}
	skillNames := make([]string, 0, 2)
	for _, implementation := range run.selectedImplementationsSnapshot() {
		if skill, found := dedicatedSkillForImplementation(s.skillCatalog, implementation); found {
			skillNames = append(skillNames, skill.Name)
		}
	}
	for _, resolver := range run.selectedEvidenceResolversSnapshot() {
		skillNames = append(skillNames, resolver.Skill)
	}
	type authority struct {
		packID    string
		inputKind string
		download  sciencecapability.ExecutionDownload
	}
	matches := make([]authority, 0, 1)
	for _, skillName := range uniqueSortedFolded(skillNames) {
		for _, engine := range s.scienceCapabilities.LocalExecutionPacksForSkill(skillName) {
			for _, download := range engine.ExecutionPack.Downloads {
				if download.URL != request.SourceURL || download.Filename != request.Filename ||
					!strings.EqualFold(download.SHA256, request.ExpectedSHA256) {
					continue
				}
				matches = append(matches, authority{
					packID: engine.ExecutionPack.ID, inputKind: download.InputKind, download: download,
				})
			}
		}
	}
	if len(matches) != 1 {
		return sciencecapability.ExecutionDownload{}, "", false
	}
	return matches[0].download,
		fmt.Sprintf("execution-pack:%s:%s", matches[0].packID, matches[0].inputKind), true
}

func (s *Server) selectedEvidenceResolverDownloads(
	run *sessionRunnerChatRun,
) []map[string]any {
	if s == nil || s.scienceCapabilities == nil || run == nil {
		return nil
	}
	result := make([]map[string]any, 0, 1)
	seen := map[string]bool{}
	for _, resolver := range run.selectedEvidenceResolversSnapshot() {
		for _, engine := range s.scienceCapabilities.LocalExecutionPacksForSkill(resolver.Skill) {
			for _, download := range engine.ExecutionPack.Downloads {
				key := engine.ExecutionPack.ID + "\x00" + download.InputKind + "\x00" + download.URL
				if seen[key] {
					continue
				}
				seen[key] = true
				result = append(result, map[string]any{
					"execution_pack_id": engine.ExecutionPack.ID,
					"input_kind":        download.InputKind, "url": download.URL,
					"filename": download.Filename, "expected_sha256": download.SHA256,
					"size_bytes": download.SizeBytes,
				})
			}
		}
	}
	return result
}
