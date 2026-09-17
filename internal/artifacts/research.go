package artifacts

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type ResearchAudit struct {
	Files         []string       `json:"files"`
	Phases        ResearchPhases `json:"phases"`
	FinalMarkdown []string       `json:"finalMarkdown"`
	FinalHTML     []string       `json:"finalHtml"`
	Signature     string         `json:"signature"`
}

type ResearchPhases struct {
	Phase1               bool `json:"phase1"`
	Phase2Any            bool `json:"phase2Any"`
	Phase2Verification   bool `json:"phase2Verification"`
	Phase3Synthesis      bool `json:"phase3Synthesis"`
	Phase4Report         bool `json:"phase4Report"`
	Phase45EvidenceAudit bool `json:"phase45EvidenceAudit"`
	Phase5Review         bool `json:"phase5Review"`
	FinalMarkdown        bool `json:"finalMarkdown"`
	FinalHTML            bool `json:"finalHtml"`
}

var (
	rePhaseSynthesis    = regexp.MustCompile(`(?i)(^|/)phase\d+_synthesis/`)
	reSynthesisName     = regexp.MustCompile(`(?i)synthesis`)
	reFinalMarkdownName = regexp.MustCompile(`(?i)(final|report|executive|analysis|深度|报告)`)
	reEarlyPhase        = regexp.MustCompile(`(?i)(^|/)phase[123]_`)
	rePhase4Report      = regexp.MustCompile(`(?i)(^|/)phase4_(report|compiler)/`)
	rePhase8CompilerMD  = regexp.MustCompile(`(?i)(^|/)phase8_html_compiler/.+\.md$`)
	reEvidenceAudit     = regexp.MustCompile(`(?i)(^|/)phase4_5_evidence_audit/|(^|/)phase\d+_evidence_audit/`)
)

func ClassifyResearchArtifacts(files []string) ResearchAudit {
	rel := normalizeResearchArtifactFiles(files)
	synthesisFiles := make([]string, 0)
	finalMarkdown := make([]string, 0)
	finalHTML := make([]string, 0)
	phase4Report := false
	phase45EvidenceAudit := false
	phase1 := false
	phase2Any := false
	phase2Verification := false
	phase5Review := false
	for _, file := range rel {
		lower := strings.ToLower(file)
		if strings.Contains(file, "phase1_") {
			phase1 = true
		}
		if strings.Contains(file, "phase2_") {
			phase2Any = true
		}
		if strings.Contains(file, "phase2_verification") {
			phase2Verification = true
		}
		if strings.Contains(file, "phase5_") {
			phase5Review = true
		}
		if strings.HasSuffix(lower, ".md") && rePhaseSynthesis.MatchString(file) && reSynthesisName.MatchString(file) {
			synthesisFiles = append(synthesisFiles, file)
		}
		if strings.HasSuffix(lower, ".md") && reFinalMarkdownName.MatchString(file) && !reEarlyPhase.MatchString(file) && !rePhaseSynthesis.MatchString(file) {
			finalMarkdown = append(finalMarkdown, file)
		}
		if strings.HasSuffix(lower, ".html") || strings.HasSuffix(lower, ".htm") {
			finalHTML = append(finalHTML, file)
		}
		if rePhase4Report.MatchString(file) || rePhase8CompilerMD.MatchString(file) {
			phase4Report = true
		}
		if reEvidenceAudit.MatchString(file) {
			phase45EvidenceAudit = true
		}
	}
	return ResearchAudit{
		Files:         rel,
		FinalMarkdown: finalMarkdown,
		FinalHTML:     finalHTML,
		Signature:     strings.Join(rel, "\n"),
		Phases: ResearchPhases{
			Phase1:               phase1,
			Phase2Any:            phase2Any,
			Phase2Verification:   phase2Verification,
			Phase3Synthesis:      len(synthesisFiles) > 0,
			Phase4Report:         phase4Report,
			Phase45EvidenceAudit: phase45EvidenceAudit,
			Phase5Review:         phase5Review,
			FinalMarkdown:        len(finalMarkdown) > 0,
			FinalHTML:            len(finalHTML) > 0,
		},
	}
}

func normalizeResearchArtifactFiles(files []string) []string {
	seen := map[string]struct{}{}
	rel := make([]string, 0, len(files))
	for _, file := range files {
		clean := strings.TrimSpace(filepath.ToSlash(file))
		clean = strings.TrimPrefix(clean, "./")
		if clean == "" {
			continue
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		rel = append(rel, clean)
	}
	sort.Strings(rel)
	return rel
}
