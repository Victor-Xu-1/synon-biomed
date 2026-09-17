package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	maxVerificationRecords    = 4096
	maxVerificationJSONBytes  = 1 << 20
	maxVerificationIDBytes    = 256
	maxVerificationTextBytes  = 64 << 10
	maxVerificationModelBytes = 512
)

type VerificationCheck struct {
	ID                string    `json:"id"`
	RootFrameID       string    `json:"root_frame_id"`
	ArtifactVersionID *string   `json:"artifact_version_id"`
	ClaimID           *string   `json:"claim_id"`
	Claim             *string   `json:"claim"`
	Verdict           string    `json:"verdict"`
	Severity          *string   `json:"severity"`
	Evidence          *string   `json:"evidence"`
	Rebuttal          *string   `json:"rebuttal"`
	ReviewerIndex     *int      `json:"reviewer_idx"`
	ReviewerModel     *string   `json:"reviewer_model"`
	ReviewerFrameID   *string   `json:"reviewer_frame_id"`
	SourceRef         any       `json:"source_ref"`
	Status            string    `json:"status"`
	ReflagCount       *int      `json:"reflag_count"`
	CreatedAt         time.Time `json:"created_at"`
}

type SessionClaim struct {
	ID          string    `json:"id"`
	RootFrameID string    `json:"root_frame_id"`
	FrameID     string    `json:"frame_id"`
	StepID      *string   `json:"step_id"`
	ClaimText   string    `json:"claim_text"`
	Entities    any       `json:"entities"`
	Source      string    `json:"source"`
	CreatedAt   time.Time `json:"created_at"`
}

type RunningVerification struct {
	FrameID       string    `json:"frame_id"`
	TargetFrameID *string   `json:"target_frame_id"`
	Model         *string   `json:"model"`
	CreatedAt     time.Time `json:"created_at"`
}

type VerificationSnapshot struct {
	Checks *[]VerificationCheck
	Claims *[]SessionClaim
}

func (s *Store) ListVerificationChecks(rootFrameID, status string) ([]VerificationCheck, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	rootFrameID = strings.TrimSpace(rootFrameID)
	status = strings.TrimSpace(strings.ToLower(status))
	if rootFrameID == "" {
		return nil, errors.New("root frame id is required")
	}
	if status != "" && status != "open" && status != "resolved" && status != "unaddressed" {
		return nil, errors.New("verification status must be open, resolved, or unaddressed")
	}
	query := `SELECT id, root_frame_id, artifact_version_id, claim_id, claim, verdict,
		severity, evidence, rebuttal, reviewer_idx, reviewer_model, reviewer_frame_id,
		source_ref, status, reflag_count, created_at
		FROM verification_checks WHERE root_frame_id = ?`
	args := []any{rootFrameID}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at, id`
	rows, err := s.db.QueryContext(context.Background(), query, args...)
	if err != nil {
		return nil, fmt.Errorf("list verification checks: %w", err)
	}
	defer rows.Close()
	checks := make([]VerificationCheck, 0)
	for rows.Next() {
		check, scanErr := scanVerificationCheck(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		checks = append(checks, check)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate verification checks: %w", err)
	}
	return checks, nil
}

// ListReviewerVerificationChecks returns only the checks emitted by one
// reviewer frame. Runtime status polling uses this bounded query so a long-lived
// task with a large review history does not repeatedly scan every prior check.
func (s *Store) ListReviewerVerificationChecks(rootFrameID, reviewerFrameID string) ([]VerificationCheck, error) {
	db := s.readDatabase()
	if db == nil {
		return nil, errors.New("workspace store is closed")
	}
	rootFrameID = strings.TrimSpace(rootFrameID)
	reviewerFrameID = strings.TrimSpace(reviewerFrameID)
	if rootFrameID == "" || reviewerFrameID == "" {
		return nil, errors.New("root frame id and reviewer frame id are required")
	}
	rows, err := db.QueryContext(context.Background(), `
		SELECT id, root_frame_id, artifact_version_id, claim_id, claim, verdict,
			severity, evidence, rebuttal, reviewer_idx, reviewer_model, reviewer_frame_id,
			source_ref, status, reflag_count, created_at
		FROM verification_checks
		WHERE root_frame_id = ? AND reviewer_frame_id = ?
		ORDER BY created_at, id`, rootFrameID, reviewerFrameID)
	if err != nil {
		return nil, fmt.Errorf("list reviewer verification checks: %w", err)
	}
	defer rows.Close()
	checks := make([]VerificationCheck, 0)
	for rows.Next() {
		check, scanErr := scanVerificationCheck(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		checks = append(checks, check)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate reviewer verification checks: %w", err)
	}
	return checks, nil
}

func (s *Store) ListArtifactVerificationChecks(versionID, ownerUserID string) ([]VerificationCheck, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, errors.New("workspace store is closed")
	}
	versionID = strings.TrimSpace(versionID)
	ownerUserID = strings.TrimSpace(ownerUserID)
	if versionID == "" || ownerUserID == "" {
		return nil, false, errors.New("version id and owner user id are required")
	}
	var found string
	err := s.db.QueryRowContext(context.Background(), `
		SELECT v.id FROM artifact_versions v
		JOIN artifacts a ON a.id = v.artifact_id
		JOIN projects p ON p.id = a.project_id
		WHERE v.id = ? AND p.user_id = ?`, versionID, ownerUserID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return []VerificationCheck{}, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("look up artifact verification owner: %w", err)
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, root_frame_id, artifact_version_id, claim_id, claim, verdict,
			severity, evidence, rebuttal, reviewer_idx, reviewer_model, reviewer_frame_id,
			source_ref, status, reflag_count, created_at
		FROM verification_checks WHERE artifact_version_id = ? ORDER BY created_at, id`, versionID)
	if err != nil {
		return nil, false, fmt.Errorf("list artifact verification checks: %w", err)
	}
	defer rows.Close()
	checks := make([]VerificationCheck, 0)
	for rows.Next() {
		check, err := scanVerificationCheck(rows)
		if err != nil {
			return nil, false, err
		}
		checks = append(checks, check)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate artifact verification checks: %w", err)
	}
	return checks, true, nil
}

