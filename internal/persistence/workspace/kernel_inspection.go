package workspace

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const KernelInspectionMaxRows = 1000

type KernelArtifactBrowseOptions struct {
	VersionID           string
	ProjectID           string
	FrameID             string
	Filename            string
	ExactFilename       bool
	ContentType         string
	Content             string
	After               *time.Time
	Before              *time.Time
	IncludeIntermediate bool
	Limit               int
	Offset              int
	Search              string
}

type KernelArtifactRecord struct {
	ID              string    `json:"id"`
	ProjectID       string    `json:"project_id"`
	RootFrameID     *string   `json:"root_frame_id"`
	FrameID         *string   `json:"frame_id"`
	Filename        string    `json:"filename"`
	ContentType     string    `json:"content_type"`
	SizeBytes       int64     `json:"size_bytes"`
	LatestVersionID string    `json:"latest_version_id"`
	Checksum        *string   `json:"checksum,omitempty"`
	VersionNumber   int       `json:"version_number"`
	IsUserUpload    bool      `json:"is_user_upload"`
	IsEphemeral     bool      `json:"is_ephemeral"`
	FolderID        *string   `json:"folder_id"`
	Priority        string    `json:"priority"`
	AgentName       *string   `json:"agent_name"`
	Language        *string   `json:"language"`
	IsIntermediate  bool      `json:"is_intermediate"`
	IsCheckpoint    bool      `json:"is_checkpoint"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	Score           *float64  `json:"_score,omitempty"`
	Weak            *bool     `json:"_weak,omitempty"`
}

type ArtifactVersionDependency struct {
	ID                 string    `json:"id"`
	VersionID          string    `json:"version_id"`
	DependsOnVersionID string    `json:"depends_on_version_id"`
	ReferenceName      string    `json:"reference_name,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
}

