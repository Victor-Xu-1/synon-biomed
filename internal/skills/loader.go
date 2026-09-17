package skills

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"gopkg.in/yaml.v3"

	"synon-go/internal/buildinfo"
	"synon-go/internal/discoveryquery"
)

type Skill struct {
	Name                        string            `json:"name"`
	Description                 string            `json:"description"`
	DescriptionI18n             map[string]string `json:"description_i18n,omitempty"`
	Category                    string            `json:"category,omitempty"`
	Tags                        []string          `json:"tags"`
	Keywords                    []string          `json:"keywords"`
	Tools                       []string          `json:"tools"`
	Arguments                   []string          `json:"arguments"`
	RequiredCapabilities        []string          `json:"required_capabilities,omitempty"`
	ImplementationIdentities    []string          `json:"implementation_identities,omitempty"`
	RequiredSkills              []string          `json:"required_skills,omitempty"`
	CriticalConstraints         []string          `json:"critical_constraints,omitempty"`
	SetupEvidenceURLs           []string          `json:"setup_evidence_urls,omitempty"`
	RequiredEnvironmentPackages []string          `json:"required_environment_packages,omitempty"`
	PreferredExecutionAssets    []string          `json:"preferred_execution_assets,omitempty"`
	Path                        string            `json:"path"`
	References                  []string          `json:"references"`
	Body                        string            `json:"body"`
	BodyHash                    string            `json:"body_hash"`
}

// SearchMatch preserves the deterministic lexical relevance used by the
// catalog. Callers may use the score to decide how much discovery context to
// provide, but it is never an execution or authorization decision.
type SearchMatch struct {
	Skill Skill
	Score int
}

type LoadError struct {
	Path string `json:"path"`
	Err  string `json:"error"`
}

type Catalog struct {
	mu         sync.RWMutex
	skills     []Skill
	loadErrors []LoadError
}

func NewCatalog() *Catalog {
	return &Catalog{
		skills:     []Skill{},
		loadErrors: []LoadError{},
	}
}

func (c *Catalog) AddSkill(skill Skill) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.skills = append(c.skills, skill)
}

func (c *Catalog) UpsertSkill(skill Skill) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for index := range c.skills {
		if strings.EqualFold(c.skills[index].Name, skill.Name) {
			c.skills[index] = skill
			c.sortSkillsLocked()
			return
		}
	}
	c.skills = append(c.skills, skill)
	c.sortSkillsLocked()
}

func (c *Catalog) RemoveSkill(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for index := range c.skills {
		if !strings.EqualFold(c.skills[index].Name, name) {
			continue
		}
		c.skills = append(c.skills[:index], c.skills[index+1:]...)
		return true
	}
	return false
}

func (c *Catalog) RemoveSkillsUnder(root string) []string {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	removed := make([]string, 0)
	kept := c.skills[:0]
	for _, skill := range c.skills {
		if strings.HasPrefix(skill.Path, "builtin:") {
			kept = append(kept, skill)
			continue
		}
		pathAbs, pathErr := filepath.Abs(skill.Path)
		relative, relErr := filepath.Rel(rootAbs, pathAbs)
		within := pathErr == nil && relErr == nil &&
			(relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)))
		if within {
			removed = append(removed, skill.Name)
			continue
		}
		kept = append(kept, skill)
	}
	c.skills = kept
	sort.Strings(removed)
	return removed
}

func (c *Catalog) AddLoadError(path string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadErrors = append(c.loadErrors, LoadError{Path: path, Err: err.Error()})
}

func (c *Catalog) Skills() []Skill {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Skill, len(c.skills))
	copy(out, c.skills)
	return out
}

func (c *Catalog) LoadErrors() []LoadError {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]LoadError, len(c.loadErrors))
	copy(out, c.loadErrors)
	return out
}

type LoadOptions struct {
	MaxBodyBytes       int
	RejectOversizeBody bool
}