func (s *Store) ListSessionClaims(rootFrameID string) ([]SessionClaim, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	rootFrameID = strings.TrimSpace(rootFrameID)
	if rootFrameID == "" {
		return nil, errors.New("root frame id is required")
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, root_frame_id, frame_id, step_id, claim_text, entities, source, created_at
		FROM session_claims WHERE root_frame_id = ? ORDER BY created_at, id`, rootFrameID)
	if err != nil {
		return nil, fmt.Errorf("list session claims: %w", err)
	}
	defer rows.Close()
	claims := make([]SessionClaim, 0)
	for rows.Next() {
		var claim SessionClaim
		var stepID, entities sql.NullString
		if err := rows.Scan(&claim.ID, &claim.RootFrameID, &claim.FrameID, &stepID, &claim.ClaimText, &entities, &claim.Source, &claim.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan session claim: %w", err)
		}
		claim.StepID = nullableStringPointer(stepID)
		if entities.Valid {
			if err := json.Unmarshal([]byte(entities.String), &claim.Entities); err != nil {
				return nil, fmt.Errorf("decode session claim entities: %w", err)
			}
		}
		claims = append(claims, claim)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate session claims: %w", err)
	}
	return claims, nil
}

func (s *Store) ListRunningVerification(rootFrameID string) ([]RunningVerification, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	rootFrameID = strings.TrimSpace(rootFrameID)
	if rootFrameID == "" {
		return nil, errors.New("root frame id is required")
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT f.id, COALESCE(json_extract(m.input_data, '$._review_target_frame_id'), f.parent_frame_id),
			m.model, f.created_at
		FROM frames f LEFT JOIN frame_runtime_metadata m ON m.frame_id = f.id
		WHERE f.root_frame_id = ?
			AND (
				upper(f.agent_name) IN ('REVIEWER', 'AUDITOR')
				OR (
					COALESCE(m.is_hidden, 0) = 1
					AND lower(COALESCE(json_extract(m.input_data, '$.kind'), '')) IN ('runner_completion_review', 'runner_scientific_review')
				)
			)
			AND lower(f.status) IN ('processing', 'created')
		ORDER BY f.created_at, f.id`, rootFrameID)
	if err != nil {
		return nil, fmt.Errorf("list running verification: %w", err)
	}
	defer rows.Close()
	running := make([]RunningVerification, 0)
	for rows.Next() {
		var item RunningVerification
		var target, model sql.NullString
		if err := rows.Scan(&item.FrameID, &target, &model, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan running verification: %w", err)
		}
		item.TargetFrameID = nullableStringPointer(target)
		item.Model = nullableStringPointer(model)
		running = append(running, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate running verification: %w", err)
	}
	return running, nil
}

func scanVerificationCheck(scanner interface{ Scan(...any) error }) (VerificationCheck, error) {
	var check VerificationCheck
	var artifactVersionID, claimID, claim, severity, evidence, rebuttal sql.NullString
	var reviewerIndex, reflagCount sql.NullInt64
	var reviewerModel, reviewerFrameID sql.NullString
	var rawSourceRef string
	if err := scanner.Scan(&check.ID, &check.RootFrameID, &artifactVersionID, &claimID, &claim, &check.Verdict,
		&severity, &evidence, &rebuttal, &reviewerIndex, &reviewerModel, &reviewerFrameID,
		&rawSourceRef, &check.Status, &reflagCount, &check.CreatedAt); err != nil {
		return VerificationCheck{}, fmt.Errorf("scan verification check: %w", err)
	}
	check.ArtifactVersionID = nullableStringPointer(artifactVersionID)
	check.ClaimID = nullableStringPointer(claimID)
	check.Claim = nullableStringPointer(claim)
	check.Severity = nullableStringPointer(severity)
	check.Evidence = nullableStringPointer(evidence)
	check.Rebuttal = nullableStringPointer(rebuttal)
	check.ReviewerModel = nullableStringPointer(reviewerModel)
	check.ReviewerFrameID = nullableStringPointer(reviewerFrameID)
	if reviewerIndex.Valid {
		value := int(reviewerIndex.Int64)
		check.ReviewerIndex = &value
	}
	if reflagCount.Valid {
		value := int(reflagCount.Int64)
		check.ReflagCount = &value
	}
	if err := json.Unmarshal([]byte(rawSourceRef), &check.SourceRef); err != nil {
		return VerificationCheck{}, fmt.Errorf("decode verification source_ref: %w", err)
	}
	return check, nil
}

func (s *Store) ReplaceVerificationSnapshot(rootFrameID string, snapshot VerificationSnapshot) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	rootFrameID = strings.TrimSpace(rootFrameID)
	if rootFrameID == "" {
		return errors.New("root frame id is required")
	}
	if snapshot.Checks == nil && snapshot.Claims == nil {
		return nil
	}
	if snapshot.Checks != nil && len(*snapshot.Checks) > maxVerificationRecords {
		return fmt.Errorf("verification checks exceed %d records", maxVerificationRecords)
	}
	if snapshot.Claims != nil && len(*snapshot.Claims) > maxVerificationRecords {
		return fmt.Errorf("session claims exceed %d records", maxVerificationRecords)
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin verification snapshot: %w", err)
	}
	defer tx.Rollback()
	var rootID string
	if err := tx.QueryRowContext(ctx, `SELECT root_frame_id FROM frames WHERE id = ?`, rootFrameID).Scan(&rootID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("frame %s not found", rootFrameID)
		}
		return fmt.Errorf("look up verification root: %w", err)
	}
	if rootID != rootFrameID {
		return errors.New("verification snapshots must target a root frame")
	}
	if snapshot.Checks != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM verification_checks WHERE root_frame_id = ?`, rootFrameID); err != nil {
			return fmt.Errorf("clear verification checks: %w", err)
		}
	}
	if snapshot.Claims != nil {
		if snapshot.Checks == nil {
			if _, err := tx.ExecContext(ctx, `UPDATE verification_checks SET claim_id = NULL WHERE root_frame_id = ?`, rootFrameID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM session_claims WHERE root_frame_id = ?`, rootFrameID); err != nil {
			return fmt.Errorf("clear session claims: %w", err)
		}
		for index := range *snapshot.Claims {
			if err := insertSessionClaim(ctx, tx, rootFrameID, &(*snapshot.Claims)[index], s.now().UTC()); err != nil {
				return err
			}
		}
	}
	if snapshot.Checks != nil {
		for index := range *snapshot.Checks {
			if err := insertVerificationCheck(ctx, tx, rootFrameID, &(*snapshot.Checks)[index], s.now().UTC()); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit verification snapshot: %w", err)
	}
	return nil
}

