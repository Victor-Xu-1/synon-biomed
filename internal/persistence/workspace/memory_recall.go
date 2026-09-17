package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"synon-go/internal/memoryconfig"
	"synon-go/internal/memorypolicy"
)

const (
	memoryRecallRRFConstant  = memorypolicy.RRFConstant
	memoryRecallThreshold    = memorypolicy.RecallRRFThreshold
	memoryRecallProjectBoost = memorypolicy.RecallProjectBoost
	memoryRecallXProjectRank = memorypolicy.RecallCrossProjectRankMax
	memoryRecallXProjectMax  = memorypolicy.RecallCrossProjectMax
	memoryRecallProfileMax   = memorypolicy.RecallProfileMax
	memoryRecallReserveMax   = memorypolicy.RecallReserveMax
)

var (
	memoryCamelBoundaryLower = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	memoryCamelBoundaryUpper = regexp.MustCompile(`([A-Z]+)([A-Z][a-z])`)
	memoryRecallASCIIWord    = regexp.MustCompile(`\b[A-Za-z]{2,11}\b`)
	memoryRecallUUID         = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	memoryRecallToolUseID    = regexp.MustCompile(`\btoolu_[A-Za-z0-9]+\b`)
	memoryRecallScopeID      = regexp.MustCompile(`(?i)\b(frame|project|root_frame)[_-]?id\b[:\s=]*\S*`)
	memoryOperonSelfClosing  = regexp.MustCompile(`<operon:[a-zA-Z0-9_-]+\b[^>]*/>`)
	memoryOperonOpening      = regexp.MustCompile(`<operon:([a-zA-Z0-9_-]+)\b[^>]*>`)
	memorySkillDiscovery     = regexp.MustCompile(`(?s)<skill_discovery\b.*?</skill_discovery>`)
	memorySkillPayload       = regexp.MustCompile(`\{"type":"skill","name":"[\w-]+","display_name":"([^"\\]|\\.)*","description":"([^"\\]|\\.)*"\}`)
	memoryAttachmentPayloads = []*regexp.Regexp{
		regexp.MustCompile(`\{"type":"(artifact|attachment)","id":"[^"]+"(,"version_id":"[^"]+")?,"filename":"[^"]+","artifact_ref":"\{\{artifact:[^}]+\}\}","content_type":"[^"]+","size_bytes":[0-9]+(,"agent_name":"[^"]+")?\}`),
		regexp.MustCompile(`\{"artifact_ref":"\{\{artifact:[^}]+\}\}","filename":"[^"]+","content_type":"[^"]+","size_bytes":[0-9]+(,"agent_name":"[^"]+")?\}`),
		regexp.MustCompile(`\{"type":"(artifact|attachment)","id":"[^"]+"(,"version_id":"[^"]+")?,"filename":"[^"]+","file_path":"[^"]+","content_type":"[^"]+","size_bytes":[0-9]+(,"agent_name":"[^"]+")?\}`),
		regexp.MustCompile(`\{"file_path":"[^"]+","filename":"[^"]+","content_type":"[^"]+","size_bytes":[0-9]+(,"agent_name":"[^"]+")?\}`),
	}
	memoryRecallAcronyms = map[string]string{
		"pr": "pull request", "bq": "bigquery", "gcp": "google cloud", "gpu": "gpu graphics processing unit",
		"tpu": "tpu tensor processing unit", "scrna": "single cell rna sequencing", "scrnaseq": "single cell rna sequencing",
		"de": "differential expression", "qc": "quality control", "umap": "umap dimensionality reduction",
		"pca": "pca principal component analysis", "ko": "knockout", "wt": "wild type", "grna": "guide rna",
		"deg": "differentially expressed genes", "scvi": "scvi single cell variational inference", "adata": "anndata",
	}
	memoryRecallStopWords = map[string]struct{}{
		"the": {}, "a": {}, "an": {}, "and": {}, "or": {}, "of": {}, "to": {}, "in": {}, "on": {}, "for": {}, "with": {},
		"is": {}, "are": {}, "be": {}, "this": {}, "that": {}, "it": {}, "as": {}, "at": {}, "by": {}, "from": {},
		"you": {}, "your": {}, "i": {}, "we": {}, "my": {}, "our": {}, "me": {}, "do": {}, "does": {}, "can": {}, "will": {},
		"please": {}, "run": {}, "use": {}, "using": {}, "make": {}, "need": {}, "want": {}, "how": {}, "what": {},
		"ok": {}, "okay": {}, "sounds": {}, "great": {}, "good": {}, "sure": {}, "thanks": {}, "thank": {}, "yes": {}, "yeah": {},
		"yep": {}, "no": {}, "nope": {}, "ahead": {}, "go": {}, "now": {}, "let": {}, "lets": {},
	}
)

type MemoryRecallOptions struct {
	UserID       string
	ProjectID    string
	FrameID      string
	Query        string
	Limit        int
	CrossProject bool
	RecordAccess bool
	Now          time.Time
	Kind         string
	ServedIDs    map[string]struct{}
	// Config carries the exact workspace memory tuning. Nil preserves the
	// recovered defaults for repository callers that do not own runtime config.
	Config *memoryconfig.Config
	// StaleRankDecay is a pointer so nil can mean the workspace default
	// while an explicit zero remains a valid, observable configuration value.
	StaleRankDecay *float64
}