func BuiltinRuntimeCatalog() *Catalog {
	catalog := NewCatalog()
	productName := buildinfo.Release().Name
	body := strings.TrimSpace(fmt.Sprintf(`Use this skill when work depends on the %s runtime contract.

Standard tools: search_skills, skill, repl, save_artifacts

Keep session work durable: write user, assistant, tool, checkpoint, compact, artifact, and runner events through the runtime journal. Route tool calls through the registered tool gateway so schema validation, permission checks, hooks, audit records, and post-tool processing remain consistent.

When a task requires delegation, use host.delegate from the persistent repl kernel so child work runs under the single host supervision authority with explicit runtime provenance. Use host.collect for asynchronous results and host.send_message for related follow-up work. Preserve the parent boundary, surface progress through checkpoints, and leave auditable artifact records, output paths, or blockers.

For capability discovery, call search_skills and then load the selected workflow with skill. Connected MCP methods are advertised directly and share one connector pool with host.mcp inside repl. Use only exact names from the current snapshot and preserve any unavailable capability as a diagnostic blocker.`, productName))
	catalog.AddSkill(Skill{
		Name:        "synon-runtime",
		Description: "Built-in " + productName + " runtime operating guidance for sessions, tools, permissions, supervised delegation, artifacts, and connectors.",
		DescriptionI18n: map[string]string{
			"zh-CN": "了解 " + productName + " 的运行时能力、工具边界和本地工作方式。",
		},
		Category:   "compute-platform",
		Tags:       []string{"runtime", "synon", "agent"},
		Keywords:   []string{"session", "tool", "permission", "hook", "delegation", "artifact", "mcp", "connector"},
		Tools:      []string{"search_skills", "skill", "repl", "save_artifacts"},
		References: []string{"builtin:synon-runtime/runtime-contract"},
		Path:       "builtin:synon-runtime",
		Body:       body,
		BodyHash:   sha1Hex(body),
	})
	return catalog
}

func Load(directories []string, options ...LoadOptions) *Catalog {
	opts := LoadOptions{}
	if len(options) > 0 {
		opts = options[0]
	}
	catalog := NewCatalog()
	for _, dir := range uniqueStrings(directories) {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		absDir := dir
		if strings.HasPrefix(dir, `\\`) || strings.HasPrefix(dir, "//") || (len(dir) >= 2 && dir[1] == ':') {
			absDir = dir
		} else if !filepath.IsAbs(dir) {
			cwd, _ := os.Getwd()
			absDir = filepath.Join(cwd, dir)
		}
		info, err := os.Stat(absDir)
		if err != nil {
			catalog.AddLoadError(absDir, err)
			continue
		}
		if !info.IsDir() {
			catalog.AddLoadError(absDir, fmt.Errorf("not a directory"))
			continue
		}
		root, err := os.OpenRoot(absDir)
		if err != nil {
			catalog.AddLoadError(absDir, err)
			continue
		}
		presentation := newPresentationLoader(root, absDir, catalog)
		err = filepath.WalkDir(absDir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				catalog.AddLoadError(path, err)
				return nil
			}
			if d.IsDir() {
				return nil
			}
			if d.Name() != "SKILL.md" {
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				catalog.AddLoadError(path, fmt.Errorf("skill manifest must be a regular file, not a symbolic link"))
				return nil
			}
			relative, relErr := filepath.Rel(absDir, path)
			if relErr != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				catalog.AddLoadError(path, fmt.Errorf("skill manifest is outside the configured root"))
				return nil
			}
			data, readErr := root.ReadFile(relative)
			if readErr != nil {
				catalog.AddLoadError(path, readErr)
				return nil
			}
			skill, parseErr := parseSkillFile(path, data)
			if parseErr != nil {
				catalog.AddLoadError(path, parseErr)
				return nil
			}
			presentation.apply(&skill, relative)
			limitBody := skill.Body
			if opts.MaxBodyBytes > 0 && len(limitBody) > opts.MaxBodyBytes {
				if opts.RejectOversizeBody {
					catalog.AddLoadError(path, fmt.Errorf(
						"skill body exceeds %d bytes", opts.MaxBodyBytes,
					))
					return nil
				}
				limitBody = truncateString(limitBody, opts.MaxBodyBytes)
			}
			skill.Body = limitBody
			skill.BodyHash = sha1Hex(limitBody)
			catalog.AddSkill(skill)
			return nil
		})
		closeErr := root.Close()
		if err != nil {
			catalog.AddLoadError(absDir, err)
		}
		if closeErr != nil {
			catalog.AddLoadError(absDir, closeErr)
		}
		presentation.finish()
	}
	catalog.mu.Lock()
	catalog.sortSkillsLocked()
	catalog.mu.Unlock()
	return catalog
}

func (c *Catalog) Search(query string, maxResults int) []Skill {
	c.mu.RLock()
	defer c.mu.RUnlock()
	max := maxResults
	if max <= 0 {
		max = 5
	}
	ranked := c.searchRanked(query, max, true)
	matches := make([]Skill, 0, len(ranked))
	for _, match := range ranked {
		matches = append(matches, match.Skill)
	}
	return matches
}

// SearchRanked returns the same ordering as Search together with the catalog's
// relevance score. Exposing this prevents Harness callers from maintaining a
// second, drifting search implementation solely to distinguish an obvious
// match from a weak lexical candidate.
func (c *Catalog) SearchRanked(query string, maxResults int) []SearchMatch {
	c.mu.RLock()
	defer c.mu.RUnlock()
	max := maxResults
	if max <= 0 {
		max = 5
	}
	return c.searchRanked(query, max, true)
}

