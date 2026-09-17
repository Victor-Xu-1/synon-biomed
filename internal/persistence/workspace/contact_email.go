package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

const (
	ContactEmailDecisionAllowed  = "allowed"
	ContactEmailDecisionDeclined = "declined"
	ContactEmailDecisionRevoked  = "revoked"
)

type ContactEmailDecision struct {
	ID            string
	UserID        string
	Decision      string
	Email         string
	NoticeVersion string
	NoticeText    string
	CreatedAt     time.Time
}

type RecordContactEmailDecisionInput struct {
	UserID        string
	Decision      string
	Email         string
	NoticeVersion string
	NoticeText    string
}

func (s *Store) LatestContactEmailDecision(userID string) (ContactEmailDecision, bool, error) {
	if s == nil || s.db == nil {
		return ContactEmailDecision{}, false, errors.New("workspace store is closed")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ContactEmailDecision{}, false, errors.New("contact email user id is required")
	}
	var decision ContactEmailDecision
	var email sql.NullString
	err := s.db.QueryRowContext(context.Background(), `
		SELECT id, user_id, decision, email, notice_version, notice_text, created_at
		FROM contact_email_decisions
		WHERE user_id = ?
		ORDER BY created_at DESC, rowid DESC
		LIMIT 1`, userID).Scan(
		&decision.ID, &decision.UserID, &decision.Decision, &email,
		&decision.NoticeVersion, &decision.NoticeText, &decision.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ContactEmailDecision{}, false, nil
	}
	if err != nil {
		return ContactEmailDecision{}, false, fmt.Errorf("load latest contact email decision: %w", err)
	}
	if email.Valid {
		decision.Email = email.String
	}
	return decision, true, nil
}

func (s *Store) RecordContactEmailDecision(input RecordContactEmailDecisionInput) (ContactEmailDecision, error) {
	if s == nil || s.db == nil {
		return ContactEmailDecision{}, errors.New("workspace store is closed")
	}
	input.UserID = strings.TrimSpace(input.UserID)
	input.Email = strings.TrimSpace(input.Email)
	input.NoticeVersion = strings.TrimSpace(input.NoticeVersion)
	if err := validateContactEmailDecisionInput(input); err != nil {
		return ContactEmailDecision{}, err
	}
	if input.Decision != ContactEmailDecisionAllowed {
		input.Email = ""
	}

	now := s.now().UTC()
	decision := ContactEmailDecision{
		ID: uuid.NewString(), UserID: input.UserID, Decision: input.Decision,
		Email: input.Email, NoticeVersion: input.NoticeVersion,
		NoticeText: input.NoticeText, CreatedAt: now,
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return ContactEmailDecision{}, fmt.Errorf("begin contact email decision: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	_, _ = tx.Exec(`PRAGMA secure_delete = FAST`)
	if _, err := tx.Exec(`
		INSERT INTO contact_email_decisions
		(id, user_id, decision, email, notice_version, notice_text, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		decision.ID, decision.UserID, decision.Decision, nullableString(decision.Email),
		decision.NoticeVersion, decision.NoticeText, decision.CreatedAt,
	); err != nil {
		return ContactEmailDecision{}, fmt.Errorf("insert contact email decision: %w", err)
	}
	// A changed or revoked address must not remain recoverable in older rows.
	if _, err := tx.Exec(`
		UPDATE contact_email_decisions SET email = NULL
		WHERE user_id = ? AND id <> ?`, decision.UserID, decision.ID); err != nil {
		return ContactEmailDecision{}, fmt.Errorf("redact previous contact emails: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ContactEmailDecision{}, fmt.Errorf("commit contact email decision: %w", err)
	}
	_, _ = s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	return decision, nil
}

func validateContactEmailDecisionInput(input RecordContactEmailDecisionInput) error {
	if input.UserID == "" || len(input.UserID) > 255 || containsControl(input.UserID) {
		return errors.New("contact email user id is invalid")
	}
	if input.Decision != ContactEmailDecisionAllowed && input.Decision != ContactEmailDecisionDeclined && input.Decision != ContactEmailDecisionRevoked {
		return errors.New("contact email decision is invalid")
	}
	if input.Decision == ContactEmailDecisionAllowed && input.Email == "" {
		return errors.New("allowed contact email decision requires an address")
	}
	if len(input.Email) > 320 || containsControl(input.Email) {
		return errors.New("contact email address is invalid")
	}
	if input.NoticeVersion == "" || len(input.NoticeVersion) > 128 || containsControl(input.NoticeVersion) {
		return errors.New("contact email notice version is invalid")
	}
	if input.NoticeText == "" || len(input.NoticeText) > 64*1024 {
		return errors.New("contact email notice text is invalid")
	}
	return nil
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func (s *Store) activateArchivedContactEmailDecisions(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	return s.activateArchivedContactEmailDecisionsWithExecutor(ctx, s.db)
}

func (s *Store) activateArchivedContactEmailDecisionsWithExecutor(ctx context.Context, executor schemaMigrationExecutor) error {
	var tableName string
	err := executor.QueryRowContext(ctx, `
		SELECT name FROM sqlite_master
		WHERE type = 'table' AND name = 'legacy_v11_contact_email_decisions'`).Scan(&tableName)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("inspect archived contact email table: %w", err)
	}
	if err == nil {
		if _, err := executor.ExecContext(ctx, `
			INSERT OR IGNORE INTO contact_email_decisions
			(id, user_id, decision, email, notice_version, notice_text, created_at)
			SELECT id, 'local', decision, email, notice_version, notice_text,
			       CASE
			         WHEN typeof(created_at) = 'integer' AND created_at > 100000000000
			           THEN datetime(created_at / 1000.0, 'unixepoch')
			         WHEN typeof(created_at) = 'integer'
			           THEN datetime(created_at, 'unixepoch')
			         ELSE created_at
			       END
			FROM legacy_v11_contact_email_decisions`); err != nil {
			return fmt.Errorf("activate archived contact email decisions: %w", err)
		}
		var mismatched int
		if err := executor.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM legacy_v11_contact_email_decisions AS legacy
			LEFT JOIN contact_email_decisions AS active
			  ON active.id = legacy.id
			 AND active.user_id = 'local'
			 AND active.decision = legacy.decision
			 AND active.notice_version = legacy.notice_version
			 AND active.notice_text = legacy.notice_text
			WHERE active.id IS NULL`).Scan(&mismatched); err != nil {
			return fmt.Errorf("verify archived contact email activation: %w", err)
		}
		if mismatched != 0 {
			return fmt.Errorf("activate archived contact email decisions: %d rows conflicted with active storage", mismatched)
		}
	}

	_, _ = executor.ExecContext(ctx, `PRAGMA secure_delete = FAST`)
	result, err := executor.ExecContext(ctx, `
		UPDATE contact_email_decisions
		SET email = NULL
		WHERE email IS NOT NULL
		  AND (
		    decision <> 'allowed'
		    OR rowid <> (
		      SELECT latest.rowid
		      FROM contact_email_decisions AS latest
		      WHERE latest.user_id = contact_email_decisions.user_id
		      ORDER BY latest.created_at DESC, latest.rowid DESC
		      LIMIT 1
		    )
		  )`)
	if err != nil {
		return fmt.Errorf("redact archived contact email history: %w", err)
	}
	if affected, err := result.RowsAffected(); err == nil && affected > 0 {
		_, _ = executor.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	}
	return nil
}