func (s *Store) RecordArtifactVersionDependency(ctx context.Context, versionID, dependsOnVersionID, referenceName string) (ArtifactVersionDependency, error) {
	if s == nil || s.db == nil {
		return ArtifactVersionDependency{}, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	versionID = strings.TrimSpace(versionID)
	dependsOnVersionID = strings.TrimSpace(dependsOnVersionID)
	referenceName = strings.TrimSpace(referenceName)
	if versionID == "" || dependsOnVersionID == "" || versionID == dependsOnVersionID {
		return ArtifactVersionDependency{}, errors.New("distinct artifact version dependency ids are required")
	}
	if len(referenceName) > 1024 {
		return ArtifactVersionDependency{}, errors.New("artifact dependency reference name exceeds 1024 bytes")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ArtifactVersionDependency{}, err
	}
	defer tx.Rollback()
	var outputOwner, inputOwner string
	if err := tx.QueryRowContext(ctx, `SELECT p.user_id FROM artifact_versions v JOIN artifacts a ON a.id=v.artifact_id JOIN projects p ON p.id=a.project_id WHERE v.id=?`, versionID).Scan(&outputOwner); err != nil {
		return ArtifactVersionDependency{}, fmt.Errorf("look up dependent artifact version: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT p.user_id FROM artifact_versions v JOIN artifacts a ON a.id=v.artifact_id JOIN projects p ON p.id=a.project_id WHERE v.id=?`, dependsOnVersionID).Scan(&inputOwner); err != nil {
		return ArtifactVersionDependency{}, fmt.Errorf("look up input artifact version: %w", err)
	}
	if outputOwner != inputOwner {
		return ArtifactVersionDependency{}, errors.New("artifact dependency versions belong to different owners")
	}
	dependency := ArtifactVersionDependency{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("synon-artifact-dependency\x00"+versionID+"\x00"+dependsOnVersionID)).String(), VersionID: versionID, DependsOnVersionID: dependsOnVersionID, ReferenceName: referenceName, CreatedAt: s.now().UTC()}
	if _, err := tx.ExecContext(ctx, `INSERT INTO artifact_version_dependencies(id,artifact_version_id,depends_on_version_id,reference_name,created_at) VALUES(?,?,?,?,?) ON CONFLICT(artifact_version_id,depends_on_version_id) DO UPDATE SET reference_name=excluded.reference_name`, dependency.ID, dependency.VersionID, dependency.DependsOnVersionID, nullableString(dependency.ReferenceName), dependency.CreatedAt); err != nil {
		return ArtifactVersionDependency{}, fmt.Errorf("record artifact version dependency: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ArtifactVersionDependency{}, err
	}
	return dependency, nil
}

func (s *Store) ListKernelArtifacts(ctx context.Context, ownerUserID, currentProjectID string, options KernelArtifactBrowseOptions) ([]KernelArtifactRecord, int, error) {
	if s == nil || s.db == nil {
		return nil, 0, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ownerUserID = strings.TrimSpace(ownerUserID)
	currentProjectID = strings.TrimSpace(currentProjectID)
	projectID := strings.TrimSpace(options.ProjectID)
	versionID := strings.TrimSpace(options.VersionID)
	if projectID == "" && versionID == "" {
		projectID = currentProjectID
	} else if projectID == "" {
		projectID = "all"
	}
	if ownerUserID == "" || currentProjectID == "" {
		return nil, 0, errors.New("kernel artifact owner and current project are required")
	}
	if options.Limit <= 0 {
		options.Limit = 200
	}
	if options.Limit > KernelInspectionMaxRows {
		return nil, 0, fmt.Errorf("kernel artifact limit exceeds %d", KernelInspectionMaxRows)
	}
	if options.Offset < 0 {
		return nil, 0, errors.New("kernel artifact offset must be non-negative")
	}
	if options.ExactFilename && strings.TrimSpace(options.Filename) == "" {
		return nil, 0, errors.New("exact filename matching requires filename")
	}
	if strings.TrimSpace(options.Search) != "" && (strings.TrimSpace(options.Filename) != "" || strings.TrimSpace(options.Content) != "") {
		return nil, 0, errors.New("artifact search and filename/content filters are mutually exclusive")
	}

	query := `SELECT a.id,a.project_id,m.root_frame_id,m.frame_id,a.name,
		COALESCE(NULLIF(p.content_type,''),a.kind),
		CASE WHEN COALESCE(v.storage_path,'')='' THEN length(v.content) ELSE v.size_bytes END,
		v.id,NULLIF(v.content_sha256,''),v.version_number,COALESCE(m.is_user_upload,0),COALESCE(m.is_ephemeral,0),
		a.folder_id,a.priority,p.agent_name,p.language,
		COALESCE(p.is_intermediate,0),COALESCE(p.is_checkpoint,0),a.created_at,a.updated_at
		FROM artifacts a
		JOIN projects project ON project.id=a.project_id
		JOIN artifact_versions v ON v.artifact_id=a.id AND v.version_number=a.current_version_number
		LEFT JOIN artifact_runtime_metadata m ON m.artifact_id=a.id
		LEFT JOIN artifact_version_provenance p ON p.version_id=v.id
		WHERE project.user_id=?`
	arguments := []any{ownerUserID}
	if versionID != "" {
		query += ` AND v.id=?`
		arguments = append(arguments, versionID)
	}
	if projectID != "all" {
		query += ` AND a.project_id=?`
		arguments = append(arguments, projectID)
	}
	if frameID := strings.TrimSpace(options.FrameID); frameID != "" {
		query += ` AND m.frame_id=?`
		arguments = append(arguments, frameID)
	}
	if filename := strings.TrimSpace(options.Filename); filename != "" {
		if options.ExactFilename {
			query += ` AND lower(a.name)=lower(?)`
			arguments = append(arguments, filename)
		} else {
			query += ` AND instr(lower(a.name),lower(?))>0`
			arguments = append(arguments, filename)
		}
	}
	if contentType := strings.TrimSpace(options.ContentType); contentType != "" {
		query += ` AND lower(COALESCE(NULLIF(p.content_type,''),a.kind)) LIKE lower(?)`
		arguments = append(arguments, contentType+"%")
	}
	if !options.IncludeIntermediate {
		query += ` AND COALESCE(p.is_intermediate,0)=0`
	}
	if options.After != nil {
		query += ` AND a.created_at>?`
		arguments = append(arguments, options.After.UTC())
	}
	if options.Before != nil {
		query += ` AND a.created_at<?`
		arguments = append(arguments, options.Before.UTC())
	}
	query += ` ORDER BY a.created_at DESC,a.id DESC`

	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, 0, fmt.Errorf("list kernel artifacts: %w", err)
	}
	defer rows.Close()
	records := make([]KernelArtifactRecord, 0)
	search := strings.ToLower(strings.TrimSpace(options.Search))
	for rows.Next() {
		var record KernelArtifactRecord
		var rootFrameID, frameID, checksum, folderID, agentName, language sql.NullString
		if err := rows.Scan(&record.ID, &record.ProjectID, &rootFrameID, &frameID, &record.Filename,
			&record.ContentType, &record.SizeBytes, &record.LatestVersionID, &checksum, &record.VersionNumber,
			&record.IsUserUpload, &record.IsEphemeral, &folderID, &record.Priority, &agentName, &language,
			&record.IsIntermediate, &record.IsCheckpoint, &record.CreatedAt, &record.UpdatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan kernel artifact: %w", err)
		}
		record.RootFrameID = nullableStringPointer(rootFrameID)
		record.FrameID = nullableStringPointer(frameID)
		record.Checksum = nullableStringPointer(checksum)
		record.FolderID = nullableStringPointer(folderID)
		record.AgentName = nullableStringPointer(agentName)
		record.Language = nullableStringPointer(language)
		if search != "" {
			score := kernelArtifactSearchScore(record.Filename, search)
			if score == 0 {
				continue
			}
			weak := score < 0.5
			record.Score, record.Weak = &score, &weak
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate kernel artifacts: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, 0, fmt.Errorf("close kernel artifact rows: %w", err)
	}
	if content := strings.TrimSpace(options.Content); content != "" {
		if len([]byte(content)) > 2048 {
			return nil, 0, errors.New("artifact content filter exceeds 2048 bytes")
		}
		filtered := records[:0]
		for _, record := range records {
			matched, err := s.artifactVersionContains(ctx, record.LatestVersionID, content)
			if err != nil {
				return nil, 0, err
			}
			if matched {
				filtered = append(filtered, record)
			}
		}
		records = filtered
	}
	if search != "" {
		sort.SliceStable(records, func(i, j int) bool {
			return *records[i].Score > *records[j].Score
		})
	}
	total := len(records)
	if options.Offset >= total {
		return []KernelArtifactRecord{}, total, nil
	}
	records = records[options.Offset:]
	if len(records) > options.Limit {
		records = records[:options.Limit]
	}
	return records, total, nil
}

func (s *Store) artifactVersionContains(ctx context.Context, versionID, pattern string) (bool, error) {
	_, _, reader, found, err := s.OpenArtifactVersionContent(versionID)
	if err != nil || !found {
		return false, err
	}
	defer reader.Close()
	needle := bytes.ToLower([]byte(pattern))
	buffer := make([]byte, 64*1024)
	overlap := make([]byte, 0, len(needle)-1)
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		default:
		}
		count, readErr := reader.Read(buffer)
		if count > 0 {
			window := make([]byte, 0, len(overlap)+count)
			window = append(window, overlap...)
			window = append(window, buffer[:count]...)
			if bytes.Contains(bytes.ToLower(window), needle) {
				return true, nil
			}
			keep := len(needle) - 1
			if keep > len(window) {
				keep = len(window)
			}
			overlap = append(overlap[:0], window[len(window)-keep:]...)
		}
		if errors.Is(readErr, io.EOF) {
			return false, nil
		}
		if readErr != nil {
			return false, fmt.Errorf("search artifact version content: %w", readErr)
		}
	}
}

func kernelArtifactSearchScore(filename, search string) float64 {
	filename = strings.ToLower(filename)
	if filename == search {
		return 1
	}
	if strings.Contains(filename, search) {
		return 0.8
	}
	tokens := strings.FieldsFunc(search, func(r rune) bool {
		return r == ' ' || r == '-' || r == '_' || r == '.' || r == '/'
	})
	if len(tokens) == 0 {
		return 0
	}
	matched := 0
	for _, token := range tokens {
		if token != "" && strings.Contains(filename, token) {
			matched++
		}
	}
	if matched == 0 {
		return 0
	}
	return float64(matched) / float64(len(tokens)) * 0.7
}

func (s *Store) ListArtifactVersionDependencies(ctx context.Context, versionID, direction string) ([]ArtifactVersionDependency, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	versionID = strings.TrimSpace(versionID)
	if versionID == "" {
		return nil, errors.New("artifact version id is required")
	}
	query := `SELECT id,artifact_version_id,depends_on_version_id,COALESCE(reference_name,''),created_at
		FROM artifact_version_dependencies WHERE artifact_version_id=? ORDER BY created_at,id`
	if direction == "down" {
		query = `SELECT id,artifact_version_id,depends_on_version_id,COALESCE(reference_name,''),created_at
			FROM artifact_version_dependencies WHERE depends_on_version_id=? ORDER BY created_at,id`
	} else if direction != "up" {
		return nil, errors.New("artifact dependency direction must be up or down")
	}
	rows, err := s.db.QueryContext(ctx, query, versionID)
	if err != nil {
		return nil, fmt.Errorf("list artifact version dependencies: %w", err)
	}
	defer rows.Close()
	result := make([]ArtifactVersionDependency, 0)
	for rows.Next() {
		var dependency ArtifactVersionDependency
		if err := rows.Scan(&dependency.ID, &dependency.VersionID, &dependency.DependsOnVersionID, &dependency.ReferenceName, &dependency.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan artifact version dependency: %w", err)
		}
		result = append(result, dependency)
	}
	return result, rows.Err()
}