// SearchActivationRanked uses only declarative routing metadata: the Skill
// name, description, tags, and keywords. A Skill description is the standard
// catalog contract for when that Skill applies, so Skills do not need a second
// proprietary keyword list before the Harness can route to them. Instruction
// bodies remain excluded: executable prose is never activation authority.
func (c *Catalog) SearchActivationRanked(query string, maxResults int) []SearchMatch {
	c.mu.RLock()
	defer c.mu.RUnlock()
	max := maxResults
	if max <= 0 {
		max = 5
	}
	return c.searchActivationRanked(query, max)
}

func (c *Catalog) searchActivationRanked(query string, maxResults int) []SearchMatch {
	lowerQuery := strings.ToLower(strings.TrimSpace(query))
	if lowerQuery == "" {
		return nil
	}
	queryTerms := splitQueryTerms(query)
	metadataTerms := make([]map[string]struct{}, len(c.skills))
	documentFrequency := make(map[string]int)
	for index, skill := range c.skills {
		terms := make(map[string]struct{})
		metadata := strings.Join(append(
			[]string{skill.Name, skill.Description},
			append(append([]string(nil), skill.Tags...), skill.Keywords...)...,
		), " ")
		for _, term := range splitQueryTerms(metadata) {
			terms[term] = struct{}{}
		}
		metadataTerms[index] = terms
		for term := range terms {
			documentFrequency[term]++
		}
	}
	type scored struct {
		skill Skill
		score int
	}
	values := make([]scored, 0, len(c.skills))
	for index, skill := range c.skills {
		score := 0
		name := strings.ToLower(strings.TrimSpace(skill.Name))
		if name == lowerQuery {
			score += 100
		} else if discoveryMetadataPhraseContained(lowerQuery, name) {
			score += 24
		}
		for _, phrase := range append(append([]string(nil), skill.Tags...), skill.Keywords...) {
			if discoveryMetadataPhraseContained(lowerQuery, phrase) {
				score += 24
			}
		}
		// Description matching is corpus-discriminative rather than based on a
		// maintained domain word list. Terms that occur in fewer Skill metadata
		// records contribute more; catalog-wide boilerplate contributes nothing.
		// This keeps routing generic across newly installed Skills and languages
		// supported by discoveryquery.Terms.
		for _, term := range queryTerms {
			if _, matched := metadataTerms[index][term]; !matched {
				continue
			}
			frequency := documentFrequency[term]
			if frequency <= 0 || frequency == len(c.skills) {
				continue
			}
			weight := (len(c.skills) * 8) / (frequency + 1)
			if weight < 1 {
				weight = 1
			} else if weight > 24 {
				weight = 24
			}
			score += weight
		}
		if score > 0 {
			values = append(values, scored{skill: skill, score: score})
		}
	}
	sort.SliceStable(values, func(i, j int) bool {
		if values[i].score != values[j].score {
			return values[i].score > values[j].score
		}
		return strings.ToLower(values[i].skill.Name) < strings.ToLower(values[j].skill.Name)
	})
	matches := make([]SearchMatch, 0, len(values))
	for _, value := range values {
		matches = append(matches, SearchMatch{Skill: value.skill, Score: value.score})
		if len(matches) == maxResults {
			break
		}
	}
	return matches
}

func (c *Catalog) sortSkillsLocked() {
	sort.SliceStable(c.skills, func(i, j int) bool {
		return strings.ToLower(c.skills[i].Name) < strings.ToLower(c.skills[j].Name)
	})
}

