package server

import (
	"fmt"
	"sort"
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

// registeredExecutionDownloadCatalogSource recognizes an exact immutable
// download declared by any local execution pack. This is deliberately
// separate from selected-run authority: a fresh run may need to acquire an
// execution asset before the model has restored its skill selection, while
// the catalog still owns the URL, filename, checksum, size, and redirect
// policy. Exact URL + filename + checksum matching keeps this route bounded to
// trusted product capability data rather than user/model-provided URLs.
func (s *Server) registeredExecutionDownloadCatalogSource(
	request agentPublicScientificFileRequest,
) (sciencecapability.ExecutionDownload, string, bool) {
	if s == nil || s.scienceCapabilities == nil ||
		strings.TrimSpace(request.SourceURL) == "" ||
		strings.TrimSpace(request.Filename) == "" ||
		strings.TrimSpace(request.ExpectedSHA256) == "" {
		return sciencecapability.ExecutionDownload{}, "", false
	}
	type authority struct {
		packID    string
		inputKind string
		download  sciencecapability.ExecutionDownload
	}
	matches := make([]authority, 0, 1)
	for _, capability := range s.scienceCapabilities.Capabilities {
		for _, engine := range capability.AcceptedEngines {
			if engine.ExecutionPack.Mode != "local" {
				continue
			}
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
	if len(matches) == 0 {
		return sciencecapability.ExecutionDownload{}, "", false
	}
	sort.SliceStable(matches, func(left, right int) bool {
		if matches[left].packID != matches[right].packID {
			return matches[left].packID < matches[right].packID
		}
		return matches[left].inputKind < matches[right].inputKind
	})
	match := matches[0]
	return match.download,
		fmt.Sprintf("execution-catalog:%s:%s", match.packID, match.inputKind), true
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

// selectedExecutionDownloadHints exposes only immutable downloads belonging to
// the currently selected implementation or evidence resolver. Recovery code
// uses these hints to repair a model that routes a binary asset through the
// page-fetch tool; the catalog remains the authority for URL, filename, and
// checksum.
func (s *Server) selectedExecutionDownloadHints(run *sessionRunnerChatRun) []sciencecapability.ExecutionDownload {
	if s == nil || s.skillCatalog == nil || s.scienceCapabilities == nil || run == nil {
		return nil
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
	seen := map[string]bool{}
	hints := make([]sciencecapability.ExecutionDownload, 0, 2)
	for _, skillName := range uniqueSortedFolded(skillNames) {
		for _, engine := range s.scienceCapabilities.LocalExecutionPacksForSkill(skillName) {
			for _, download := range engine.ExecutionPack.Downloads {
				key := download.URL + "\x00" + download.Filename + "\x00" + download.SHA256
				if download.URL == "" || seen[key] {
					continue
				}
				seen[key] = true
				hints = append(hints, download)
			}
		}
	}
	return hints
}

// registeredExecutionDownloadHints returns the immutable binary sources from
// every local execution pack. It is intentionally broader than the selected
// implementation view: routing an exact catalog binary through web_fetch is
// invalid even while a resumed run is restoring its selection state. This
// helper does not authorize a download; the download tool still performs its
// source and task-authority checks.
func (s *Server) registeredExecutionDownloadHints() []sciencecapability.ExecutionDownload {
	if s == nil || s.scienceCapabilities == nil {
		return nil
	}
	seen := map[string]bool{}
	hints := make([]sciencecapability.ExecutionDownload, 0, 4)
	for _, capability := range s.scienceCapabilities.Capabilities {
		for _, engine := range capability.AcceptedEngines {
			if engine.ExecutionPack.Mode != "local" {
				continue
			}
			for _, download := range engine.ExecutionPack.Downloads {
				download.URL = strings.TrimSpace(download.URL)
				if download.URL == "" {
					continue
				}
				key := download.URL + "\\x00" + download.Filename + "\\x00" + strings.ToLower(download.SHA256)
				if seen[key] {
					continue
				}
				seen[key] = true
				hints = append(hints, download)
			}
		}
	}
	sort.Slice(hints, func(left, right int) bool {
		if hints[left].URL != hints[right].URL {
			return hints[left].URL < hints[right].URL
		}
		if hints[left].Filename != hints[right].Filename {
			return hints[left].Filename < hints[right].Filename
		}
		return strings.ToLower(hints[left].SHA256) < strings.ToLower(hints[right].SHA256)
	})
	return hints
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