func insertSessionClaim(ctx context.Context, tx *sql.Tx, rootFrameID string, claim *SessionClaim, fallback time.Time) error {
	var err error
	claim.ID, err = normalizeVerificationText(claim.ID, "claim id", maxVerificationIDBytes, false)
	if err != nil {
		return err
	}
	if claim.ID == "" {
		claim.ID = uuid.NewString()
	}
	claim.FrameID, err = normalizeVerificationText(claim.FrameID, "claim frame id", maxVerificationIDBytes, true)
	if err != nil {
		return err
	}
	claim.StepID, err = normalizeVerificationTextPointer(claim.StepID, "claim step id", maxVerificationIDBytes)
	if err != nil {
		return err
	}
	claim.ClaimText, err = normalizeVerificationText(claim.ClaimText, "claim text", maxVerificationTextBytes, true)
	if err != nil {
		return err
	}
	claim.Source, err = normalizeVerificationText(strings.ToLower(claim.Source), "claim source", 32, true)
	if err != nil {
		return err
	}
	if claim.Source != "agent" && claim.Source != "llm_extracted" {
		return errors.New("invalid session claim")
	}
	var owned string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM frames WHERE id = ? AND root_frame_id = ?`, claim.FrameID, rootFrameID).Scan(&owned); err != nil {
		return fmt.Errorf("session claim frame is outside the verification root")
	}
	entities, err := marshalBoundedVerificationJSON(claim.Entities)
	if err != nil {
		return err
	}
	if claim.CreatedAt.IsZero() {
		claim.CreatedAt = fallback
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO session_claims (id, root_frame_id, frame_id, step_id, claim_text, entities, source, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		claim.ID, rootFrameID, claim.FrameID, nullableTrimmedPointer(claim.StepID), claim.ClaimText, nullableJSON(entities, claim.Entities), claim.Source, claim.CreatedAt.UTC())
	if err != nil {
		return fmt.Errorf("insert session claim: %w", err)
	}
	claim.RootFrameID = rootFrameID
	return nil
}

func insertVerificationCheck(ctx context.Context, tx *sql.Tx, rootFrameID string, check *VerificationCheck, fallback time.Time) error {
	var err error
	check.ID, err = normalizeVerificationText(check.ID, "verification check id", maxVerificationIDBytes, false)
	if err != nil {
		return err
	}
	if check.ID == "" {
		check.ID = uuid.NewString()
	}
	check.Verdict, err = normalizeVerificationText(strings.ToLower(check.Verdict), "verification verdict", 32, true)
	if err != nil {
		return err
	}
	check.Status, err = normalizeVerificationText(strings.ToLower(check.Status), "verification status", 32, false)
	if err != nil {
		return err
	}
	if check.Status == "" {
		check.Status = "open"
	}
	if !stringInSet(check.Verdict, "pass", "warn", "fail", "inconclusive") || !stringInSet(check.Status, "open", "resolved", "unaddressed") {
		return errors.New("invalid verification check verdict or status")
	}
	if (check.ReviewerIndex != nil && *check.ReviewerIndex < 0) || (check.ReflagCount != nil && *check.ReflagCount < 0) {
		return errors.New("verification indexes must not be negative")
	}
	check.ArtifactVersionID, err = normalizeVerificationTextPointer(check.ArtifactVersionID, "artifact version id", maxVerificationIDBytes)
	if err != nil {
		return err
	}
	check.ClaimID, err = normalizeVerificationTextPointer(check.ClaimID, "verification claim id", maxVerificationIDBytes)
	if err != nil {
		return err
	}
	check.ReviewerFrameID, err = normalizeVerificationTextPointer(check.ReviewerFrameID, "reviewer frame id", maxVerificationIDBytes)
	if err != nil {
		return err
	}
	check.Claim, err = normalizeVerificationTextPointer(check.Claim, "verification claim", maxVerificationTextBytes)
	if err != nil {
		return err
	}
	check.Severity, err = normalizeVerificationTextPointer(check.Severity, "verification severity", 64)
	if err != nil {
		return err
	}
	check.Evidence, err = normalizeVerificationTextPointer(check.Evidence, "verification evidence", maxVerificationTextBytes)
	if err != nil {
		return err
	}
	check.Rebuttal, err = normalizeVerificationTextPointer(check.Rebuttal, "verification rebuttal", maxVerificationTextBytes)
	if err != nil {
		return err
	}
	check.ReviewerModel, err = normalizeVerificationTextPointer(check.ReviewerModel, "reviewer model", maxVerificationModelBytes)
	if err != nil {
		return err
	}
	if check.ArtifactVersionID != nil {
		var owned string
		err := tx.QueryRowContext(ctx, `SELECT v.id FROM artifact_versions v JOIN artifacts a ON a.id=v.artifact_id JOIN frames f ON f.project_id=a.project_id WHERE v.id=? AND f.id=?`, *check.ArtifactVersionID, rootFrameID).Scan(&owned)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("verification artifact version is outside the root project")
		}
		if err != nil {
			return fmt.Errorf("validate verification artifact version: %w", err)
		}
	}
	if check.ClaimID != nil {
		var owned string
		err := tx.QueryRowContext(ctx, `SELECT id FROM session_claims WHERE id = ? AND root_frame_id = ?`, *check.ClaimID, rootFrameID).Scan(&owned)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("verification claim is outside the verification root")
		}
		if err != nil {
			return fmt.Errorf("validate verification claim: %w", err)
		}
	}
	if check.ReviewerFrameID != nil {
		var owned string
		err := tx.QueryRowContext(ctx, `SELECT id FROM frames WHERE id = ? AND root_frame_id = ?`, *check.ReviewerFrameID, rootFrameID).Scan(&owned)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("reviewer frame is outside the verification root")
		}
		if err != nil {
			return fmt.Errorf("validate reviewer frame: %w", err)
		}
	}
	sourceRef, err := marshalBoundedVerificationJSON(check.SourceRef)
	if err != nil {
		return err
	}
	if check.SourceRef == nil {
		sourceRef = []byte("{}")
	}
	if check.CreatedAt.IsZero() {
		check.CreatedAt = fallback
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO verification_checks (id, root_frame_id, artifact_version_id, claim_id, claim, verdict, severity, evidence, rebuttal, reviewer_idx, reviewer_model, reviewer_frame_id, source_ref, status, reflag_count, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		check.ID, rootFrameID, nullableTrimmedPointer(check.ArtifactVersionID), nullableTrimmedPointer(check.ClaimID), nullableTrimmedPointer(check.Claim), check.Verdict, nullableTrimmedPointer(check.Severity), nullableTrimmedPointer(check.Evidence), nullableTrimmedPointer(check.Rebuttal), check.ReviewerIndex, nullableTrimmedPointer(check.ReviewerModel), nullableTrimmedPointer(check.ReviewerFrameID), string(sourceRef), check.Status, check.ReflagCount, check.CreatedAt.UTC())
	if err != nil {
		return fmt.Errorf("insert verification check: %w", err)
	}
	check.RootFrameID = rootFrameID
	return nil
}

func marshalBoundedVerificationJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode verification JSON: %w", err)
	}
	if len(raw) > maxVerificationJSONBytes {
		return nil, errors.New("verification JSON exceeds 1 MiB")
	}
	return raw, nil
}
func nullableTrimmedPointer(value *string) any {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	return strings.TrimSpace(*value)
}
func nullableJSON(raw []byte, value any) any {
	if value == nil {
		return nil
	}
	return string(raw)
}
func stringInSet(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}

func normalizeVerificationText(value, field string, limit int, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", fmt.Errorf("%s is required", field)
	}
	if len(value) > limit {
		return "", fmt.Errorf("%s exceeds %d bytes", field, limit)
	}
	return value, nil
}

func normalizeVerificationTextPointer(value *string, field string, limit int) (*string, error) {
	if value == nil {
		return nil, nil
	}
	normalized, err := normalizeVerificationText(*value, field, limit, false)
	if err != nil {
		return nil, err
	}
	if normalized == "" {
		return nil, nil
	}
	return &normalized, nil
}