func (c *Catalog) searchRanked(query string, maxResults int, includeBody bool) []SearchMatch {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	matches := []SearchMatch{}
	lowerQuery := strings.ToLower(query)
	if selected, ok := strings.CutPrefix(lowerQuery, "select:"); ok {
		requested := splitCSV(selected)
		skillByName := make(map[string]Skill, len(c.skills))
		for _, skill := range c.skills {
			skillByName[strings.ToLower(strings.TrimSpace(skill.Name))] = skill
		}
		for _, name := range requested {
			skill, ok := skillByName[strings.ToLower(name)]
			if ok {
				matches = append(matches, SearchMatch{Skill: skill, Score: 100})
			}
		}
		return truncateMatches(matches, maxResults)
	}

	terms := splitQueryTerms(query)
	type scored struct {
		skill Skill
		score int
	}
	scoredMatches := make([]scored, 0, len(c.skills))
	for _, skill := range c.skills {
		score := 0
		bodyScore := 0
		name := strings.ToLower(skill.Name)
		desc := strings.ToLower(skill.Description)
		body := strings.ToLower(skill.Body)
		if name == lowerQuery {
			score += 8
		} else if strings.Contains(name, lowerQuery) {
			score += 6
		}
		if desc == lowerQuery {
			score += 6
		} else if strings.Contains(desc, lowerQuery) {
			score += 4
		}
		// A complete intent phrase declared by Skill metadata is stronger than
		// several generic alias or body-token matches. Score it independently of
		// tokenization so multilingual phrases such as 首次人体/起始剂量 and
		// hyphenated English terms retain their routing specificity. Very short
		// metadata values remain token-scored to avoid promoting generic words.
		for _, tag := range skill.Tags {
			if discoveryMetadataPhraseContained(lowerQuery, tag) {
				score += 12
			}
		}
		for _, keyword := range skill.Keywords {
			if discoveryMetadataPhraseContained(lowerQuery, keyword) {
				score += 12
			}
		}
		for _, term := range terms {
			if term == "" {
				continue
			}
			if strings.Contains(name, term) {
				score += 8
			}
			if strings.Contains(desc, term) {
				score += 4
			}
			if includeBody && strings.Contains(body, term) {
				// Bodies can be thousands of words long. Without a cap, a
				// generic long Skill outranks a concise domain match merely by
				// containing more query fragments. Body text is supporting
				// recall; name, description, tags and keywords determine intent.
				bodyScore += 2
			}
			for _, tag := range skill.Tags {
				if strings.EqualFold(tag, term) {
					score += 8
					break
				}
			}
			for _, keyword := range skill.Keywords {
				if strings.EqualFold(keyword, term) {
					score += 6
					break
				}
			}
			for _, tool := range skill.Tools {
				if strings.EqualFold(tool, term) {
					score += 4
					break
				}
			}
		}
		if bodyScore > 8 {
			bodyScore = 8
		}
		score += bodyScore
		if score > 0 {
			scoredMatches = append(scoredMatches, scored{skill: skill, score: score})
		}
	}
	sort.SliceStable(scoredMatches, func(i, j int) bool {
		if scoredMatches[i].score != scoredMatches[j].score {
			return scoredMatches[i].score > scoredMatches[j].score
		}
		return strings.ToLower(scoredMatches[i].skill.Name) < strings.ToLower(scoredMatches[j].skill.Name)
	})
	for _, match := range scoredMatches {
		matches = append(matches, SearchMatch{Skill: match.skill, Score: match.score})
		if len(matches) >= maxResults {
			break
		}
	}
	return matches
}