func memoryVisibleInProject(memory Memory, projectID string) bool {
	return memory.SubjectProjectID == "" || memory.SubjectProjectID == strings.TrimSpace(projectID)
}

type memoryRecallDocument struct {
	memory   Memory
	tokens   []string
	tokenSet map[string]struct{}
	tf       map[string]int
	length   int
}

type memoryRecallIndex struct {
	documents         []memoryRecallDocument
	documentFrequency map[string]int
	averageLength     float64
}

type memoryRecallScore struct {
	memory      Memory
	rrf         float64
	bm25Rank    int
	jaccardRank int
	inProject   bool
	isProfile   bool
	isContained bool
}

type memoryScanner interface {
	Scan(...any) error
}

type memoryTransaction interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func scanMemory(scanner memoryScanner) (Memory, error) {
	var memory Memory
	var surfaced sql.NullTime
	if err := scanner.Scan(
		&memory.ID, &memory.UserID, &memory.Body, &memory.SubjectProjectID,
		&memory.SubjectArtifactID, &memory.SubjectVersionID, &memory.SubjectFrameID, &memory.SourceFrameID,
		&memory.Origin, &memory.Evidence, &memory.SupersededBy,
		&memory.CategoryID, &memory.CategoryName, &memory.CategoryGuidance,
		&surfaced, &memory.CreatedAt, &memory.UpdatedAt,
	); err != nil {
		return Memory{}, err
	}
	if surfaced.Valid {
		value := surfaced.Time.UTC()
		memory.LastSurfacedAt = &value
	}
	return memory, nil
}

