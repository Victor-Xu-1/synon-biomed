package workspace

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	maxTokenClassJSONBytes = 1 << 20
	maxTokenClasses        = 1024
)

type TokenClassUsage struct {
	Input      int64   `json:"input"`
	Output     int64   `json:"output"`
	CacheRead  int64   `json:"cache_read"`
	CacheWrite int64   `json:"cache_write"`
	Cost       float64 `json:"cost"`
	Credits    float64 `json:"credits"`
}

type TokenTotals struct {
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	TotalCost        float64 `json:"total_cost"`
	AuxCost          float64 `json:"aux_cost"`
}

type TokenClassSession struct {
	RootFrameID      string                     `json:"root_frame_id"`
	Classes          map[string]TokenClassUsage `json:"classes"`
	Unattributed     TokenClassUsage            `json:"unattributed"`
	Totals           TokenTotals                `json:"totals"`
	AttributedFrames int                        `json:"attributed_frames"`
	TotalFrames      int                        `json:"total_frames"`
	LastActivityAt   int64                      `json:"last_activity_at,omitempty"`
}

func (s *Store) TokenClassBreakdownForRoot(rootFrameID, ownerUserID string) (TokenClassSession, bool, error) {
	if s == nil || s.db == nil {
		return TokenClassSession{}, false, errors.New("workspace store is closed")
	}
	rootFrameID = strings.TrimSpace(rootFrameID)
	ownerUserID = strings.TrimSpace(ownerUserID)
	if rootFrameID == "" || ownerUserID == "" {
		return TokenClassSession{}, false, errors.New("root frame id and owner user id are required")
	}
	rows, err := s.db.QueryContext(context.Background(), tokenClassRowsSQL+`
		WHERE p.user_id = ? AND f.root_frame_id = ? ORDER BY f.updated_at, f.id`, ownerUserID, rootFrameID)
	if err != nil {
		return TokenClassSession{}, false, fmt.Errorf("query root token classes: %w", err)
	}
	defer rows.Close()
	session, found, err := aggregateTokenClassRows(rows)
	if err != nil || !found {
		return TokenClassSession{}, found, err
	}
	return session[0], true, nil
}

func (s *Store) TokenClassBreakdownForWindow(ownerUserID string, since time.Time) ([]TokenClassSession, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	ownerUserID = strings.TrimSpace(ownerUserID)
	if ownerUserID == "" || since.IsZero() {
		return nil, errors.New("owner user id and window start are required")
	}
	rows, err := s.db.QueryContext(context.Background(), tokenClassRowsSQL+`
		JOIN frames root ON root.id = f.root_frame_id
		LEFT JOIN frame_runtime_metadata root_metadata ON root_metadata.frame_id = root.id
		WHERE p.user_id = ? AND COALESCE(root_metadata.is_hidden, 0) = 0 AND EXISTS (
			SELECT 1 FROM frames recent
			LEFT JOIN frame_runtime_metadata recent_metadata ON recent_metadata.frame_id = recent.id
			WHERE recent.root_frame_id = f.root_frame_id AND (
				recent_metadata.completed_at >= ? OR
				(recent_metadata.completed_at IS NULL AND recent.updated_at >= ?) OR
				(lower(recent.status) NOT IN ('completed', 'failed', 'cancelled', 'success', 'replaced') AND recent.updated_at >= ?)
			)
		) ORDER BY f.root_frame_id, f.updated_at, f.id`, ownerUserID, since.UTC(), since.UTC(), since.UTC())
	if err != nil {
		return nil, fmt.Errorf("query token class window: %w", err)
	}
	defer rows.Close()
	sessions, _, err := aggregateTokenClassRows(rows)
	return sessions, err
}

const tokenClassRowsSQL = `
	SELECT f.root_frame_id, f.updated_at, m.completed_at, f.status,
		COALESCE(m.input_tokens, 0) + COALESCE(m.aux_input_tokens, 0),
		COALESCE(m.output_tokens, 0) + COALESCE(m.aux_output_tokens, 0),
		COALESCE(m.cache_read_tokens, 0) + COALESCE(m.aux_cache_read_tokens, 0),
		COALESCE(m.cache_write_tokens, 0) + COALESCE(m.aux_cache_write_tokens, 0),
		COALESCE(m.total_cost, 0) + COALESCE(m.aux_cost, 0),
		COALESCE(m.aux_cost, 0),
		m.token_class_usage
	FROM frames f JOIN projects p ON p.id = f.project_id
	LEFT JOIN frame_runtime_metadata m ON m.frame_id = f.id
`

