package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const compatibilitySessionTotalsNote = "frames[].input_tokens/output_tokens/total_cost are subtree-cumulative (each frame includes all descendants; the root frame equals the whole session). summary totals equal the root frame. Summing frames[] double-counts."

type CompatibilitySessionExport struct {
	ExportVersion    string                         `json:"export_version"`
	ExportedAt       string                         `json:"exported_at"`
	RootFrameID      string                         `json:"root_frame_id"`
	ProjectID        string                         `json:"project_id"`
	ConversationName string                         `json:"conversation_name"`
	Summary          CompatibilitySessionSummary    `json:"summary"`
	Frames           []CompatibilitySessionFrame    `json:"frames"`
	Artifacts        []CompatibilitySessionArtifact `json:"artifacts"`
}

type CompatibilitySessionSummary struct {
	UserEmail      any                        `json:"user_email"`
	TotalFrames    int                        `json:"total_frames"`
	TotalTokens    CompatibilitySessionTokens `json:"total_tokens"`
	TotalCost      float64                    `json:"total_cost"`
	TotalsNote     string                     `json:"totals_note"`
	DurationSecond *float64                   `json:"duration_seconds"`
	AgentsUsed     []string                   `json:"agents_used"`
	Status         string                     `json:"status"`
}

type CompatibilitySessionTokens struct {
	Input  int64 `json:"input"`
	Output int64 `json:"output"`
}

type CompatibilitySessionFrame struct {
	ID                 string                           `json:"id"`
	ParentFrameID      *string                          `json:"parent_frame_id"`
	AgentName          string                           `json:"agent_name"`
	DelegateName       *string                          `json:"delegate_name"`
	Status             string                           `json:"status"`
	TaskSummary        *string                          `json:"task_summary"`
	IsHidden           bool                             `json:"is_hidden"`
	Model              *string                          `json:"model"`
	Effort             *string                          `json:"effort"`
	InputTokens        int64                            `json:"input_tokens"`
	OutputTokens       int64                            `json:"output_tokens"`
	TotalCost          float64                          `json:"total_cost"`
	CreatedAt          string                           `json:"created_at"`
	CompletedAt        *string                          `json:"completed_at"`
	InputData          any                              `json:"input_data"`
	OutputData         any                              `json:"output_data"`
	ContextMetadata    map[string]any                   `json:"context_metadata"`
	Messages           []map[string]any                 `json:"messages"`
	CompactionArchives []CompatibilityCompactionArchive `json:"compaction_archives"`
	ChildrenIDs        []string                         `json:"children_ids"`
	projectID          string
	rootFrameID        string
	conversationName   string
	frameCreatedAt     time.Time
}

type CompatibilityCompactionArchive struct {
	CompactionIndex int              `json:"compaction_index"`
	MessageCount    int              `json:"message_count"`
	TokenCount      *int             `json:"token_count"`
	Summary         string           `json:"summary"`
	Messages        []map[string]any `json:"messages"`
}

type CompatibilitySessionArtifact struct {
	ArtifactID  string  `json:"artifact_id"`
	VersionID   string  `json:"version_id"`
	Filename    string  `json:"filename"`
	ContentType string  `json:"content_type"`
	SizeBytes   int64   `json:"size_bytes"`
	FrameID     *string `json:"frame_id"`
	AgentName   *string `json:"agent_name"`
	CreatedAt   *string `json:"created_at"`
}