func (s *Store) createMemoryTx(ctx context.Context, tx memoryTransaction, input CreateMemoryInput, ownerUserID string) (Memory, error) {
	if tx == nil {
		return Memory{}, errors.New("memory transaction is required")
	}
	input, err := normalizeMemoryInput(input)
	if err != nil {
		return Memory{}, err
	}
	ownerUserID = strings.TrimSpace(ownerUserID)
	if ownerUserID == "" || input.UserID != ownerUserID {
		return Memory{}, errors.New("memory user must match authenticated owner")
	}
	if _, err := validateMemoryScopeTx(ctx, tx, input, ownerUserID); err != nil {
		return Memory{}, err
	}
	now := s.now().UTC()
	memory := Memory{
		ID: input.ID, UserID: ownerUserID, Body: input.Body,
		SubjectProjectID: input.SubjectProjectID, SubjectArtifactID: input.SubjectArtifactID,
		SubjectVersionID: input.SubjectVersionID, SubjectFrameID: input.SubjectFrameID,
		SourceFrameID: input.SourceFrameID, Origin: input.Origin, Evidence: input.Evidence,
		CategoryID: input.CategoryID, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memories (
		id, user_id, body, subject_project_id, subject_artifact_id, subject_version_id,
		subject_frame_id, category_id, source_frame_id, origin, evidence, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		memory.ID, memory.UserID, memory.Body, nullableString(memory.SubjectProjectID),
		nullableString(memory.SubjectArtifactID), nullableString(memory.SubjectVersionID),
		nullableString(memory.SubjectFrameID), nullableString(memory.CategoryID), nullableString(memory.SourceFrameID),
		memory.Origin, memory.Evidence, memory.CreatedAt, memory.UpdatedAt,
	); err != nil {
		return Memory{}, fmt.Errorf("insert memory: %w", err)
	}
	if memory.CategoryID != "" {
		if err := tx.QueryRowContext(ctx, `SELECT name, guidance FROM memory_categories WHERE id = ?`, memory.CategoryID).Scan(&memory.CategoryName, &memory.CategoryGuidance); err != nil {
			return Memory{}, fmt.Errorf("read assigned memory category: %w", err)
		}
	}
	return memory, nil
}

func normalizeMemoryInput(input CreateMemoryInput) (CreateMemoryInput, error) {
	input.ID = strings.TrimSpace(input.ID)
	input.UserID = strings.TrimSpace(input.UserID)
	input.Origin = strings.TrimSpace(input.Origin)
	input.Evidence = strings.ToLower(strings.TrimSpace(input.Evidence))
	input.SubjectProjectID = strings.TrimSpace(input.SubjectProjectID)
	input.SubjectArtifactID = strings.TrimSpace(input.SubjectArtifactID)
	input.SubjectVersionID = strings.TrimSpace(input.SubjectVersionID)
	input.SubjectFrameID = strings.TrimSpace(input.SubjectFrameID)
	input.SourceFrameID = strings.TrimSpace(input.SourceFrameID)
	input.CategoryID = strings.TrimSpace(input.CategoryID)
	for field, value := range map[string]string{"memory id": input.ID, "user id": input.UserID, "memory body": input.Body, "memory origin": input.Origin} {
		if value == "" {
			return CreateMemoryInput{}, fmt.Errorf("%s is required", field)
		}
	}
	if memorypolicy.UTF16Length(input.Body) > memorypolicy.TextMaxUTF16Units {
		return CreateMemoryInput{}, fmt.Errorf("memory body exceeds %d characters", memorypolicy.TextMaxUTF16Units)
	}
	if !utf8.ValidString(input.Body) {
		return CreateMemoryInput{}, errors.New("memory body must be valid UTF-8")
	}
	identifiers := []struct {
		name  string
		value string
		max   int
	}{
		{"memory id", input.ID, memorypolicy.MemoryIDMaxLength},
		{"user id", input.UserID, memorypolicy.UserIDMaxLength},
		{"memory project id", input.SubjectProjectID, memorypolicy.ProjectIDMaxLength},
		{"memory artifact id", input.SubjectArtifactID, memorypolicy.ArtifactIDMaxLength},
		{"memory version id", input.SubjectVersionID, memorypolicy.VersionIDMaxLength},
		{"memory frame id", input.SubjectFrameID, memorypolicy.FrameIDMaxLength},
		{"memory source frame id", input.SourceFrameID, memorypolicy.FrameIDMaxLength},
		{"memory category id", input.CategoryID, memorypolicy.CategoryIDMaxLength},
	}
	for _, identifier := range identifiers {
		if strings.ContainsAny(identifier.value, "\x00\r\n") || memorypolicy.UTF16Length(identifier.value) > identifier.max {
			return CreateMemoryInput{}, fmt.Errorf("%s must be a single-line value of at most %d characters", identifier.name, identifier.max)
		}
	}
	if input.Evidence == "" {
		input.Evidence = "stated"
	}
	switch input.Origin {
	case "extractor", "agent_tool", "user":
	default:
		return CreateMemoryInput{}, fmt.Errorf("unsupported memory origin %q", input.Origin)
	}
	switch input.Evidence {
	case "stated", "observed", "inferred":
	default:
		return CreateMemoryInput{}, fmt.Errorf("unsupported memory evidence %q", input.Evidence)
	}
	return input, nil
}

func validateMemoryScopeTx(ctx context.Context, tx memoryTransaction, input CreateMemoryInput, ownerUserID string) (string, error) {
	if input.SubjectFrameID != "" && input.CategoryID != "" {
		return "", errors.New("frame-scoped memories cannot carry a category")
	}
	projectID := input.SubjectProjectID
	if projectID != "" {
		var projectOwner string
		if err := tx.QueryRowContext(ctx, `SELECT user_id FROM projects WHERE id = ?`, projectID).Scan(&projectOwner); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", fmt.Errorf("project %q does not exist", projectID)
			}
			return "", fmt.Errorf("look up memory project: %w", err)
		}
		if projectOwner != ownerUserID {
			return "", errors.New("memory project is unavailable to owner")
		}
	}
	checks := []struct {
		label string
		id    string
		query string
	}{
		{"artifact", input.SubjectArtifactID, `SELECT a.project_id, p.user_id FROM artifacts a JOIN projects p ON p.id = a.project_id WHERE a.id = ?`},
		{"artifact version", input.SubjectVersionID, `SELECT a.project_id, p.user_id FROM artifact_versions v JOIN artifacts a ON a.id = v.artifact_id JOIN projects p ON p.id = a.project_id WHERE v.id = ?`},
		{"frame", input.SubjectFrameID, `SELECT f.project_id, p.user_id FROM frames f JOIN projects p ON p.id = f.project_id WHERE f.id = ?`},
	}
	for _, check := range checks {
		if check.id == "" {
			continue
		}
		var referencedProject, referencedOwner string
		if err := tx.QueryRowContext(ctx, check.query, check.id).Scan(&referencedProject, &referencedOwner); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", fmt.Errorf("memory %s %q does not exist", check.label, check.id)
			}
			return "", fmt.Errorf("look up memory %s: %w", check.label, err)
		}
		if referencedOwner != ownerUserID {
			return "", fmt.Errorf("memory %s is unavailable to owner", check.label)
		}
		if projectID == "" {
			projectID = referencedProject
		} else if projectID != referencedProject {
			return "", fmt.Errorf("memory %s belongs to a different project", check.label)
		}
	}
	if input.SourceFrameID != "" {
		var sourceProject, sourceOwner string
		if err := tx.QueryRowContext(ctx, `SELECT f.project_id, p.user_id FROM frames AS f JOIN projects AS p ON p.id = f.project_id WHERE f.id = ?`, input.SourceFrameID).Scan(&sourceProject, &sourceOwner); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", fmt.Errorf("memory source frame %q does not exist", input.SourceFrameID)
			}
			return "", fmt.Errorf("look up memory source frame: %w", err)
		}
		if sourceOwner != ownerUserID {
			return "", errors.New("memory source frame is unavailable to owner")
		}
		if projectID == "" {
			projectID = sourceProject
		} else if projectID != sourceProject {
			return "", errors.New("memory source frame belongs to a different project")
		}
	}
	if input.SubjectArtifactID != "" && input.SubjectVersionID != "" {
		var pairCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifact_versions WHERE id = ? AND artifact_id = ?`,
			input.SubjectVersionID, input.SubjectArtifactID).Scan(&pairCount); err != nil {
			return "", fmt.Errorf("verify memory artifact version pair: %w", err)
		}
		if pairCount != 1 {
			return "", errors.New("memory artifact version does not belong to artifact")
		}
	}
	if input.CategoryID != "" {
		var categoryOwner string
		if err := tx.QueryRowContext(ctx, `SELECT user_id FROM memory_categories WHERE id = ?`, input.CategoryID).Scan(&categoryOwner); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", fmt.Errorf("memory category %q does not exist", input.CategoryID)
			}
			return "", fmt.Errorf("look up memory category: %w", err)
		}
		if categoryOwner != ownerUserID {
			return "", errors.New("memory category is unavailable to owner")
		}
	}
	return projectID, nil
}

func (s *Store) RecallMemories(ctx context.Context, options MemoryRecallOptions) ([]Memory, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	options.UserID = strings.TrimSpace(options.UserID)
	options.ProjectID = strings.TrimSpace(options.ProjectID)
	options.FrameID = strings.TrimSpace(options.FrameID)
	config := memoryconfig.Default()
	if options.Config != nil {
		config = *options.Config
		if options.Limit == 0 {
			options.Limit = config.RecallInjectMax
		}
	}
	rawQuery := options.Query
	options.Query = memoryRecallQueryText(rawQuery)
	if options.UserID == "" {
		return nil, errors.New("memory user id is required")
	}
	enabled, err := s.MemoryEnabledWithDefault(ctx, options.UserID, config.Enabled)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return []Memory{}, nil
	}
	if options.Now.IsZero() {
		options.Now = s.now().UTC()
	} else {
		options.Now = options.Now.UTC()
	}
	queryTokens := memoryTokens(options.Query)
	if memoryRecallWordCount(options.Query, queryTokens) < memorypolicy.RecallMinWords || len(queryTokens) == 0 {
		return []Memory{}, nil
	}
	query := memorySelect + `
		WHERE m.user_id = ? AND m.superseded_by IS NULL`
	args := []any{options.UserID}
	if options.FrameID == "" {
		query += ` AND m.subject_frame_id IS NULL`
	} else {
		query += ` AND (m.subject_frame_id IS NULL OR m.subject_frame_id = ?)`
		args = append(args, options.FrameID)
	}
	query += ` ORDER BY m.created_at DESC, m.id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query memory recall candidates: %w", err)
	}
	defer rows.Close()
	candidates := make([]Memory, 0)
	for rows.Next() {
		memory, err := scanMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("scan memory recall candidate: %w", err)
		}
		candidates = append(candidates, memory)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memory recall candidates: %w", err)
	}
	contained, err := s.memoryRecallContainedIDs(ctx, options.UserID)
	if err != nil {
		return nil, err
	}
	inProject, err := s.memoryRecallProjectIDs(ctx, options.UserID, options.ProjectID)
	if err != nil {
		return nil, err
	}
	index := buildMemoryRecallIndex(candidates)
	if options.Kind == "subagent_spawn" || options.Kind == "plan_generated" {
		spawnTokens := distinctiveMemoryRecallTokens(
			index,
			memorySpawnRecallQueryText(rawQuery),
			config.RecallSpawnQueryMaxTokens,
			config.RecallSpawnQueryDFMaxRatio,
		)
		if len(spawnTokens) > 0 {
			queryTokens = spawnTokens
		}
	}
	staleRankDecay := config.StaleRankDecay
	if options.StaleRankDecay != nil {
		staleRankDecay = *options.StaleRankDecay
	}
	staleness, staleRankDecay, err := s.memoryRecallRankingPolicy(ctx, candidates, &staleRankDecay)
	if err != nil {
		return nil, err
	}
	scores := scoreMemoryRecallIndexWithPolicy(index, queryTokens, inProject, staleness, config.RecallProjectBoost, staleRankDecay)
	for index := range scores {
		_, scores[index].isContained = contained[scores[index].memory.ID]
	}
	selected := shapeMemoryRecallWithConfig(scores, options, config)
	candidates = make([]Memory, 0, len(selected))
	for _, score := range selected {
		memory := score.memory
		memory.RecallScore = score.rrf
		candidates = append(candidates, memory)
	}
	if options.RecordAccess && len(candidates) > 0 {
		if err := s.recordMemoryRecall(ctx, candidates, options.Now); err != nil {
			return nil, err
		}
		for index := range candidates {
			value := options.Now
			candidates[index].LastSurfacedAt = &value
		}
	}
	return candidates, nil
}