func aggregateTokenClassRows(rows *sql.Rows) ([]TokenClassSession, bool, error) {
	byRoot := make(map[string]*TokenClassSession)
	order := make([]string, 0)
	for rows.Next() {
		var rootID string
		var updated time.Time
		var completed sql.NullTime
		var status string
		var totals TokenTotals
		var raw sql.NullString
		if err := rows.Scan(&rootID, &updated, &completed, &status, &totals.InputTokens, &totals.OutputTokens, &totals.CacheReadTokens, &totals.CacheWriteTokens, &totals.TotalCost, &totals.AuxCost, &raw); err != nil {
			return nil, false, fmt.Errorf("scan token class row: %w", err)
		}
		session := byRoot[rootID]
		if session == nil {
			session = &TokenClassSession{RootFrameID: rootID, Classes: map[string]TokenClassUsage{}}
			byRoot[rootID] = session
			order = append(order, rootID)
		}
		if session.TotalFrames == math.MaxInt {
			return nil, false, errors.New("token class frame count overflow")
		}
		session.TotalFrames++
		var err error
		if session.Totals.InputTokens, err = checkedAddTokenInt64(session.Totals.InputTokens, nonnegativeInt64(totals.InputTokens)); err != nil {
			return nil, false, err
		}
		if session.Totals.OutputTokens, err = checkedAddTokenInt64(session.Totals.OutputTokens, nonnegativeInt64(totals.OutputTokens)); err != nil {
			return nil, false, err
		}
		if session.Totals.CacheReadTokens, err = checkedAddTokenInt64(session.Totals.CacheReadTokens, nonnegativeInt64(totals.CacheReadTokens)); err != nil {
			return nil, false, err
		}
		if session.Totals.CacheWriteTokens, err = checkedAddTokenInt64(session.Totals.CacheWriteTokens, nonnegativeInt64(totals.CacheWriteTokens)); err != nil {
			return nil, false, err
		}
		if totals.TotalCost > 0 && !math.IsNaN(totals.TotalCost) && !math.IsInf(totals.TotalCost, 0) {
			session.Totals.TotalCost, err = checkedAddTokenFloat64(session.Totals.TotalCost, totals.TotalCost)
			if err != nil {
				return nil, false, err
			}
		}
		if totals.AuxCost > 0 && !math.IsNaN(totals.AuxCost) && !math.IsInf(totals.AuxCost, 0) {
			session.Totals.AuxCost, err = checkedAddTokenFloat64(session.Totals.AuxCost, totals.AuxCost)
			if err != nil {
				return nil, false, err
			}
		}
		activity := updated
		if completed.Valid && terminalTokenFrameStatus(status) {
			activity = completed.Time
		}
		updatedMilliseconds := activity.UTC().UnixMilli()
		if updatedMilliseconds > session.LastActivityAt {
			session.LastActivityAt = updatedMilliseconds
		}
		if raw.Valid && strings.TrimSpace(raw.String) != "" {
			if session.AttributedFrames == math.MaxInt {
				return nil, false, errors.New("attributed token frame count overflow")
			}
			session.AttributedFrames++
			classes, err := decodeTokenClassUsage(raw.String)
			if err != nil {
				return nil, false, fmt.Errorf("decode token classes for root %s: %w", rootID, err)
			}
			for name, usage := range classes {
				current := session.Classes[name]
				if _, found := session.Classes[name]; !found && len(session.Classes) >= maxTokenClasses {
					return nil, false, fmt.Errorf("root %s exceeds %d token classes", rootID, maxTokenClasses)
				}
				if err := addTokenUsage(&current, usage); err != nil {
					return nil, false, fmt.Errorf("aggregate token class %s: %w", name, err)
				}
				session.Classes[name] = current
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate token class rows: %w", err)
	}
	result := make([]TokenClassSession, 0, len(order))
	for _, rootID := range order {
		session := byRoot[rootID]
		var err error
		session.Unattributed, err = unattributedTokenUsage(*session)
		if err != nil {
			return nil, false, fmt.Errorf("aggregate unattributed tokens for root %s: %w", rootID, err)
		}
		result = append(result, *session)
	}
	return result, len(result) > 0, nil
}

func decodeTokenClassUsage(raw string) (map[string]TokenClassUsage, error) {
	if len(raw) > maxTokenClassJSONBytes {
		return nil, errors.New("token class JSON exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.UseNumber()
	values := map[string]map[string]any{}
	if err := decoder.Decode(&values); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("token class JSON contains multiple values")
		}
		return nil, fmt.Errorf("decode trailing token class JSON: %w", err)
	}
	if len(values) > maxTokenClasses {
		return nil, fmt.Errorf("token class JSON exceeds %d classes", maxTokenClasses)
	}
	classes := make(map[string]TokenClassUsage, len(values))
	for rawName, fields := range values {
		name := strings.TrimSpace(rawName)
		if name == "" || len(name) > 128 {
			return nil, errors.New("invalid token class name")
		}
		usage := TokenClassUsage{}
		var err error
		if usage.Input, err = tokenInt64(fields["input"]); err != nil {
			return nil, fmt.Errorf("%s.input: %w", name, err)
		}
		if usage.Output, err = tokenInt64(fields["output"]); err != nil {
			return nil, fmt.Errorf("%s.output: %w", name, err)
		}
		if usage.CacheRead, err = tokenInt64(fields["cache_read"]); err != nil {
			return nil, fmt.Errorf("%s.cache_read: %w", name, err)
		}
		if usage.CacheWrite, err = tokenInt64(fields["cache_write"]); err != nil {
			return nil, fmt.Errorf("%s.cache_write: %w", name, err)
		}
		if usage.Cost, err = tokenFloat64(fields["cost"]); err != nil {
			return nil, fmt.Errorf("%s.cost: %w", name, err)
		}
		if usage.Credits, err = tokenFloat64(fields["credits"]); err != nil {
			return nil, fmt.Errorf("%s.credits: %w", name, err)
		}
		if usage.Credits <= 0 {
			usage.Credits = fallbackTokenCredits(usage)
		}
		classes[name] = usage
	}
	return classes, nil
}

func tokenInt64(value any) (int64, error) {
	if value == nil {
		return 0, nil
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, errors.New("must be a number")
	}
	parsed, err := strconv.ParseInt(string(number), 10, 64)
	if err != nil || parsed < 0 {
		return 0, errors.New("must be a non-negative integer")
	}
	return parsed, nil
}
func tokenFloat64(value any) (float64, error) {
	if value == nil {
		return 0, nil
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, errors.New("must be a number")
	}
	parsed, err := strconv.ParseFloat(string(number), 64)
	if err != nil || parsed < 0 || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, errors.New("must be a finite non-negative number")
	}
	return parsed, nil
}
func addTokenUsage(target *TokenClassUsage, value TokenClassUsage) error {
	var err error
	if target.Input, err = checkedAddTokenInt64(target.Input, value.Input); err != nil {
		return err
	}
	if target.Output, err = checkedAddTokenInt64(target.Output, value.Output); err != nil {
		return err
	}
	if target.CacheRead, err = checkedAddTokenInt64(target.CacheRead, value.CacheRead); err != nil {
		return err
	}
	if target.CacheWrite, err = checkedAddTokenInt64(target.CacheWrite, value.CacheWrite); err != nil {
		return err
	}
	if target.Cost, err = checkedAddTokenFloat64(target.Cost, value.Cost); err != nil {
		return err
	}
	target.Credits, err = checkedAddTokenFloat64(target.Credits, value.Credits)
	return err
}
func unattributedTokenUsage(session TokenClassSession) (TokenClassUsage, error) {
	attributed := TokenClassUsage{}
	for _, usage := range session.Classes {
		if err := addTokenUsage(&attributed, usage); err != nil {
			return TokenClassUsage{}, err
		}
	}
	return TokenClassUsage{Input: positiveDifference(session.Totals.InputTokens, attributed.Input), Output: positiveDifference(session.Totals.OutputTokens, attributed.Output), CacheRead: positiveDifference(session.Totals.CacheReadTokens, attributed.CacheRead), CacheWrite: positiveDifference(session.Totals.CacheWriteTokens, attributed.CacheWrite), Cost: positiveFloatDifference(session.Totals.TotalCost, attributed.Cost)}, nil
}
func positiveDifference(total, part int64) int64 {
	if total > part {
		return total - part
	}
	return 0
}
func positiveFloatDifference(total, part float64) float64 {
	if total > part {
		return total - part
	}
	return 0
}
func nonnegativeInt64(value int64) int64 {
	if value > 0 {
		return value
	}
	return 0
}

func checkedAddTokenInt64(left, right int64) (int64, error) {
	if left < 0 || right < 0 || right > math.MaxInt64-left {
		return 0, errors.New("token count overflow")
	}
	return left + right, nil
}

func checkedAddTokenFloat64(left, right float64) (float64, error) {
	sum := left + right
	if left < 0 || right < 0 || math.IsNaN(sum) || math.IsInf(sum, 0) {
		return 0, errors.New("token cost overflow")
	}
	return sum, nil
}

func fallbackTokenCredits(usage TokenClassUsage) float64 {
	weight := float64(usage.Input) - float64(usage.CacheRead) + float64(usage.Output)*5
	if weight <= 0 {
		return 0
	}
	return weight / 250
}

func terminalTokenFrameStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "cancelled", "success", "replaced":
		return true
	default:
		return false
	}
}