// BuildCompatibilitySessionExport projects the durable Go workspace into the
// public v1.1 session export schema. Authorization is part of the query so an
// inaccessible root is indistinguishable from a missing one.
func (s *Store) BuildCompatibilitySessionExport(
	ctx context.Context, ownerUserID, rootFrameID string, exportedAt time.Time,
) (CompatibilitySessionExport, bool, error) {
	if s == nil || s.db == nil {
		return CompatibilitySessionExport{}, false, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ownerUserID, rootFrameID = strings.TrimSpace(ownerUserID), strings.TrimSpace(rootFrameID)
	if ownerUserID == "" || rootFrameID == "" {
		return CompatibilitySessionExport{}, false, errors.New("session export owner and root frame id are required")
	}
	if exportedAt.IsZero() {
		exportedAt = s.now().UTC()
	} else {
		exportedAt = exportedAt.UTC()
	}
	resolvedRootID, found, err := s.resolveCompatibilitySessionRoot(ctx, ownerUserID, rootFrameID)
	if err != nil || !found {
		return CompatibilitySessionExport{}, found, err
	}
	rootFrameID = resolvedRootID

	rows, err := s.db.QueryContext(ctx, `
		SELECT f.id, f.project_id, f.parent_frame_id, f.root_frame_id,
			f.agent_name, f.status, f.name, f.created_at,
			m.delegate_name, m.task_summary, COALESCE(m.is_hidden, 0), m.model, m.effort,
			COALESCE(m.input_tokens, 0), COALESCE(m.output_tokens, 0), COALESCE(m.total_cost, 0),
			m.completed_at, m.input_data, m.output_data, COALESCE(m.context_data, '{}')
		FROM frames f
		JOIN projects p ON p.id = f.project_id AND p.user_id = ?
		LEFT JOIN frame_runtime_metadata m ON m.frame_id = f.id
		WHERE f.root_frame_id = ?
		ORDER BY f.root_sequence, f.id`, ownerUserID, rootFrameID)
	if err != nil {
		return CompatibilitySessionExport{}, false, fmt.Errorf("query compatibility session frames: %w", err)
	}
	defer rows.Close()

	frames := make([]CompatibilitySessionFrame, 0)
	for rows.Next() {
		var frame CompatibilitySessionFrame
		var parentID, delegate, taskSummary, model, effort sql.NullString
		var completed sql.NullTime
		var inputData, outputData sql.NullString
		var rawContext string
		if err := rows.Scan(
			&frame.ID, &frame.projectID, &parentID, &frame.rootFrameID,
			&frame.AgentName, &frame.Status, &frame.conversationName, &frame.frameCreatedAt,
			&delegate, &taskSummary, &frame.IsHidden, &model, &effort,
			&frame.InputTokens, &frame.OutputTokens, &frame.TotalCost,
			&completed, &inputData, &outputData, &rawContext,
		); err != nil {
			return CompatibilitySessionExport{}, false, fmt.Errorf("scan compatibility session frame: %w", err)
		}
		frame.ParentFrameID = nullableStringPointer(parentID)
		frame.DelegateName = nullableStringPointer(delegate)
		frame.TaskSummary = nullableStringPointer(taskSummary)
		frame.Model = nullableStringPointer(model)
		frame.Effort = nullableStringPointer(effort)
		frame.CreatedAt = frame.frameCreatedAt.UTC().Format(time.RFC3339Nano)
		if completed.Valid {
			value := completed.Time.UTC().Format(time.RFC3339Nano)
			frame.CompletedAt = &value
		}
		if frame.InputData, err = decodeCompatibilityExportJSON(inputData); err != nil {
			return CompatibilitySessionExport{}, false, fmt.Errorf("decode frame %s input data: %w", frame.ID, err)
		}
		if frame.OutputData, err = decodeCompatibilityExportJSON(outputData); err != nil {
			return CompatibilitySessionExport{}, false, fmt.Errorf("decode frame %s output data: %w", frame.ID, err)
		}
		var contextData map[string]any
		if err := json.Unmarshal([]byte(rawContext), &contextData); err != nil {
			return CompatibilitySessionExport{}, false, fmt.Errorf("decode frame %s context data: %w", frame.ID, err)
		}
		frame.ContextMetadata = compatibilitySessionContextMetadata(contextData)
		frame.ChildrenIDs = []string{}
		frames = append(frames, frame)
	}
	if err := rows.Err(); err != nil {
		return CompatibilitySessionExport{}, false, fmt.Errorf("iterate compatibility session frames: %w", err)
	}
	if err := rows.Close(); err != nil {
		return CompatibilitySessionExport{}, false, fmt.Errorf("close compatibility session frames: %w", err)
	}
	if len(frames) == 0 {
		return CompatibilitySessionExport{}, false, nil
	}
	for index := range frames {
		frames[index].Messages, err = s.listCompatibilitySessionMessages(ctx, frames[index].ID)
		if err != nil {
			return CompatibilitySessionExport{}, false, err
		}
		frames[index].CompactionArchives, err = s.listCompatibilityCompactionArchives(frames[index].ID)
		if err != nil {
			return CompatibilitySessionExport{}, false, err
		}
		if frames[index].CompactionArchives == nil {
			frames[index].CompactionArchives = []CompatibilityCompactionArchive{}
		}
	}
	rootIndex := -1
	byID := make(map[string]int, len(frames))
	for index := range frames {
		byID[frames[index].ID] = index
		if frames[index].ID == rootFrameID && frames[index].ParentFrameID == nil {
			rootIndex = index
		}
	}
	if rootIndex < 0 {
		return CompatibilitySessionExport{}, false, nil
	}
	for index := range frames {
		if frames[index].ParentFrameID == nil {
			continue
		}
		if parent, found := byID[*frames[index].ParentFrameID]; found {
			frames[parent].ChildrenIDs = append(frames[parent].ChildrenIDs, frames[index].ID)
		}
	}

	artifacts, err := s.compatibilitySessionArtifacts(ctx, ownerUserID, frames[rootIndex].projectID, rootFrameID)
	if err != nil {
		return CompatibilitySessionExport{}, false, err
	}
	agents := make([]string, 0)
	seenAgents := make(map[string]struct{})
	for _, frame := range frames {
		if _, found := seenAgents[frame.AgentName]; frame.AgentName == "" || found {
			continue
		}
		seenAgents[frame.AgentName] = struct{}{}
		agents = append(agents, frame.AgentName)
	}
	sort.Strings(agents)
	root := frames[rootIndex]
	var duration *float64
	if root.CompletedAt != nil {
		if completed, parseErr := time.Parse(time.RFC3339Nano, *root.CompletedAt); parseErr == nil {
			created, createErr := time.Parse(time.RFC3339Nano, root.CreatedAt)
			if createErr == nil {
				seconds := completed.Sub(created).Seconds()
				duration = &seconds
			}
		}
	}
	exported := CompatibilitySessionExport{
		ExportVersion: "1.0", ExportedAt: exportedAt.Format(time.RFC3339Nano),
		RootFrameID: rootFrameID, ProjectID: root.projectID, ConversationName: root.conversationName,
		Summary: CompatibilitySessionSummary{
			UserEmail: nil, TotalFrames: len(frames),
			TotalTokens: CompatibilitySessionTokens{Input: root.InputTokens, Output: root.OutputTokens},
			TotalCost:   math.Round(root.TotalCost*10_000) / 10_000, TotalsNote: compatibilitySessionTotalsNote,
			DurationSecond: duration, AgentsUsed: agents, Status: root.Status,
		},
		Frames: frames, Artifacts: artifacts,
	}
	return exported, true, nil
}

func (s *Store) resolveCompatibilitySessionRoot(
	ctx context.Context, ownerUserID, rootFramePrefix string,
) (string, bool, error) {
	var rootFrameID string
	err := s.db.QueryRowContext(ctx, `
		SELECT frame.id
		FROM frames frame
		JOIN projects project ON project.id = frame.project_id
		WHERE project.user_id = ? AND frame.parent_frame_id IS NULL
			AND (frame.id = ? OR frame.id LIKE ? ESCAPE '\')
		ORDER BY CASE WHEN frame.id = ? THEN 0 ELSE 1 END, frame.id
		LIMIT 1`, ownerUserID, rootFramePrefix, escapeCompatibilityLike(rootFramePrefix)+"%", rootFramePrefix,
	).Scan(&rootFrameID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("resolve compatibility session root: %w", err)
	}
	return rootFrameID, true, nil
}

func escapeCompatibilityLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}