func (s *Store) memoryRecallContainedIDs(ctx context.Context, userID string) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.id
		FROM memories AS m
		JOIN memory_categories AS c ON c.id = m.category_id
		WHERE c.user_id = ? AND c.auto_recall = 0`, userID)
	if err != nil {
		return nil, fmt.Errorf("query contained memory ids: %w", err)
	}
	defer rows.Close()
	result := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan contained memory id: %w", err)
		}
		result[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate contained memory ids: %w", err)
	}
	return result, nil
}

func memoryRecallWordCount(query string, tokens []string) int {
	count := len(strings.Fields(query))
	if count >= memorypolicy.RecallMinWords {
		return count
	}
	for _, r := range query {
		if unicode.In(r, unicode.Han) {
			return len(tokens)
		}
	}
	return count
}

func (s *Store) memoryRecallProjectIDs(ctx context.Context, userID, projectID string) (map[string]struct{}, error) {
	result := map[string]struct{}{}
	if projectID == "" {
		return result, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT m.id FROM memories AS m
		WHERE m.user_id = ? AND m.superseded_by IS NULL AND m.subject_frame_id IS NULL AND (
			m.subject_project_id = ?
			OR m.subject_artifact_id IN (SELECT id FROM artifacts WHERE project_id = ?)
			OR m.subject_version_id IN (
				SELECT v.id FROM artifact_versions AS v JOIN artifacts AS a ON a.id = v.artifact_id WHERE a.project_id = ?
			)
		)`, userID, projectID, projectID, projectID)
	if err != nil {
		return nil, fmt.Errorf("query current-project memory ids: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan current-project memory id: %w", err)
		}
		result[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate current-project memory ids: %w", err)
	}
	return result, nil
}