func discoveryMetadataPhraseContained(query, rawPhrase string) bool {
	phrase := strings.ToLower(strings.TrimSpace(rawPhrase))
	if phrase == "" {
		return false
	}
	letters := 0
	for _, char := range phrase {
		if unicode.IsLetter(char) {
			letters++
		}
	}
	if letters < 3 {
		return false
	}
	if containsHanText(phrase) {
		return strings.Contains(compactDiscoveryText(query), compactDiscoveryText(phrase))
	}
	queryTokens := discoveryMetadataTokens(query)
	phraseTokens := discoveryMetadataTokens(phrase)
	if len(phraseTokens) == 0 || len(phraseTokens) > len(queryTokens) {
		return false
	}
	for offset := 0; offset+len(phraseTokens) <= len(queryTokens); offset++ {
		matched := true
		for index := range phraseTokens {
			if queryTokens[offset+index] != phraseTokens[index] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func discoveryMetadataTokens(value string) []string {
	return strings.FieldsFunc(strings.ToLower(value), func(char rune) bool {
		return !unicode.IsLetter(char) && !unicode.IsDigit(char)
	})
}

func containsHanText(value string) bool {
	for _, char := range value {
		if unicode.Is(unicode.Han, char) {
			return true
		}
	}
	return false
}

func compactDiscoveryText(value string) string {
	return strings.Map(func(char rune) rune {
		if unicode.IsSpace(char) {
			return -1
		}
		return char
	}, value)
}

func (c *Catalog) SearchNames(query string, maxResults int) []string {
	skills := c.Search(query, maxResults)
	out := make([]string, 0, len(skills))
	for _, skill := range skills {
		out = append(out, skill.Name)
	}
	return out
}

func truncateMatches[T any](values []T, max int) []T {
	if max <= 0 {
		return values
	}
	if len(values) <= max {
		return values
	}
	return values[:max]
}

func (c *Catalog) BuildSkillContext(matches []Skill, maxBytes int, root string) string {
	return buildSkillContext(matches, maxBytes, root)
}

func buildSkillContext(matches []Skill, maxBytes int, root string) string {
	if len(matches) == 0 {
		return ""
	}
	var lines []string
	lines = append(lines, "Runtime-selected SKILL.md contexts:")
	for _, skill := range matches {
		lines = append(lines, "### "+skill.Name)
		if strings.TrimSpace(skill.Path) != "" {
			lines = append(lines, "Source: "+filepath.Clean(skill.Path))
		}
		if skill.Description != "" {
			lines = append(lines, "Description: "+skill.Description)
		}
		if len(skill.Tags) > 0 {
			lines = append(lines, "Tags: "+strings.Join(uniqueStrings(skill.Tags), ", "))
		}
		if len(skill.Tools) > 0 {
			lines = append(lines, "Tools: "+strings.Join(uniqueStrings(skill.Tools), ", "))
		}
		if len(skill.References) > 0 {
			lines = append(lines, "References: "+strings.Join(uniqueStrings(skill.References), ", "))
		}
		lines = append(lines, "Instructions:")
		if strings.TrimSpace(skill.Body) != "" {
			lines = append(lines, strings.TrimSpace(skill.Body))
		} else {
			lines = append(lines, "(no body)")
		}
		if len(skill.References) > 0 {
			lines = append(lines, "Referenced files:")
			references := loadReferencedSkillFiles(skill, root, 2, 1200)
			for _, ref := range references {
				lines = append(lines, "## "+ref.path)
				if strings.TrimSpace(ref.content) != "" {
					lines = append(lines, strings.TrimSpace(ref.content))
				} else {
					lines = append(lines, "(empty)")
				}
			}
		}
		lines = append(lines, "")
	}
	context := strings.TrimSpace(strings.Join(lines, "\n"))
	if maxBytes <= 0 || len(context) <= maxBytes {
		return context
	}
	return truncateString(context, maxBytes)
}

func loadReferencedSkillFiles(skill Skill, root string, maxFiles, maxBytesPerFile int) []struct {
	path    string
	content string
} {
	baseDir := filepath.Dir(skill.Path)
	fallbackRoots := make([]string, 0, 3)
	if baseDir != string([]byte{}) {
		fallbackRoots = append(fallbackRoots, filepath.Clean(baseDir))
	}
	if strings.TrimSpace(root) != string([]byte{}) {
		fallbackRoots = append(fallbackRoots, filepath.Clean(root))
	}
	if maxFiles <= 0 {
		maxFiles = 2
	}
	if maxBytesPerFile <= 0 {
		maxBytesPerFile = 1200
	}
	references := []struct {
		path    string
		content string
	}{}
	normalizedRoot := filepath.Clean(strings.TrimSpace(root))
	for _, reference := range uniqueStrings(skill.References) {
		if len(references) >= maxFiles {
			break
		}
		if strings.TrimSpace(reference) == string([]byte{}) {
			continue
		}
		var selected string
		for _, fallback := range fallbackRoots {
			candidate, ok := resolveReferencedPath(reference, fallback, fallback == normalizedRoot)
			if !ok {
				continue
			}
			raw, err := os.ReadFile(candidate)
			if err != nil {
				continue
			}
			selected = candidate
			references = append(references, struct {
				path    string
				content string
			}{
				path:    candidate,
				content: truncateString(strings.TrimSpace(string(raw)), maxBytesPerFile),
			})
			break
		}
		if selected == string([]byte{}) {
			continue
		}
	}
	return references
}

func resolveReferencedPath(reference string, base string, stripParentFromRoot bool) (string, bool) {
	reference = strings.TrimSpace(reference)
	base = filepath.Clean(base)
	if reference == "" || strings.TrimSpace(base) == "" {
		return "", false
	}
	var candidate string
	if filepath.IsAbs(reference) {
		candidate = filepath.Clean(reference)
	} else {
		cleaned := filepath.Clean(reference)
		if stripParentFromRoot {
			separator := string(filepath.Separator)
			for strings.HasPrefix(cleaned, string([]byte{46, 46})+separator) {
				cleaned = strings.TrimPrefix(cleaned, string([]byte{46, 46})+separator)
				cleaned = strings.TrimPrefix(cleaned, separator)
			}
		}
		candidate = filepath.Clean(filepath.Join(base, cleaned))
	}
	if !referencedPathWithinRoot(candidate, base) {
		return "", false
	}
	return candidate, true
}

func referencedPathWithinRoot(candidate string, root string) bool {
	root = strings.TrimSpace(root)
	candidate = strings.TrimSpace(candidate)
	if root == "" || candidate == "" {
		return false
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	if evaluatedRoot, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootAbs = evaluatedRoot
	}
	if evaluatedCandidate, err := filepath.EvalSymlinks(candidateAbs); err == nil {
		candidateAbs = evaluatedCandidate
	}
	rel, err := filepath.Rel(rootAbs, candidateAbs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func parseSkillFile(path string, data []byte) (Skill, error) {
	skill := Skill{
		Path: filepath.Clean(path),
	}
	lines := strings.Split(string(data), "\n")
	metaLines, bodyLines, hasFrontMatter := splitFrontMatter(lines)
	if hasFrontMatter {
		if parsed, err := parseFrontMatter(metaLines); err != nil {
			return Skill{}, fmt.Errorf("parse frontmatter: %w", err)
		} else {
			skill.merge(parsed)
		}
		skill.Body = strings.TrimSpace(strings.Join(bodyLines, "\n"))
	} else {
		skill.Body = strings.TrimSpace(strings.Join(lines, "\n"))
	}
	if skill.Name == "" {
		skill.Name = filepath.Base(filepath.Dir(path))
	}
	if skill.Description == "" {
		skill.Description = "(no description)"
	}
	return skill, nil
}

func splitFrontMatter(lines []string) ([]string, []string, bool) {
	if len(lines) == 0 {
		return nil, nil, false
	}
	if strings.TrimSpace(lines[0]) != "---" {
		return nil, lines, false
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return lines[1:i], lines[i+1:], true
		}
	}
	return nil, nil, false
}

func parseFrontMatter(lines []string) (Skill, error) {
	var document yaml.Node
	raw := strings.Join(lines, "\n")
	if err := yaml.Unmarshal([]byte(raw), &document); err != nil {
		normalized, changed := normalizeLegacyPlainDescription(lines)
		if !changed {
			return Skill{}, err
		}
		document = yaml.Node{}
		if retryErr := yaml.Unmarshal([]byte(normalized), &document); retryErr != nil {
			return Skill{}, retryErr
		}
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return Skill{}, fmt.Errorf("frontmatter must be a YAML mapping")
	}
	skill := Skill{}
	root := document.Content[0]
	seenKeys := make(map[string]struct{}, len(root.Content)/2)
	for index := 0; index+1 < len(root.Content); index += 2 {
		key := strings.ToLower(strings.TrimSpace(root.Content[index].Value))
		if _, exists := seenKeys[key]; exists {
			return Skill{}, fmt.Errorf("frontmatter field %q is duplicated", key)
		}
		seenKeys[key] = struct{}{}
		value := root.Content[index+1]
		switch key {
		case "name":
			skill.Name = scalarFrontMatterValue(value)
		case "description":
			skill.Description = strings.TrimSpace(scalarFrontMatterValue(value))
		case "tags":
			skill.Tags = frontMatterStringList(value)
		case "keywords":
			skill.Keywords = frontMatterStringList(value)
		case "tools", "allowed-tools":
			skill.Tools = frontMatterStringList(value)
		case "arguments":
			skill.Arguments = validArgumentNames(frontMatterStringList(value))
		case "required-capabilities":
			values, err := strictFrontMatterStringSequence(value, "required-capabilities")
			if err != nil {
				return Skill{}, err
			}
			capabilities, err := validCapabilityNames(values)
			if err != nil {
				return Skill{}, err
			}
			skill.RequiredCapabilities = capabilities
		case "implementation-identities":
			values, err := strictFrontMatterStringSequence(value, "implementation-identities")
			if err != nil {
				return Skill{}, err
			}
			if len(values) > 16 {
				return Skill{}, errors.New("frontmatter field \"implementation-identities\" exceeds 16 entries")
			}
			for _, identity := range values {
				if len([]rune(identity)) > 160 || strings.ContainsAny(identity, "\x00\r\n") {
					return Skill{}, errors.New("frontmatter field \"implementation-identities\" contains an invalid identity")
				}
			}
			skill.ImplementationIdentities = uniqueStrings(values)
		case "metadata":
			requiredSkills, err := frontMatterMetadataRequiredSkills(value)
			if err != nil {
				return Skill{}, err
			}
			skill.RequiredSkills = append(skill.RequiredSkills, requiredSkills...)
		case "critical-constraints":
			values, err := strictFrontMatterStringSequence(value, "critical-constraints")
			if err != nil {
				return Skill{}, err
			}
			if len(values) > 16 {
				return Skill{}, errors.New("frontmatter field \"critical-constraints\" exceeds 16 entries")
			}
			for _, constraint := range values {
				if len([]rune(constraint)) > 512 || strings.ContainsAny(constraint, "\x00\r\n") {
					return Skill{}, errors.New("frontmatter field \"critical-constraints\" contains an invalid constraint")
				}
			}
			skill.CriticalConstraints = values
		case "setup-evidence-urls":
			values, err := strictFrontMatterStringSequence(value, "setup-evidence-urls")
			if err != nil {
				return Skill{}, err
			}
			if len(values) > 16 {
				return Skill{}, errors.New("frontmatter field \"setup-evidence-urls\" exceeds 16 entries")
			}
			for _, raw := range values {
				parsed, parseErr := url.ParseRequestURI(raw)
				if parseErr != nil || parsed == nil || !strings.EqualFold(parsed.Scheme, "https") ||
					strings.TrimSpace(parsed.Hostname()) == "" || parsed.User != nil || len([]rune(raw)) > 2048 {
					return Skill{}, errors.New("frontmatter field \"setup-evidence-urls\" contains an invalid public HTTPS URL")
				}
			}
			skill.SetupEvidenceURLs = uniqueStrings(values)
		case "required-environment-packages":
			values, err := strictFrontMatterStringSequence(value, "required-environment-packages")
			if err != nil {
				return Skill{}, err
			}
			if len(values) > 64 {
				return Skill{}, errors.New("frontmatter field \"required-environment-packages\" exceeds 64 entries")
			}
			for _, packageSpec := range values {
				if len([]rune(packageSpec)) > 128 || strings.ContainsAny(packageSpec, "\x00\r\n") {
					return Skill{}, errors.New("frontmatter field \"required-environment-packages\" contains an invalid package spec")
				}
			}
			skill.RequiredEnvironmentPackages = values
		case "preferred-execution-assets":
			values, err := strictFrontMatterStringSequence(value, "preferred-execution-assets")
			if err != nil {
				return Skill{}, err
			}
			if len(values) > 16 {
				return Skill{}, errors.New("frontmatter field \"preferred-execution-assets\" exceeds 16 entries")
			}
			for _, asset := range values {
				normalized := filepath.ToSlash(filepath.Clean(asset))
				if normalized != asset || filepath.IsAbs(asset) || asset == "." || asset == ".." ||
					strings.HasPrefix(asset, "../") || strings.ContainsAny(asset, "\x00\r\n") || len([]rune(asset)) > 256 {
					return Skill{}, errors.New("frontmatter field \"preferred-execution-assets\" contains an invalid relative path")
				}
			}
			skill.PreferredExecutionAssets = values
		case "references":
			skill.References = frontMatterStringList(value)
		default:
			normalized := strings.NewReplacer("-", "", "_", "").Replace(key)
			if normalized == "requiredcapabilities" {
				return Skill{}, fmt.Errorf("frontmatter field %q must be named required-capabilities", key)
			}
		}
	}
	if skill.Name == "" && skill.Description == "" && len(skill.Tags) == 0 && len(skill.Keywords) == 0 && len(skill.Tools) == 0 && len(skill.References) == 0 {
		return skill, fmt.Errorf("frontmatter appears empty")
	}
	return skill, nil
}

func strictFrontMatterStringSequence(node *yaml.Node, field string) ([]string, error) {
	if node == nil || node.Kind != yaml.SequenceNode || len(node.Content) == 0 {
		return nil, fmt.Errorf("frontmatter field %q must be a non-empty string sequence", field)
	}
	values := make([]string, 0, len(node.Content))
	for _, item := range node.Content {
		if item.Kind != yaml.ScalarNode || item.Tag != "!!str" || strings.TrimSpace(item.Value) == "" {
			return nil, fmt.Errorf("frontmatter field %q must be a non-empty string sequence", field)
		}
		values = append(values, strings.TrimSpace(item.Value))
	}
	return values, nil
}

func normalizeLegacyPlainDescription(lines []string) (string, bool) {
	normalized := append([]string(nil), lines...)
	changed := false
	for index, line := range normalized {
		if strings.TrimLeft(line, " \t") != line || !strings.HasPrefix(line, "description:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		if value == "" || value == ">" || value == "|" || strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") {
			continue
		}
		normalized[index] = "description: " + strconv.Quote(value)
		changed = true
	}
	return strings.Join(normalized, "\n"), changed
}

func scalarFrontMatterValue(node *yaml.Node) string {
	if node == nil || node.Kind != yaml.ScalarNode {
		return ""
	}
	return node.Value
}

func frontMatterStringList(node *yaml.Node) []string {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.ScalarNode {
		return splitCSV(node.Value)
	}
	if node.Kind != yaml.SequenceNode {
		return nil
	}
	values := make([]string, 0, len(node.Content))
	for _, item := range node.Content {
		if item.Kind != yaml.ScalarNode {
			continue
		}
		if value := strings.TrimSpace(item.Value); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func (target *Skill) merge(source Skill) {
	if source.Name != "" {
		target.Name = source.Name
	}
	if source.Description != "" {
		target.Description = source.Description
	}
	if len(source.Tags) > 0 {
		target.Tags = append(target.Tags, source.Tags...)
	}
	if len(source.Keywords) > 0 {
		target.Keywords = append(target.Keywords, source.Keywords...)
	}
	if len(source.Tools) > 0 {
		target.Tools = append(target.Tools, source.Tools...)
	}
	if len(source.Arguments) > 0 {
		target.Arguments = append(target.Arguments, source.Arguments...)
	}
	if len(source.RequiredCapabilities) > 0 {
		target.RequiredCapabilities = append(target.RequiredCapabilities, source.RequiredCapabilities...)
	}
	if len(source.ImplementationIdentities) > 0 {
		target.ImplementationIdentities = append(target.ImplementationIdentities, source.ImplementationIdentities...)
	}
	if len(source.RequiredSkills) > 0 {
		target.RequiredSkills = append(target.RequiredSkills, source.RequiredSkills...)
	}
	if len(source.CriticalConstraints) > 0 {
		target.CriticalConstraints = append(target.CriticalConstraints, source.CriticalConstraints...)
	}
	if len(source.SetupEvidenceURLs) > 0 {
		target.SetupEvidenceURLs = append(target.SetupEvidenceURLs, source.SetupEvidenceURLs...)
	}
	if len(source.RequiredEnvironmentPackages) > 0 {
		target.RequiredEnvironmentPackages = append(target.RequiredEnvironmentPackages, source.RequiredEnvironmentPackages...)
	}
	if len(source.PreferredExecutionAssets) > 0 {
		target.PreferredExecutionAssets = append(target.PreferredExecutionAssets, source.PreferredExecutionAssets...)
	}
	if len(source.References) > 0 {
		target.References = append(target.References, source.References...)
	}
}

func validSkillDependencyName(value string) bool {
	if value == "" || len(value) > 64 || value[0] == '-' || value[len(value)-1] == '-' || strings.Contains(value, "--") {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' {
			continue
		}
		return false
	}
	return true
}

func frontMatterMetadataRequiredSkills(metadata *yaml.Node) ([]string, error) {
	if metadata == nil || metadata.Kind != yaml.MappingNode {
		return nil, nil
	}
	for index := 0; index+1 < len(metadata.Content); index += 2 {
		key := strings.ToLower(strings.TrimSpace(metadata.Content[index].Value))
		key = strings.NewReplacer("-", "", "_", "").Replace(key)
		if key != "dependencies" {
			continue
		}
		dependencies := metadata.Content[index+1]
		if dependencies.Kind != yaml.MappingNode {
			return nil, errors.New("frontmatter field \"metadata.dependencies\" must be a mapping")
		}
		for dependencyIndex := 0; dependencyIndex+1 < len(dependencies.Content); dependencyIndex += 2 {
			dependencyKey := strings.ToLower(strings.TrimSpace(dependencies.Content[dependencyIndex].Value))
			dependencyKey = strings.NewReplacer("-", "", "_", "").Replace(dependencyKey)
			if dependencyKey != "skills" {
				continue
			}
			values, err := strictFrontMatterStringSequence(
				dependencies.Content[dependencyIndex+1], "metadata.dependencies.skills",
			)
			if err != nil {
				return nil, err
			}
			if len(values) > 16 {
				return nil, errors.New("frontmatter field \"metadata.dependencies.skills\" exceeds 16 entries")
			}
			result := make([]string, 0, len(values))
			seen := make(map[string]struct{}, len(values))
			for _, dependency := range values {
				normalized := strings.ToLower(strings.TrimSpace(dependency))
				if !validSkillDependencyName(normalized) {
					return nil, errors.New("frontmatter field \"metadata.dependencies.skills\" contains an invalid skill name")
				}
				if _, duplicate := seen[normalized]; duplicate {
					return nil, errors.New("frontmatter field \"metadata.dependencies.skills\" contains a duplicate skill name")
				}
				seen[normalized] = struct{}{}
				result = append(result, normalized)
			}
			return result, nil
		}
	}
	return nil, nil
}

func splitKV(line string) (string, string) {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) == 1 {
		return strings.TrimSpace(parts[0]), ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

func splitCSV(value string) []string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "[")
	value = strings.TrimSuffix(value, "]")
	fields := strings.Split(value, ",")
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		item := strings.TrimSpace(strings.Trim(field, " \"'"))
		if item == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}

func validArgumentNames(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		name := strings.TrimSpace(value)
		if name == "" || isNumericString(name) {
			continue
		}
		out = append(out, name)
	}
	return out
}

func validCapabilityNames(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, errors.New("required capability list is empty")
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		name := strings.TrimSpace(value)
		if name == "" || len(name) > 64 {
			return nil, errors.New("required capability name is invalid")
		}
		valid := true
		previousHyphen := false
		for index, r := range name {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9' && index > 0) || (r == '-' && index > 0) {
				if r == '-' && previousHyphen {
					valid = false
					break
				}
				previousHyphen = r == '-'
				continue
			}
			valid = false
			break
		}
		if !valid || strings.HasSuffix(name, "-") {
			return nil, fmt.Errorf("required capability %q is invalid", name)
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("required capability %q is duplicated", name)
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out, nil
}

func isNumericString(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		normalized := filepath.Clean(filepath.ToSlash(strings.TrimSpace(value)))
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, value)
	}
	return out
}

func splitQueryTerms(query string) []string {
	return discoveryquery.Terms(query)
}

func truncateString(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	return value[:maxBytes]
}

func sha1Hex(value string) string {
	// Use a lightweight deterministic hash for catalog diagnostics.
	sum := 0
	for _, b := range []byte(value) {
		sum = (sum*131 + int(b)) & 0x7fffffff
	}
	return fmt.Sprintf("%x", sum)
}

func maxInt(first, second int) int {
	if first <= 0 {
		return second
	}
	return first
}

func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
