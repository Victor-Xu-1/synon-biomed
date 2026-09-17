package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrMutationIdempotencyConflict = errors.New("idempotency key already identifies a different mutation request")

const mutationResultLedgerSchema = `CREATE TABLE IF NOT EXISTS mutation_result_ledger (
	owner_user_id TEXT NOT NULL,
	idempotency_key TEXT NOT NULL,
	operation TEXT NOT NULL,
	request_hash TEXT NOT NULL,
	result_json TEXT NOT NULL,
	created_at TIMESTAMP NOT NULL,
	PRIMARY KEY (owner_user_id, idempotency_key, operation)
)`

type artifactVersionLedgerResult struct {
	ArtifactID string `json:"artifactId"`
	VersionID  string `json:"versionId"`
}

type attachmentLedgerResult struct {
	AttachmentID string `json:"attachmentId"`
}

type artifactEditLedgerResult struct {
	ArtifactID    string   `json:"artifactId"`
	VersionID     string   `json:"versionId"`
	AnnotationIDs []string `json:"annotationIds"`
}

func (s *Store) ensureMutationResultLedgerSchema(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, mutationResultLedgerSchema); err != nil {
		return fmt.Errorf("create mutation result ledger schema: %w", err)
	}
	return nil
}

func mutationRequestHash(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode mutation request identity: %w", err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func lookupMutationResultTx(ctx context.Context, tx *sql.Tx, ownerUserID, idempotencyKey, operation, requestHash string, output any) (bool, error) {
	var storedHash, raw string
	err := tx.QueryRowContext(ctx, `SELECT request_hash, result_json FROM mutation_result_ledger
		WHERE owner_user_id = ? AND idempotency_key = ? AND operation = ?`, ownerUserID, idempotencyKey, operation).Scan(&storedHash, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("look up mutation result: %w", err)
	}
	if storedHash != requestHash {
		return false, ErrMutationIdempotencyConflict
	}
	if err := json.Unmarshal([]byte(raw), output); err != nil {
		return false, fmt.Errorf("decode persisted mutation result: %w", err)
	}
	return true, nil
}

func (s *Store) lookupMutationResult(ctx context.Context, ownerUserID, idempotencyKey, operation, requestHash string, output any) (bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, fmt.Errorf("begin mutation result lookup: %w", err)
	}
	defer tx.Rollback()
	found, err := lookupMutationResultTx(ctx, tx, ownerUserID, idempotencyKey, operation, requestHash, output)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("finish mutation result lookup: %w", err)
	}
	return found, nil
}

func insertMutationResultTx(ctx context.Context, tx *sql.Tx, ownerUserID, idempotencyKey, operation, requestHash string, result any, now time.Time) error {
	for field, value := range map[string]string{"owner": ownerUserID, "idempotency key": idempotencyKey, "operation": operation, "request hash": requestHash} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("mutation result %s is required", field)
		}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode mutation result: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mutation_result_ledger
		(owner_user_id, idempotency_key, operation, request_hash, result_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, ownerUserID, idempotencyKey, operation, requestHash, string(raw), now)
	if err != nil {
		return fmt.Errorf("insert mutation result: %w", err)
	}
	return nil
}

func loadArtifactVersionResultTx(ctx context.Context, tx *sql.Tx, artifactID, versionID string) (Artifact, ArtifactVersion, error) {
	artifact, version, found, err := scanArtifactVersionRow(tx.QueryRowContext(ctx, artifactVersionJoinSelect+` WHERE artifact.id = ? AND version.id = ?`, artifactID, versionID))
	if err != nil {
		return Artifact{}, ArtifactVersion{}, err
	}
	if !found {
		return Artifact{}, ArtifactVersion{}, errors.New("persisted artifact mutation result is missing")
	}
	return artifact, version, nil
}