func buildMemoryRecallIndex(memories []Memory) memoryRecallIndex {
	documents := make([]memoryRecallDocument, 0, len(memories))
	documentFrequency := map[string]int{}
	totalLength := 0
	for _, memory := range memories {
		// workspace indexes `${memory.id} ${memory.body}`. Keeping the ID in
		// the lexical document is observable when an agent searches a recalled ID.
		tokens := memoryTokens(memory.ID + " " + memory.Body)
		tokenSet := make(map[string]struct{}, len(tokens))
		tf := make(map[string]int, len(tokens))
		for _, token := range tokens {
			tf[token]++
			tokenSet[token] = struct{}{}
		}
		for token := range tokenSet {
			documentFrequency[token]++
		}
		totalLength += len(tokens)
		documents = append(documents, memoryRecallDocument{memory: memory, tokens: tokens, tokenSet: tokenSet, tf: tf, length: len(tokens)})
	}
	averageLength := float64(totalLength) / float64(max(len(documents), 1))
	if averageLength == 0 {
		averageLength = 1
	}
	return memoryRecallIndex{documents: documents, documentFrequency: documentFrequency, averageLength: averageLength}
}

func scoreMemoryDocuments(memories []Memory, queryTokens []string, inProject map[string]struct{}) []memoryRecallScore {
	return scoreMemoryRecallIndex(buildMemoryRecallIndex(memories), queryTokens, inProject)
}

func scoreMemoryRecallIndex(recallIndex memoryRecallIndex, queryTokens []string, inProject map[string]struct{}) []memoryRecallScore {
	return scoreMemoryRecallIndexWithStaleness(recallIndex, queryTokens, inProject, nil, memorypolicy.StaleRankDecay)
}

func scoreMemoryRecallIndexWithStaleness(
	recallIndex memoryRecallIndex,
	queryTokens []string,
	inProject map[string]struct{},
	staleness map[string]MemoryStaleness,
	staleRankDecay float64,
) []memoryRecallScore {
	return scoreMemoryRecallIndexWithPolicy(recallIndex, queryTokens, inProject, staleness, memorypolicy.RecallProjectBoost, staleRankDecay)
}

func scoreMemoryRecallIndexWithPolicy(
	recallIndex memoryRecallIndex,
	queryTokens []string,
	inProject map[string]struct{},
	staleness map[string]MemoryStaleness,
	projectBoost float64,
	staleRankDecay float64,
) []memoryRecallScore {
	documents := recallIndex.documents
	if len(documents) == 0 {
		return nil
	}
	type rankedScore struct {
		index int
		score float64
	}
	bm25, jaccard := make([]rankedScore, 0, len(documents)), make([]rankedScore, 0, len(documents))
	querySet := make(map[string]struct{}, len(queryTokens))
	for _, token := range queryTokens {
		querySet[token] = struct{}{}
	}
	for documentIndex, document := range documents {
		if score := memoryBM25(queryTokens, document, recallIndex.documentFrequency, len(documents), recallIndex.averageLength); score > 0 {
			bm25 = append(bm25, rankedScore{index: documentIndex, score: score})
		}
		if score := memoryJaccard(querySet, document.tokenSet); score > 0 {
			jaccard = append(jaccard, rankedScore{index: documentIndex, score: score})
		}
	}
	sort.SliceStable(bm25, func(i, j int) bool { return bm25[i].score > bm25[j].score })
	sort.SliceStable(jaccard, func(i, j int) bool { return jaccard[i].score > jaccard[j].score })
	bm25Ranks, jaccardRanks := map[int]int{}, map[int]int{}
	for index, score := range bm25 {
		bm25Ranks[score.index] = index + 1
	}
	for index, score := range jaccard {
		jaccardRanks[score.index] = index + 1
	}
	result := make([]memoryRecallScore, 0, len(documents))
	for index, document := range documents {
		bm25Rank, hasBM25 := bm25Ranks[index]
		jaccardRank, hasJaccard := jaccardRanks[index]
		if !hasBM25 && !hasJaccard {
			continue
		}
		rrf := 0.0
		if hasBM25 {
			rrf += 1 / (memoryRecallRRFConstant + float64(bm25Rank))
		} else {
			bm25Rank = math.MaxInt
		}
		if hasJaccard {
			rrf += 1 / (memoryRecallRRFConstant + float64(jaccardRank))
		} else {
			jaccardRank = math.MaxInt
		}
		_, currentProject := inProject[document.memory.ID]
		if currentProject {
			rrf *= projectBoost
		}
		if note, exists := staleness[document.memory.ID]; exists && note.IsStale {
			rrf *= staleRankDecay
		}
		result = append(result, memoryRecallScore{
			memory: document.memory, rrf: rrf, bm25Rank: bm25Rank, jaccardRank: jaccardRank,
			inProject: currentProject, isProfile: memoryEntityKey(document.memory) == "profile",
		})
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].rrf > result[j].rrf })
	return result
}

