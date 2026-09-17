package workspace

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"synon-go/internal/memorypolicy"
)

var memoryStalenessRoleMarker = regexp.MustCompile(`\[([A-Za-z]+)\]`)

type MemoryStaleness struct {
	Badge   string `json:"badge"`
	IsStale bool   `json:"is_stale"`
}

// MemoryStalenessFor reproduces the reference subject-aware badges. Missing map
// entries mean the row has no staleness note.
func (s *Store) MemoryStalenessFor(ctx context.Context, memories []Memory) (map[string]MemoryStaleness, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	result := make(map[string]MemoryStaleness)
	ownerID, err := memoryStalenessOwner(memories)
	if err != nil {
		return nil, err
	}
	if ownerID == "" {
		return result, nil
	}
	versionIDs := uniqueMemorySubjectIDs(memories, func(memory Memory) string { return memory.SubjectVersionID })
	artifactIDs := uniqueMemorySubjectIDs(memories, func(memory Memory) string {
		if memory.SubjectVersionID != "" {
			return ""
		}
		return memory.SubjectArtifactID
	})
	frameIDs := uniqueMemorySubjectIDs(memories, func(memory Memory) string { return memory.SubjectFrameID })

	versions := make(map[string]struct {
		filename string
		latestID string
	})
	if len(versionIDs) > 0 {
		args := append([]any{ownerID}, memoryStringArgs(versionIDs)...)
		rows, err := s.db.QueryContext(ctx, `SELECT learned.id, artifact.name, COALESCE(latest.id, '')
			FROM artifact_versions AS learned
			JOIN artifacts AS artifact ON artifact.id = learned.artifact_id
			JOIN projects AS project ON project.id = artifact.project_id
			LEFT JOIN artifact_versions AS latest
				ON latest.artifact_id = artifact.id AND latest.version_number = artifact.current_version_number
			WHERE project.user_id = ? AND learned.id IN (`+memorySQLQuestionMarks(len(versionIDs))+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("query memory subject versions: %w", err)
		}
		for rows.Next() {
			var id, filename, latestID string
			if err := rows.Scan(&id, &filename, &latestID); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan memory subject version: %w", err)
			}
			versions[id] = struct {
				filename string
				latestID string
			}{filename: filename, latestID: latestID}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate memory subject versions: %w", err)
		}
	}

	artifacts := make(map[string]struct {
		filename string
		latestID string
	})
	if len(artifactIDs) > 0 {
		args := append([]any{ownerID}, memoryStringArgs(artifactIDs)...)
		rows, err := s.db.QueryContext(ctx, `SELECT artifact.id, artifact.name, COALESCE(latest.id, '')
			FROM artifacts AS artifact
			JOIN projects AS project ON project.id = artifact.project_id
			LEFT JOIN artifact_versions AS latest
				ON latest.artifact_id = artifact.id AND latest.version_number = artifact.current_version_number
			WHERE project.user_id = ? AND artifact.id IN (`+memorySQLQuestionMarks(len(artifactIDs))+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("query memory subject artifacts: %w", err)
		}
		for rows.Next() {
			var id, filename, latestID string
			if err := rows.Scan(&id, &filename, &latestID); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan memory subject artifact: %w", err)
			}
			artifacts[id] = struct {
				filename string
				latestID string
			}{filename: filename, latestID: latestID}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate memory subject artifacts: %w", err)
		}
	}

	frames := make(map[string]struct {
		status    string
		updatedAt time.Time
	})
	if len(frameIDs) > 0 {
		args := append([]any{ownerID}, memoryStringArgs(frameIDs)...)
		rows, err := s.db.QueryContext(ctx, `SELECT frame.id, frame.status, frame.updated_at
			FROM frames AS frame JOIN projects AS project ON project.id = frame.project_id
			WHERE project.user_id = ? AND frame.id IN (`+memorySQLQuestionMarks(len(frameIDs))+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("query memory subject frames: %w", err)
		}
		for rows.Next() {
			var id, status string
			var updatedAt time.Time
			if err := rows.Scan(&id, &status, &updatedAt); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan memory subject frame: %w", err)
			}
			frames[id] = struct {
				status    string
				updatedAt time.Time
			}{status: status, updatedAt: updatedAt.UTC()}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate memory subject frames: %w", err)
		}
	}

	now := s.now().UTC()
	for _, memory := range memories {
		switch {
		case memory.SubjectVersionID != "":
			subject, exists := versions[memory.SubjectVersionID]
			if !exists {
				result[memory.ID] = MemoryStaleness{Badge: "⚠ subject version deleted", IsStale: true}
				continue
			}
			if subject.latestID != "" && subject.latestID != memory.SubjectVersionID {
				result[memory.ID] = MemoryStaleness{
					Badge:   fmt.Sprintf("⚠ %s now at %s (learned at %s)", sanitizeMemoryStalenessLabel(subject.filename), shortMemorySubjectID(subject.latestID), shortMemorySubjectID(memory.SubjectVersionID)),
					IsStale: true,
				}
			}
		case memory.SubjectArtifactID != "":
			subject, exists := artifacts[memory.SubjectArtifactID]
			if !exists {
				result[memory.ID] = MemoryStaleness{Badge: "⚠ subject artifact deleted", IsStale: true}
				continue
			}
			if subject.latestID != "" {
				result[memory.ID] = MemoryStaleness{Badge: fmt.Sprintf("%s @ %s", sanitizeMemoryStalenessLabel(subject.filename), shortMemorySubjectID(subject.latestID))}
			}
		case memory.SubjectFrameID != "":
			subject, exists := frames[memory.SubjectFrameID]
			if !exists {
				result[memory.ID] = MemoryStaleness{Badge: "⚠ subject session deleted", IsStale: true}
				continue
			}
			if terminalMemoryFrameStatus(subject.status) {
				days := int(now.Sub(subject.updatedAt).Hours() / 24)
				if days < 0 {
					days = 0
				}
				result[memory.ID] = MemoryStaleness{Badge: fmt.Sprintf("thread closed %dd ago", days), IsStale: true}
			}
		}
	}
	return result, nil
}