func (s *Store) listCompatibilityCompactionArchives(frameID string) ([]CompatibilityCompactionArchive, error) {
	metadata, err := s.ListCompactionArchives(frameID)
	if err != nil {
		return nil, err
	}
	archives := make([]CompatibilityCompactionArchive, 0, len(metadata))
	for _, item := range metadata {
		full, found, err := s.GetCompactionArchive(frameID, item.CompactionIndex)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("compaction archive %s/%d disappeared during export", frameID, item.CompactionIndex)
		}
		messages := full.Messages
		if messages == nil {
			messages = []map[string]any{}
		}
		archives = append(archives, CompatibilityCompactionArchive{
			CompactionIndex: full.CompactionIndex,
			MessageCount:    full.MessageCount,
			TokenCount:      full.TokenCount,
			Summary:         full.Summary,
			Messages:        messages,
		})
	}
	return archives, nil
}

func decodeCompatibilityExportJSON(value sql.NullString) (any, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" || strings.TrimSpace(value.String) == "null" {
		return nil, nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(value.String), &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func compatibilitySessionContextMetadata(contextData map[string]any) map[string]any {
	result := map[string]any{}
	for _, key := range []string{"_compaction_count", "_branch_meta", "_model", "_effort", "_thinking", "_tool_id_to_frame_id", "_rc_fork_log"} {
		if value, found := contextData[key]; found {
			result[strings.TrimPrefix(key, "_")] = value
		}
	}
	return result
}

func (s *Store) listCompatibilitySessionMessages(ctx context.Context, frameID string) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM frame_events WHERE frame_id = ? AND `+compatibilityMessagePredicate+` ORDER BY sequence`, frameID)
	if err != nil {
		return nil, fmt.Errorf("query compatibility session messages: %w", err)
	}
	defer rows.Close()
	messages := make([]map[string]any, 0)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan compatibility session message: %w", err)
		}
		var message map[string]any
		if err := json.Unmarshal([]byte(raw), &message); err != nil {
			return nil, fmt.Errorf("decode compatibility session message: %w", err)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate compatibility session messages: %w", err)
	}
	return messages, nil
}

func (s *Store) compatibilitySessionArtifacts(
	ctx context.Context, ownerUserID, projectID, rootFrameID string,
) ([]CompatibilitySessionArtifact, error) {
	listed, err := s.ListCompatibilityConversationArtifacts(ctx, ownerUserID, projectID, rootFrameID, false)
	if err != nil {
		return nil, err
	}
	result := make([]CompatibilitySessionArtifact, 0, len(listed))
	seenVersions := make(map[string]struct{}, len(listed))
	for _, artifact := range listed {
		created := artifact.CreatedAt.UTC().Format(time.RFC3339Nano)
		result = append(result, CompatibilitySessionArtifact{
			ArtifactID: artifact.ID, VersionID: artifact.VersionID, Filename: artifact.Filename,
			ContentType: artifact.ContentType, SizeBytes: artifact.SizeBytes, FrameID: artifact.FrameID,
			AgentName: artifact.AgentName, CreatedAt: &created,
		})
		seenVersions[artifact.VersionID] = struct{}{}
	}
	// Older Go data may predate artifact_runtime_metadata. Provenance is still
	// authoritative enough to include the current version in a session export.
	rows, err := s.db.QueryContext(ctx, `
		SELECT artifact.id, version.id, artifact.name,
			COALESCE(NULLIF(provenance.content_type, ''), artifact.kind),
			CASE WHEN COALESCE(version.storage_path, '') = '' THEN length(version.content) ELSE version.size_bytes END,
			provenance.frame_id, provenance.agent_name, version.created_at
		FROM artifact_versions version
		JOIN artifacts artifact ON artifact.id = version.artifact_id
		JOIN projects project ON project.id = artifact.project_id
		JOIN artifact_version_provenance provenance ON provenance.version_id = version.id
		JOIN frames frame ON frame.id = provenance.frame_id
		WHERE project.user_id = ? AND artifact.project_id = ? AND frame.root_frame_id = ?
		ORDER BY version.created_at DESC, artifact.id DESC`, ownerUserID, projectID, rootFrameID)
	if err != nil {
		return nil, fmt.Errorf("query provenance session artifacts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var artifact CompatibilitySessionArtifact
		var frameID, agentName sql.NullString
		var createdAt time.Time
		if err := rows.Scan(
			&artifact.ArtifactID, &artifact.VersionID, &artifact.Filename,
			&artifact.ContentType, &artifact.SizeBytes, &frameID, &agentName, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan provenance session artifact: %w", err)
		}
		if _, found := seenVersions[artifact.VersionID]; found {
			continue
		}
		artifact.FrameID = nullableStringPointer(frameID)
		artifact.AgentName = nullableStringPointer(agentName)
		created := createdAt.UTC().Format(time.RFC3339Nano)
		artifact.CreatedAt = &created
		result = append(result, artifact)
		seenVersions[artifact.VersionID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate provenance session artifacts: %w", err)
	}
	return result, nil
}