func (s *Store) memoryRecallRankingPolicy(ctx context.Context, memories []Memory, configured *float64) (map[string]MemoryStaleness, float64, error) {
	decay := memorypolicy.StaleRankDecay
	if configured != nil {
		decay = *configured
	}
	if decay == 1 || len(memories) == 0 {
		return nil, decay, nil
	}
	staleness, err := s.MemoryStalenessFor(ctx, memories)
	if err != nil {
		return nil, 0, fmt.Errorf("resolve memory staleness for ranking: %w", err)
	}
	return staleness, decay, nil
}

func distinctiveMemoryRecallTokens(index memoryRecallIndex, query string, maxTokens int, dfMaxRatio float64) []string {
	if maxTokens <= 0 || len(index.documents) == 0 {
		return nil
	}
	result := make([]string, 0, min(maxTokens, len(memoryTokens(query))))
	seen := make(map[string]struct{})
	for _, token := range memoryTokens(query) {
		if _, duplicate := seen[token]; duplicate {
			continue
		}
		seen[token] = struct{}{}
		if memoryRecallShortNumber(token) {
			continue
		}
		if _, stopped := memoryRecallStopWords[token]; stopped {
			continue
		}
		df := index.documentFrequency[token]
		if df == 0 {
			continue
		}
		if df >= 3 && float64(df)/float64(len(index.documents)) > dfMaxRatio {
			continue
		}
		result = append(result, token)
		if len(result) >= maxTokens {
			break
		}
	}
	return result
}