func uniqueMemorySubjectIDs(memories []Memory, get func(Memory) string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0)
	for _, memory := range memories {
		id := strings.TrimSpace(get(memory))
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

func memoryStalenessOwner(memories []Memory) (string, error) {
	ownerID := ""
	for _, memory := range memories {
		candidate := strings.TrimSpace(memory.UserID)
		if candidate == "" {
			return "", errors.New("memory staleness requires owned rows")
		}
		if ownerID == "" {
			ownerID = candidate
			continue
		}
		if candidate != ownerID {
			return "", errors.New("memory staleness rows must share one owner")
		}
	}
	return ownerID, nil
}

func memorySQLQuestionMarks(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func memoryStringArgs(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func shortMemorySubjectID(value string) string {
	if len(value) <= memorypolicy.StalenessIDPrefixMax {
		return value
	}
	return value[:memorypolicy.StalenessIDPrefixMax]
}

func sanitizeMemoryStalenessLabel(value string) string {
	value = strings.Map(func(r rune) rune {
		switch {
		case unicode.Is(unicode.Cf, r),
			r == '\u034f',
			r >= '\u180b' && r <= '\u180d',
			r >= '\ufe00' && r <= '\ufe0f',
			r >= '\U000e0100' && r <= '\U000e01ef':
			return -1
		case r == '<':
			return '‹'
		case r == '>':
			return '›'
		case r == '"':
			return '”'
		default:
			return r
		}
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	value = memoryStalenessRoleMarker.ReplaceAllString(value, "[$1 ]")
	return memorypolicy.TruncateWithEllipsis(value, memorypolicy.TextMaxUTF16Units)
}

func terminalMemoryFrameStatus(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case FrameStatusCompleted, FrameStatusFailed, FrameStatusCancelled, FrameStatusSuccess, FrameStatusReplaced:
		return true
	default:
		return false
	}
}