func memoryRecallShortNumber(value string) bool {
	if len(value) == 0 || len(value) > 2 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func shapeMemoryRecall(scores []memoryRecallScore, options MemoryRecallOptions) []memoryRecallScore {
	return shapeMemoryRecallWithConfig(scores, options, memoryconfig.Default())
}

func shapeMemoryRecallWithConfig(scores []memoryRecallScore, options MemoryRecallOptions, config memoryconfig.Config) []memoryRecallScore {
	crossProject := make([]memoryRecallScore, 0, max(0, config.RecallCrossProjectMax))
	if options.CrossProject && config.RecallCrossProjectRankMax > 0 && config.RecallCrossProjectMax > 0 {
		for _, score := range scores {
			if score.isContained || score.inProject || score.isProfile || score.bm25Rank > config.RecallCrossProjectRankMax || score.jaccardRank > config.RecallCrossProjectRankMax {
				continue
			}
			crossProject = append(crossProject, score)
			if len(crossProject) == config.RecallCrossProjectMax {
				break
			}
		}
	}
	primaryLimit := options.Limit - len(crossProject)
	if primaryLimit < 0 {
		primaryLimit = 0
	}
	primary := make([]memoryRecallScore, 0, primaryLimit)
	profileCount := 0
	for _, score := range scores {
		if len(primary) >= primaryLimit {
			break
		}
		if score.isContained || (!score.inProject && !score.isProfile) || score.rrf < config.RecallRRFThreshold {
			continue
		}
		if score.isProfile {
			if profileCount >= max(0, config.RecallProfileMax) {
				continue
			}
			profileCount++
		}
		primary = append(primary, score)
	}
	selected := append(primary, crossProject...)
	sort.SliceStable(selected, func(i, j int) bool { return selected[i].rrf > selected[j].rrf })
	selected = selected[:memoryRecallSliceEnd(len(selected), options.Limit)]
	if len(options.ServedIDs) == 0 {
		return selected
	}
	allowReserve := options.Kind == "subagent_spawn" || options.Kind == "plan_generated"
	reserveCount := 0
	filtered := make([]memoryRecallScore, 0, len(selected))
	for _, score := range selected {
		_, served := options.ServedIDs[score.memory.ID]
		if !served {
			_, served = options.ServedIDs[strings.ToLower(score.memory.ID)]
		}
		if !served {
			filtered = append(filtered, score)
			continue
		}
		if !allowReserve || score.isProfile || reserveCount >= max(0, config.RecallReserveMax) {
			continue
		}
		reserveCount++
		filtered = append(filtered, score)
	}
	return filtered
}

// JavaScript Array.slice(0, end) treats a negative end as an offset from the
// array tail. workspace exposes recall_inject_max without a non-negative
// schema constraint, so this edge is part of the observable source contract.
func memoryRecallSliceEnd(length, end int) int {
	if end < 0 {
		end = length + end
	}
	if end < 0 {
		return 0
	}
	if end > length {
		return length
	}
	return end
}

func memoryBM25(queryTokens []string, document memoryRecallDocument, documentFrequency map[string]int, documentCount int, averageLength float64) float64 {
	const k1, b = memorypolicy.BM25K1, memorypolicy.BM25B
	result := 0.0
	seen := map[string]struct{}{}
	for _, token := range queryTokens {
		if _, duplicate := seen[token]; duplicate {
			continue
		}
		seen[token] = struct{}{}
		frequency := document.tf[token]
		if frequency == 0 {
			continue
		}
		df := documentFrequency[token]
		idf := math.Log(1 + (float64(documentCount-df)+0.5)/(float64(df)+0.5))
		denominator := float64(frequency) + k1*(1-b+b*float64(document.length)/averageLength)
		result += idf * (float64(frequency) * (k1 + 1) / denominator)
	}
	return result
}

func memoryJaccard(query, document map[string]struct{}) float64 {
	if len(query) == 0 || len(document) == 0 {
		return 0
	}
	intersection := 0
	for token := range query {
		if _, exists := document[token]; exists {
			intersection++
		}
	}
	if intersection == 0 {
		return 0
	}
	return float64(intersection) / float64(len(query)+len(document)-intersection)
}

func memoryEntityKey(memory Memory) string {
	switch {
	case memory.SubjectFrameID != "":
		return "frame:" + memory.SubjectFrameID
	case memory.SubjectArtifactID != "":
		return "artifact:" + memory.SubjectArtifactID
	case memory.SubjectProjectID != "":
		return "project:" + memory.SubjectProjectID
	default:
		return "profile"
	}
}

func boundMemoryRecallQuery(value string) string {
	value = strings.TrimSpace(value)
	return memorypolicy.PrefixUTF16(value, memorypolicy.SearchQueryMaxUTF16Units)
}

// memoryRecallQueryText matches workspace Iwz: bound the request, remove
// canonical attachment payloads while retaining searchable attachment metadata,
// strip harness-only fields, expand the source acronym table, and collapse space.
func memoryRecallQueryText(value string) string {
	value = memorypolicy.PrefixUTF16(value, memorypolicy.SearchQueryMaxUTF16Units)
	attachmentMetadata := memoryRecallAttachmentMetadata(value)
	if len(attachmentMetadata) > 0 {
		value = stripMemoryAttachmentPayloads(value)
		value += " " + strings.Join(attachmentMetadata, " ")
	}
	value = cleanMemoryRecallHarnessText(value)
	value = expandMemoryRecallAcronyms(value)
	return strings.Join(strings.Fields(value), " ")
}

// workspace uses a stricter source text for spawn/plan query shaping. It
// removes skill and attachment transport payloads and does not add attachment
// metadata or acronym expansions before selecting distinctive index tokens.
func memorySpawnRecallQueryText(value string) string {
	value = memorypolicy.PrefixUTF16(value, memorypolicy.SearchQueryMaxUTF16Units)
	value = stripMemoryAttachmentPayloads(value)
	value = memorySkillPayload.ReplaceAllString(value, " ")
	return cleanMemoryRecallHarnessText(value)
}

func memoryRecallAttachmentMetadata(value string) []string {
	type attachment struct {
		ArtifactRef string `json:"artifact_ref"`
		FilePath    string `json:"file_path"`
		Filename    string `json:"filename"`
		ContentType string `json:"content_type"`
	}
	seenReferences := make(map[string]struct{})
	seenMetadata := make(map[string]struct{})
	result := make([]string, 0)
	for _, pattern := range memoryAttachmentPayloads {
		for _, payload := range pattern.FindAllString(value, -1) {
			var item attachment
			if err := json.Unmarshal([]byte(payload), &item); err != nil {
				continue
			}
			reference := item.ArtifactRef
			if reference == "" {
				reference = item.FilePath
			}
			if reference == "" {
				continue
			}
			if _, duplicate := seenReferences[reference]; duplicate {
				continue
			}
			seenReferences[reference] = struct{}{}
			parts := []string{"attached"}
			if item.Filename != "" {
				parts = append(parts, item.Filename)
				if extension := memoryRecallFilenameExtension(item.Filename); extension != "" {
					parts = append(parts, extension)
				}
			}
			if slash := strings.IndexByte(item.ContentType, '/'); slash >= 0 {
				subtype := strings.NewReplacer("/", " ", "+", " ", ".", " ").Replace(item.ContentType[slash+1:])
				if subtype != "" {
					parts = append(parts, subtype)
				}
			}
			metadata := strings.Join(parts, " ")
			if metadata == "attached" {
				continue
			}
			if _, duplicate := seenMetadata[metadata]; duplicate {
				continue
			}
			seenMetadata[metadata] = struct{}{}
			result = append(result, metadata)
		}
	}
	return result
}

func memoryRecallFilenameExtension(filename string) string {
	dot := strings.LastIndexByte(filename, '.')
	if dot < 0 || dot == len(filename)-1 {
		return ""
	}
	extension := filename[dot+1:]
	if len(extension) > 8 {
		return ""
	}
	for _, r := range extension {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return ""
		}
	}
	return strings.ToLower(extension)
}

func stripMemoryAttachmentPayloads(value string) string {
	for _, pattern := range memoryAttachmentPayloads {
		value = pattern.ReplaceAllString(value, " ")
	}
	return value
}

func cleanMemoryRecallHarnessText(value string) string {
	value = stripMemorySystemSegments(value)
	value = memoryOperonSelfClosing.ReplaceAllString(value, " ")
	value = stripMemoryOperonBlocks(value)
	value = memorySkillDiscovery.ReplaceAllString(value, " ")
	value = memoryRecallUUID.ReplaceAllString(value, " ")
	value = memoryRecallToolUseID.ReplaceAllString(value, " ")
	return memoryRecallScopeID.ReplaceAllString(value, " ")
}

func stripMemorySystemSegments(value string) string {
	const marker = "[System]"
	var result strings.Builder
	for {
		start := strings.Index(value, marker)
		if start < 0 {
			result.WriteString(value)
			break
		}
		result.WriteString(value[:start])
		rest := value[start+len(marker):]
		blankLine := strings.Index(rest, "\n\n")
		nextSystem := strings.Index(rest, "\n"+marker)
		end := -1
		switch {
		case blankLine >= 0 && nextSystem >= 0:
			end = min(blankLine, nextSystem)
		case blankLine >= 0:
			end = blankLine
		case nextSystem >= 0:
			end = nextSystem
		}
		result.WriteByte(' ')
		if end < 0 {
			break
		}
		value = rest[end:]
	}
	return result.String()
}

func stripMemoryOperonBlocks(value string) string {
	var result strings.Builder
	for {
		location := memoryOperonOpening.FindStringSubmatchIndex(value)
		if location == nil {
			result.WriteString(value)
			break
		}
		tag := value[location[2]:location[3]]
		closing := "</operon:" + tag + ">"
		closeOffset := strings.Index(value[location[1]:], closing)
		if closeOffset < 0 {
			result.WriteString(value)
			break
		}
		result.WriteString(value[:location[0]])
		result.WriteByte(' ')
		value = value[location[1]+closeOffset+len(closing):]
	}
	return result.String()
}

func expandMemoryRecallAcronyms(value string) string {
	return memoryRecallASCIIWord.ReplaceAllStringFunc(value, func(word string) string {
		key := strings.ToLower(word)
		expansion, exists := memoryRecallAcronyms[key]
		if !exists && strings.HasSuffix(key, "s") {
			expansion, exists = memoryRecallAcronyms[strings.TrimSuffix(key, "s")]
		}
		if !exists {
			return word
		}
		return word + " " + expansion
	})
}

func memoryTokens(value string) []string {
	result := make([]string, 0)
	var word strings.Builder
	var previousHan rune
	flush := func() {
		if word.Len() > 0 {
			result = appendMemoryWordTokens(result, word.String())
		}
		word.Reset()
	}
	for _, r := range strings.TrimSpace(value) {
		switch {
		case unicode.In(r, unicode.Han):
			flush()
			result = append(result, string(r))
			if previousHan != 0 {
				result = append(result, string([]rune{previousHan, r}))
			}
			previousHan = r
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			previousHan = 0
			word.WriteRune(r)
		default:
			previousHan = 0
			flush()
		}
	}
	flush()
	return result
}

func appendMemoryWordTokens(result []string, raw string) []string {
	candidates := []string{raw}
	camelSplit := memoryCamelBoundaryLower.ReplaceAllString(raw, "$1 $2")
	camelSplit = memoryCamelBoundaryUpper.ReplaceAllString(camelSplit, "$1 $2")
	if camelSplit != raw {
		candidates = append(candidates, strings.Fields(camelSplit)...)
	}
	for _, candidate := range candidates {
		token := strings.ToLower(candidate)
		if utf8.RuneCountInString(token) < 2 {
			continue
		}
		if _, stop := memoryRecallStopWords[token]; stop {
			continue
		}
		if len(token) > 3 && strings.HasSuffix(token, "s") && !strings.HasSuffix(token, "ss") {
			token = strings.TrimSuffix(token, "s")
		}
		result = append(result, token)
	}
	return result
}

func (s *Store) recordMemoryRecall(ctx context.Context, memories []Memory, surfacedAt time.Time) error {
	return s.WithTransaction(ctx, func(tx *sql.Tx) error {
		for _, memory := range memories {
			result, err := tx.ExecContext(ctx, `UPDATE memories SET last_surfaced_at = ? WHERE id = ? AND user_id = ? AND superseded_by IS NULL`, surfacedAt, memory.ID, memory.UserID)
			if err != nil {
				return fmt.Errorf("record memory recall %s: %w", memory.ID, err)
			}
			changed, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("read memory recall update result for %s: %w", memory.ID, err)
			}
			if changed != 1 {
				return fmt.Errorf("record memory recall %s affected %d rows", memory.ID, changed)
			}
		}
		return nil
	})
}

func (s *Store) ProjectMemoryEnabled(ctx context.Context, projectID, ownerUserID string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("workspace store is closed")
	}
	var owner string
	var enabled sql.NullBool
	if err := s.db.QueryRowContext(ctx, `SELECT user_id, memory_enabled FROM projects WHERE id = ?`, strings.TrimSpace(projectID)).Scan(&owner, &enabled); err != nil {
		return false, fmt.Errorf("read project memory setting: %w", err)
	}
	if owner != strings.TrimSpace(ownerUserID) {
		return false, errors.New("project memory setting is unavailable to owner")
	}
	return !enabled.Valid || enabled.Bool, nil
}
