package transcript

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type contractForeignKey struct {
	table    string
	from     []string
	to       []string
	onDelete string
}

type observedContractForeignKey struct {
	table    string
	from     []string
	to       []string
	onDelete string
}

func (r *Repository) ValidateContract(ctx context.Context) error {
	return r.validateContract(ctx, false)
}

func (r *Repository) ValidateCurrentContract(ctx context.Context) error {
	return r.validateContract(ctx, true)
}

func (r *Repository) validateContract(ctx context.Context, requireCurrent bool) error {
	if r == nil || r.db == nil {
		return ErrSchemaUnavailable
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return schemaError(err)
	}
	defer func() { _ = tx.Rollback() }()
	typedBootstrapInstalled, err := typedHistoryBootstrapV35Installed(ctx, tx)
	if err != nil {
		return err
	}
	if requireCurrent && !typedBootstrapInstalled {
		return ErrSchemaUnavailable
	}
	webReadModelInstalled, err := webReadModelV38Installed(ctx, tx)
	if err != nil {
		return err
	}
	if requireCurrent && !webReadModelInstalled {
		return ErrSchemaUnavailable
	}
	tables := []string{
		"transcript_streams", "transcript_runner_attempts", "transcript_events", "transcript_runner_receipts",
		"transcript_runner_checkpoints", "transcript_delivery_routes", "transcript_delivery_intents",
		"transcript_artifact_commits", "transcript_artifact_refs",
		"transcript_branches", "transcript_branch_state", "transcript_branch_events",
		"transcript_history_classification_runs", "transcript_history_classification_candidates",
		"transcript_history_shadow_comparisons",
		"transcript_history_backfill_runs", "transcript_history_backfill_candidates",
		"transcript_history_backfill_cursor_map",
		"transcript_history_cutover_runs", "transcript_history_cutover_events",
		"transcript_history_cutover_branches", "transcript_history_cutover_receipts",
		"transcript_history_cutover_artifact_refs", "transcript_history_cutover_branch_events",
		"transcript_history_cutover_cursor_map", "transcript_history_cutover_shadow_comparisons",
		"transcript_history_ordinary_cutover_runs", "transcript_history_ordinary_cursor_map",
		"transcript_history_activation_receipts", "transcript_payload_genesis_receipts", "transcript_frame_authority",
	}
	if typedBootstrapInstalled {
		tables = append(tables, "transcript_typed_history_bootstrap_receipts")
	}
	if webReadModelInstalled {
		tables = append(tables, "transcript_branch_heads", "transcript_artifact_reference_heads",
			"transcript_web_projection_dirty", "transcript_web_projection_state", "transcript_web_messages",
			"transcript_web_message_identities", "transcript_web_message_artifact_refs", "artifact_version_tombstones")
	}
	for _, table := range tables {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			return schemaError(err)
		}
		if count != 1 {
			return ErrSchemaUnavailable
		}
	}
	for _, index := range []struct {
		name  string
		table string
	}{
		{name: "transcript_session_epoch", table: "transcript_streams"},
		{name: "transcript_delivery_ready", table: "transcript_delivery_intents"},
		{name: "transcript_artifact_refs_source", table: "transcript_artifact_refs"},
		{name: "transcript_artifact_refs_version", table: "transcript_artifact_refs"},
		{name: "transcript_artifact_commits_pending", table: "transcript_artifact_commits"},
		{name: "transcript_artifact_commits_version", table: "transcript_artifact_commits"},
		{name: "transcript_branch_events_event", table: "transcript_branch_events"},
		{name: "transcript_history_classification_runs_latest", table: "transcript_history_classification_runs"},
		{name: "transcript_history_classification_findings", table: "transcript_history_classification_candidates"},
		{name: "transcript_history_backfill_stream", table: "transcript_history_backfill_runs"},
		{name: "transcript_history_backfill_candidate_source", table: "transcript_history_backfill_candidates"},
		{name: "transcript_history_cutover_stream", table: "transcript_history_cutover_runs"},
		{name: "transcript_history_cutover_ready", table: "transcript_history_cutover_runs"},
		{name: "transcript_history_cutover_cursor_target", table: "transcript_history_cutover_cursor_map"},
		{name: "transcript_stream_authority_identity", table: "transcript_streams"},
		{name: "transcript_history_activation_source", table: "transcript_history_activation_receipts"},
		{name: "transcript_frame_authority_stream", table: "transcript_frame_authority"},
	} {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=? AND tbl_name=?`, index.name, index.table).Scan(&count); err != nil {
			return schemaError(err)
		}
		if count != 1 {
			return ErrSchemaUnavailable
		}
	}
	columns, err := tx.QueryContext(ctx, `PRAGMA table_info(transcript_streams)`)
	if err != nil {
		return schemaError(err)
	}
	foundSessionID := false
	for columns.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := columns.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = columns.Close()
			return schemaError(err)
		}
		if name == "session_id" && strings.EqualFold(columnType, "TEXT") && notNull == 1 {
			foundSessionID = true
		}
	}
	if err := columns.Close(); err != nil {
		return schemaError(err)
	}
	if err := columns.Err(); err != nil {
		return schemaError(err)
	}
	if !foundSessionID {
		return ErrSchemaUnavailable
	}
	foreignKeys, err := tx.QueryContext(ctx, `PRAGMA foreign_key_list(transcript_artifact_refs)`)
	if err != nil {
		return schemaError(err)
	}
	restrictsSourceEvent := false
	for foreignKeys.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := foreignKeys.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			_ = foreignKeys.Close()
			return schemaError(err)
		}
		if table == "transcript_events" && from == "source_event_id" && to == "event_id" && strings.EqualFold(onDelete, "RESTRICT") {
			restrictsSourceEvent = true
		}
	}
	if err := foreignKeys.Close(); err != nil {
		return schemaError(err)
	}
	if err := foreignKeys.Err(); err != nil {
		return schemaError(err)
	}
	if !restrictsSourceEvent {
		return ErrSchemaUnavailable
	}
	if err := validateBranchContract(ctx, tx); err != nil {
		return err
	}
	if err := validateHistoryClassificationContract(ctx, tx); err != nil {
		return err
	}
	if err := validateHistoryBackfillContract(ctx, tx); err != nil {
		return err
	}
	if err := validateHistoryCutoverContract(ctx, tx); err != nil {
		return err
	}
	if err := validateHistoryActivationContract(ctx, tx); err != nil {
		return err
	}
	if err := validateHistoryOrdinaryContract(ctx, tx); err != nil {
		return err
	}
	if typedBootstrapInstalled {
		if err := validateTypedHistoryBootstrapContract(ctx, tx); err != nil {
			return err
		}
	}
	if webReadModelInstalled {
		if err := validateWebReadModelV38Contract(ctx, tx); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return schemaError(err)
	}
	return nil
}

func webReadModelV38Installed(ctx context.Context, tx *sql.Tx) (bool, error) {
	names := WebReadModelV38ObjectNames()
	var count int
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(names)), ",")
	arguments := make([]any, len(names))
	for index, name := range names {
		arguments[index] = name
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name IN (`+placeholders+`)`, arguments...).Scan(&count); err != nil {
		return false, schemaError(err)
	}
	if count == 0 {
		return false, nil
	}
	if count != len(names) {
		return false, ErrSchemaUnavailable
	}
	return true, nil
}

func validateWebReadModelV38Contract(ctx context.Context, tx *sql.Tx) error {
	names := WebReadModelV38ObjectNames()
	statements := WebReadModelV38CanonicalStatements()
	if len(names) != len(statements) {
		return ErrSchemaUnavailable
	}
	for index, name := range names {
		var observed string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE name=?`, name).Scan(&observed); err != nil {
			return schemaError(err)
		}
		valid := normalizeContractSQL(observed) == normalizeContractSQL(statements[index])
		if name == "transcript_web_projection_state" {
			valid = valid || normalizeContractSQL(observed) == normalizeContractSQL(WebReadModelV47ProjectionStateStatement()) ||
				normalizeContractSQL(observed) == normalizeContractSQL(WebReadModelV48ProjectionStateStatement()) ||
				normalizeContractSQL(observed) == normalizeContractSQL(WebReadModelV49ProjectionStateStatement()) ||
				normalizeContractSQL(observed) == normalizeContractSQL(WebReadModelV58ProjectionStateStatement()) ||
				normalizeContractSQL(observed) == normalizeContractSQL(WebReadModelV59ProjectionStateStatement()) ||
				normalizeContractSQL(observed) == normalizeContractSQL(WebReadModelV60ProjectionStateStatement()) ||
				normalizeContractSQL(observed) == normalizeContractSQL(WebReadModelV64ProjectionStateStatement()) ||
				normalizeContractSQL(observed) == normalizeContractSQL(WebReadModelV65ProjectionStateStatement()) ||
				normalizeContractSQL(observed) == normalizeContractSQL(WebReadModelV66ProjectionStateStatement()) ||
				normalizeContractSQL(observed) == normalizeContractSQL(WebReadModelV67ProjectionStateStatement())
		}
		if !valid {
			return fmt.Errorf("validate Transcript Web read-model object %s: %w", name, ErrSchemaUnavailable)
		}
	}
	return nil
}

func typedHistoryBootstrapV35Installed(ctx context.Context, tx *sql.Tx) (bool, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name IN (
		'transcript_typed_history_bootstrap_receipts',
		'transcript_typed_history_bootstrap_validate',
		'transcript_typed_history_bootstrap_immutable',
		'transcript_typed_history_bootstrap_delete_active'
	)`).Scan(&count); err != nil {
		return false, schemaError(err)
	}
	if count == 0 {
		return false, nil
	}
	if count != len(TypedHistoryBootstrapV35ObjectNames()) {
		return false, ErrSchemaUnavailable
	}
	return true, nil
}

func validateTypedHistoryBootstrapContract(ctx context.Context, tx *sql.Tx) error {
	names := TypedHistoryBootstrapV35ObjectNames()
	statements := TypedHistoryBootstrapV35CanonicalStatements()
	if len(names) != len(statements) {
		return ErrSchemaUnavailable
	}
	for index, name := range names {
		var observed string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE name=?`, name).Scan(&observed); err != nil {
			return schemaError(err)
		}
		if normalizeContractSQL(observed) != normalizeContractSQL(statements[index]) {
			return fmt.Errorf("validate typed history bootstrap object %s: %w", name, ErrSchemaUnavailable)
		}
	}
	return nil
}

func validateHistoryActivationContract(ctx context.Context, tx *sql.Tx) error {
	v34Cursor, err := historyOrdinaryCursorV34Installed(ctx, tx)
	if err != nil {
		return err
	}
	for _, name := range HistoryActivationV31ObjectNames() {
		expected, found := historyActivationV31StatementByName(name)
		if replacement, replaced := payloadGenesisV32CanonicalStatementByName(name); replaced {
			expected, found = replacement, true
		}
		if replacement, replaced := historyOrdinaryV33CanonicalStatementByName(name); replaced {
			expected, found = replacement, true
		}
		if !found {
			return ErrSchemaUnavailable
		}
		var observed string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE name=?`, name).
			Scan(&observed); err != nil {
			return schemaError(err)
		}
		if normalizeContractSQL(observed) != normalizeContractSQL(expected) {
			return fmt.Errorf("validate history activation table %s: %w", name, ErrSchemaUnavailable)
		}
	}
	ordinaryObjects := []string{
		"transcript_history_ordinary_cutover_runs",
		"transcript_history_ordinary_cursor_map",
		"transcript_history_activation_freeze_ordinary",
		"transcript_history_activation_immutable",
	}
	if v34Cursor {
		ordinaryObjects = append(ordinaryObjects,
			"transcript_history_ordinary_cursor_validate",
			"transcript_history_ordinary_cursor_immutable",
			"transcript_history_ordinary_cursor_delete_active",
		)
	}
	for _, name := range ordinaryObjects {
		expected, found := historyOrdinaryV33CanonicalStatementByName(name)
		if replacement, replaced := historyOrdinaryCursorV34CanonicalStatementByName(name); v34Cursor && replaced {
			expected, found = replacement, true
		}
		if !found {
			return ErrSchemaUnavailable
		}
		var observed string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE name=?`, name).Scan(&observed); err != nil {
			return schemaError(err)
		}
		if normalizeContractSQL(observed) != normalizeContractSQL(expected) {
			return fmt.Errorf("validate ordinary history object %s: %w", name, ErrSchemaUnavailable)
		}
	}
	for _, name := range []string{
		"transcript_payload_genesis_receipts",
		"transcript_payload_genesis_validate",
		"transcript_payload_genesis_immutable",
	} {
		expected, found := payloadGenesisV32CanonicalStatementByName(name)
		if !found {
			return ErrSchemaUnavailable
		}
		var observed string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE name=?`, name).Scan(&observed); err != nil {
			return schemaError(err)
		}
		if normalizeContractSQL(observed) != normalizeContractSQL(expected) {
			return fmt.Errorf("validate payload genesis object %s: %w", name, ErrSchemaUnavailable)
		}
	}
	if err := validateContractPrimaryKey(ctx, tx, "transcript_payload_genesis_receipts", []string{"genesis_id"}); err != nil {
		return fmt.Errorf("validate payload genesis receipt primary key: %w", err)
	}
	if err := validateContractForeignKeys(ctx, tx, "transcript_payload_genesis_receipts", []contractForeignKey{
		{table: "transcript_streams", from: []string{"stream_uid"}, to: []string{"stream_uid"}, onDelete: "NO ACTION"},
		{table: "transcript_streams", from: []string{"stream_uid", "owner_id", "session_id", "epoch"}, to: []string{"stream_uid", "owner_id", "session_id", "epoch"}, onDelete: "NO ACTION"},
		{table: "transcript_branches", from: []string{"stream_uid", "active_branch_id"}, to: []string{"stream_uid", "branch_id"}, onDelete: "NO ACTION"},
	}); err != nil {
		return fmt.Errorf("validate payload genesis receipt foreign keys: %w", err)
	}
	if err := validateContractTableClauses(ctx, tx, "transcript_payload_genesis_receipts", []string{
		"check(length(genesis_id) = 32)", "check(epoch = 1)", "check(branch_generation > 0)",
		"check(authority_generation = 1)", "check(source_event_count >= 0)",
		"check(length(source_sha256) = 32)", "check(status = 'active')",
		"check(source_kind in ('empty','frame_import','canonical_clone'))",
		"source_kind='canonical_clone' and length(source_stream_uid)>0 and source_epoch>0",
	}); err != nil {
		return fmt.Errorf("validate payload genesis receipt clauses: %w", err)
	}
	if err := validateContractPrimaryKey(ctx, tx, "transcript_history_activation_receipts", []string{"activation_id"}); err != nil {
		return fmt.Errorf("validate history activation receipt primary key: %w", err)
	}
	if err := validateContractForeignKeys(ctx, tx, "transcript_history_activation_receipts", []contractForeignKey{
		{table: "transcript_history_cutover_runs", from: []string{"cutover_id"}, to: []string{"cutover_id"}, onDelete: "NO ACTION"},
		{table: "transcript_history_cutover_runs", from: []string{"cutover_id", "source_stream_uid", "owner_id"}, to: []string{"cutover_id", "stream_uid", "owner_id"}, onDelete: "NO ACTION"},
		{table: "transcript_history_ordinary_cutover_runs", from: []string{"ordinary_cutover_id"}, to: []string{"ordinary_cutover_id"}, onDelete: "NO ACTION"},
		{table: "transcript_history_ordinary_cutover_runs", from: []string{"ordinary_cutover_id", "source_stream_uid", "owner_id"}, to: []string{"ordinary_cutover_id", "source_stream_uid", "owner_id"}, onDelete: "NO ACTION"},
		{table: "transcript_streams", from: []string{"source_stream_uid"}, to: []string{"stream_uid"}, onDelete: "NO ACTION"},
		{table: "transcript_streams", from: []string{"target_stream_uid"}, to: []string{"stream_uid"}, onDelete: "NO ACTION"},
		{table: "transcript_streams", from: []string{"source_stream_uid", "owner_id", "session_id", "source_epoch"}, to: []string{"stream_uid", "owner_id", "session_id", "epoch"}, onDelete: "NO ACTION"},
		{table: "transcript_streams", from: []string{"target_stream_uid", "owner_id", "session_id", "target_epoch"}, to: []string{"stream_uid", "owner_id", "session_id", "epoch"}, onDelete: "NO ACTION"},
		{table: "transcript_history_activation_receipts", from: []string{"prior_activation_id"}, to: []string{"activation_id"}, onDelete: "NO ACTION"},
		{table: "transcript_branches", from: []string{"target_stream_uid", "active_branch_id"}, to: []string{"stream_uid", "branch_id"}, onDelete: "NO ACTION"},
	}); err != nil {
		return fmt.Errorf("validate history activation receipt foreign keys: %w", err)
	}
	if err := validateContractTableClauses(ctx, tx, "transcript_history_activation_receipts", []string{
		"check(length(activation_id) = 32)", "check(cutover_id is null or length(cutover_id) = 32)",
		"check(ordinary_cutover_id is null or length(ordinary_cutover_id) = 32)",
		"check(provenance_kind in ('ask_user_v30','ordinary_v33'))",
		"provenance_kind='ask_user_v30' and cutover_id is not null and ordinary_cutover_id is null",
		"provenance_kind='ordinary_v33' and cutover_id is null and ordinary_cutover_id is not null",
		"check(target_epoch = source_epoch + 1)", "check(length(materialized_sha256) = 32)",
		"check(authority_generation > 1)", "check(status = 'active')",
	}); err != nil {
		return fmt.Errorf("validate history activation receipt clauses: %w", err)
	}
	if err := validateContractPrimaryKey(ctx, tx, "transcript_frame_authority", []string{"owner_id", "session_id"}); err != nil {
		return fmt.Errorf("validate frame authority primary key: %w", err)
	}
	if err := validateContractForeignKeys(ctx, tx, "transcript_frame_authority", []contractForeignKey{
		{table: "transcript_streams", from: []string{"active_stream_uid"}, to: []string{"stream_uid"}, onDelete: "CASCADE"},
		{table: "transcript_history_activation_receipts", from: []string{"activation_id"}, to: []string{"activation_id"}, onDelete: "NO ACTION"},
		{table: "transcript_payload_genesis_receipts", from: []string{"genesis_id"}, to: []string{"genesis_id"}, onDelete: "NO ACTION"},
		{table: "transcript_streams", from: []string{"active_stream_uid", "owner_id", "session_id", "active_epoch"}, to: []string{"stream_uid", "owner_id", "session_id", "epoch"}, onDelete: "CASCADE"},
		{table: "transcript_history_activation_receipts", from: []string{"activation_id", "active_stream_uid", "active_epoch", "authority_generation"}, to: []string{"activation_id", "target_stream_uid", "target_epoch", "authority_generation"}, onDelete: "NO ACTION"},
		{table: "transcript_payload_genesis_receipts", from: []string{"genesis_id", "active_stream_uid", "active_epoch", "authority_generation"}, to: []string{"genesis_id", "stream_uid", "epoch", "authority_generation"}, onDelete: "NO ACTION"},
	}); err != nil {
		return fmt.Errorf("validate frame authority foreign keys: %w", err)
	}
	if err := validateContractTableClauses(ctx, tx, "transcript_frame_authority", []string{
		"check(read_authority in ('legacy_mixed_v1','transcript_payload_v1'))",
		"check(write_authority in ('legacy_frame_ref_v1','transcript_payload_v1'))",
		"and activation_id is null and genesis_id is null) or",
		"and ((activation_id is not null and genesis_id is null) or",
	}); err != nil {
		return fmt.Errorf("validate frame authority clauses: %w", err)
	}
	if err := validateContractIndex(ctx, tx, "transcript_history_activation_source",
		[]string{"source_stream_uid", "target_epoch", "activated_at"}); err != nil {
		return fmt.Errorf("validate history activation source index: %w", err)
	}
	if err := validateContractIndex(ctx, tx, "transcript_stream_authority_identity",
		[]string{"stream_uid", "owner_id", "session_id", "epoch"}); err != nil {
		return fmt.Errorf("validate transcript stream authority identity index: %w", err)
	}
	if err := validateContractIndex(ctx, tx, "transcript_frame_authority_stream",
		[]string{"active_stream_uid", "authority_generation"}); err != nil {
		return fmt.Errorf("validate frame authority stream index: %w", err)
	}
	return nil
}

func validateHistoryOrdinaryContract(ctx context.Context, tx *sql.Tx) error {
	v34Cursor, err := historyOrdinaryCursorV34Installed(ctx, tx)
	if err != nil {
		return err
	}
	if err := validateContractPrimaryKey(ctx, tx, "transcript_history_ordinary_cutover_runs",
		[]string{"ordinary_cutover_id"}); err != nil {
		return fmt.Errorf("validate ordinary history cutover primary key: %w", err)
	}
	if err := validateContractForeignKeys(ctx, tx, "transcript_history_ordinary_cutover_runs", []contractForeignKey{
		{table: "transcript_history_classification_runs", from: []string{"classification_run_id"}, to: []string{"run_id"}, onDelete: "NO ACTION"},
		{table: "transcript_streams", from: []string{"source_stream_uid"}, to: []string{"stream_uid"}, onDelete: "NO ACTION"},
		{table: "transcript_streams", from: []string{"target_stream_uid"}, to: []string{"stream_uid"}, onDelete: "NO ACTION"},
		{table: "transcript_streams", from: []string{"source_stream_uid", "owner_id", "session_id", "source_epoch"}, to: []string{"stream_uid", "owner_id", "session_id", "epoch"}, onDelete: "NO ACTION"},
		{table: "transcript_streams", from: []string{"target_stream_uid", "owner_id", "session_id", "target_epoch"}, to: []string{"stream_uid", "owner_id", "session_id", "epoch"}, onDelete: "NO ACTION"},
		{table: "transcript_branches", from: []string{"source_stream_uid", "source_branch_id"}, to: []string{"stream_uid", "branch_id"}, onDelete: "NO ACTION"},
		{table: "transcript_branches", from: []string{"target_stream_uid", "target_branch_id"}, to: []string{"stream_uid", "branch_id"}, onDelete: "NO ACTION"},
	}); err != nil {
		return fmt.Errorf("validate ordinary history cutover foreign keys: %w", err)
	}
	if err := validateContractTableClauses(ctx, tx, "transcript_history_ordinary_cutover_runs", []string{
		"check(length(ordinary_cutover_id) = 32)", "check(target_epoch = source_epoch + 1)",
		"check(length(source_sha256) = 32)", "check(length(materialized_sha256) = 32)",
		"check(history_kind = 'ordinary_no_ask_user_v1')", "check(status = 'active')",
	}); err != nil {
		return fmt.Errorf("validate ordinary history cutover clauses: %w", err)
	}
	cursorPrimaryKey := []string{"ordinary_cutover_id", "source_branch_id", "source_ordinal"}
	if v34Cursor {
		cursorPrimaryKey = []string{"ordinary_cutover_id", "source_branch_id", "source_message_index"}
	}
	if err := validateContractPrimaryKey(ctx, tx, "transcript_history_ordinary_cursor_map", cursorPrimaryKey); err != nil {
		return fmt.Errorf("validate ordinary history cursor primary key: %w", err)
	}
	cursorForeignKeys := []contractForeignKey{
		{table: "transcript_history_ordinary_cutover_runs", from: []string{"ordinary_cutover_id"}, to: []string{"ordinary_cutover_id"}, onDelete: "NO ACTION"},
		{table: "transcript_branch_events", from: []string{"source_stream_uid", "source_branch_id", "source_ordinal"}, to: []string{"stream_uid", "branch_id", "ordinal"}, onDelete: "NO ACTION"},
		{table: "transcript_events", from: []string{"source_stream_uid", "source_event_id"}, to: []string{"stream_uid", "event_id"}, onDelete: "NO ACTION"},
		{table: "transcript_events", from: []string{"target_stream_uid", "target_event_id"}, to: []string{"stream_uid", "event_id"}, onDelete: "NO ACTION"},
		{table: "transcript_events", from: []string{"target_stream_uid", "target_publication_seq"}, to: []string{"stream_uid", "publication_seq"}, onDelete: "NO ACTION"},
	}
	if v34Cursor {
		cursorForeignKeys = append(cursorForeignKeys,
			contractForeignKey{table: "transcript_streams", from: []string{"source_stream_uid"}, to: []string{"stream_uid"}, onDelete: "NO ACTION"},
			contractForeignKey{table: "transcript_streams", from: []string{"target_stream_uid"}, to: []string{"stream_uid"}, onDelete: "NO ACTION"},
		)
	} else {
		cursorForeignKeys = append(cursorForeignKeys,
			contractForeignKey{table: "transcript_history_ordinary_cutover_runs", from: []string{"ordinary_cutover_id", "source_stream_uid", "source_branch_id"}, to: []string{"ordinary_cutover_id", "source_stream_uid", "source_branch_id"}, onDelete: "NO ACTION"},
			contractForeignKey{table: "transcript_history_ordinary_cutover_runs", from: []string{"ordinary_cutover_id", "target_stream_uid", "target_branch_id"}, to: []string{"ordinary_cutover_id", "target_stream_uid", "target_branch_id"}, onDelete: "NO ACTION"},
			contractForeignKey{table: "transcript_branch_events", from: []string{"target_stream_uid", "target_branch_id", "target_event_id"}, to: []string{"stream_uid", "branch_id", "event_id"}, onDelete: "NO ACTION"},
		)
	}
	if err := validateContractForeignKeys(ctx, tx, "transcript_history_ordinary_cursor_map", cursorForeignKeys); err != nil {
		return fmt.Errorf("validate ordinary history cursor foreign keys: %w", err)
	}
	cursorClauses := []string{
		"check(length(ordinary_cutover_id) = 32)", "check(source_ordinal > 0)",
		"check(source_event_id > 0)", "check(target_event_id > 0)",
		"check(target_publication_seq > 0)", "check(target_message_index >= 0)",
		"check(length(stable_message_id) > 0)",
	}
	if v34Cursor {
		cursorClauses = append(cursorClauses,
			"check(source_generation > 0)",
			"check(source_through_publication_seq >= 0)",
			"check(source_message_index >= 0)",
		)
	}
	if err := validateContractTableClauses(ctx, tx, "transcript_history_ordinary_cursor_map", cursorClauses); err != nil {
		return fmt.Errorf("validate ordinary history cursor clauses: %w", err)
	}
	return nil
}

func historyOrdinaryCursorV34Installed(ctx context.Context, tx *sql.Tx) (bool, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(transcript_history_ordinary_cursor_map)`)
	if err != nil {
		return false, schemaError(err)
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, schemaError(err)
		}
		switch name {
		case "source_generation", "source_through_publication_seq", "source_message_index":
			found[name] = true
		}
	}
	if err := rows.Err(); err != nil {
		return false, schemaError(err)
	}
	if len(found) == 0 {
		return false, nil
	}
	if len(found) != 3 {
		return false, ErrSchemaUnavailable
	}
	return true, nil
}

func validateHistoryCutoverContract(ctx context.Context, tx *sql.Tx) error {
	for index, name := range HistoryCutoverV30TableNames() {
		var observed string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, name).
			Scan(&observed); err != nil {
			return schemaError(err)
		}
		if normalizeContractSQL(observed) != normalizeContractSQL(historyCutoverV30Statements[index]) {
			return ErrSchemaUnavailable
		}
	}
	checks := []struct {
		table       string
		primaryKey  []string
		foreignKeys []contractForeignKey
		clauses     []string
	}{
		{table: "transcript_history_cutover_runs", primaryKey: []string{"cutover_id"}, foreignKeys: []contractForeignKey{
			{table: "transcript_history_backfill_runs", from: []string{"backfill_id"}, to: []string{"backfill_id"}, onDelete: "NO ACTION"},
			{table: "transcript_history_cutover_runs", from: []string{"supersedes_cutover_id"}, to: []string{"cutover_id"}, onDelete: "NO ACTION"},
			{table: "transcript_branches", from: []string{"stream_uid", "source_branch_id"}, to: []string{"stream_uid", "branch_id"}, onDelete: "NO ACTION"},
		}, clauses: []string{
			"check(length(cutover_id) = 32)", "check(status in ('ready','superseded'))", "check(activated = 0)",
			"check(prior_read_authority = 'legacy_mixed_v1')", "check(target_read_authority = 'transcript_payload_v1')",
		}},
		{table: "transcript_history_cutover_events", primaryKey: []string{"cutover_id", "target_event_key"}, foreignKeys: []contractForeignKey{
			{table: "transcript_history_cutover_runs", from: []string{"cutover_id"}, to: []string{"cutover_id"}, onDelete: "CASCADE"},
		}, clauses: []string{
			"check(fact_kind in ('existing','ask_user_prompt','ask_user_pending','ask_user_result'))",
			"check(json_valid(payload_json))", "check(length(payload_sha256) = 32)",
		}},
		{table: "transcript_history_cutover_branches", primaryKey: []string{"cutover_id", "source_branch_id"}, foreignKeys: []contractForeignKey{
			{table: "transcript_history_cutover_runs", from: []string{"cutover_id"}, to: []string{"cutover_id"}, onDelete: "CASCADE"},
			{table: "transcript_history_cutover_branches", from: []string{"cutover_id", "parent_source_branch_id"}, to: []string{"cutover_id", "source_branch_id"}, onDelete: "NO ACTION"},
			{table: "transcript_history_cutover_branches", from: []string{"cutover_id", "parent_target_branch_id"}, to: []string{"cutover_id", "target_branch_id"}, onDelete: "NO ACTION"},
		}, clauses: []string{
			"check(kind in ('base','edit','answer'))", "check(length(source_membership_sha256) = 32)",
			"check(length(target_membership_sha256) = 32)", "check(length(request_sha256) = 32)",
		}},
		{table: "transcript_history_cutover_receipts", primaryKey: []string{"cutover_id", "source_attempt"}, foreignKeys: []contractForeignKey{
			{table: "transcript_history_cutover_runs", from: []string{"cutover_id"}, to: []string{"cutover_id"}, onDelete: "CASCADE"},
			{table: "transcript_history_cutover_events", from: []string{"cutover_id", "target_event_key"}, to: []string{"cutover_id", "target_event_key"}, onDelete: "NO ACTION"},
		}, clauses: []string{
			"check(status in ('completed','failed','cancelled'))",
		}},
		{table: "transcript_history_cutover_artifact_refs", primaryKey: []string{"cutover_id", "target_event_key", "artifact_id", "version_id"}, foreignKeys: []contractForeignKey{
			{table: "transcript_history_cutover_events", from: []string{"cutover_id", "target_event_key"}, to: []string{"cutover_id", "target_event_key"}, onDelete: "NO ACTION"},
		}, clauses: []string{
			"check(relation in ('produced','consumed','cited','attached'))",
			"check(availability in ('available','deleted','missing'))",
		}},
		{table: "transcript_history_cutover_branch_events", primaryKey: []string{"cutover_id", "target_branch_id", "target_ordinal"}, foreignKeys: []contractForeignKey{
			{table: "transcript_history_cutover_branches", from: []string{"cutover_id", "target_branch_id"}, to: []string{"cutover_id", "target_branch_id"}, onDelete: "CASCADE"},
			{table: "transcript_history_cutover_events", from: []string{"cutover_id", "target_event_key"}, to: []string{"cutover_id", "target_event_key"}, onDelete: "NO ACTION"},
		}},
		{table: "transcript_history_cutover_cursor_map", primaryKey: []string{"cutover_id", "source_branch_id", "source_message_index"}, foreignKeys: []contractForeignKey{
			{table: "transcript_history_cutover_branches", from: []string{"cutover_id", "source_branch_id"}, to: []string{"cutover_id", "source_branch_id"}, onDelete: "CASCADE"},
			{table: "transcript_history_cutover_branches", from: []string{"cutover_id", "target_branch_id"}, to: []string{"cutover_id", "target_branch_id"}, onDelete: "CASCADE"},
			{table: "transcript_history_cutover_events", from: []string{"cutover_id", "target_publication_seq"}, to: []string{"cutover_id", "target_publication_seq"}, onDelete: "NO ACTION"},
		}},
		{table: "transcript_history_cutover_shadow_comparisons", primaryKey: []string{"cutover_id", "source_branch_id", "dimension"}, foreignKeys: []contractForeignKey{
			{table: "transcript_history_cutover_runs", from: []string{"cutover_id"}, to: []string{"cutover_id"}, onDelete: "CASCADE"},
			{table: "transcript_history_cutover_branches", from: []string{"cutover_id", "source_branch_id"}, to: []string{"cutover_id", "source_branch_id"}, onDelete: "CASCADE"},
		}, clauses: []string{
			"check(verdict = 'match')", "check(legacy_sha256 = target_sha256)",
		}},
	}
	for _, check := range checks {
		if err := validateContractPrimaryKey(ctx, tx, check.table, check.primaryKey); err != nil {
			return err
		}
		if err := validateContractForeignKeys(ctx, tx, check.table, check.foreignKeys); err != nil {
			return err
		}
		if err := validateContractTableClauses(ctx, tx, check.table, check.clauses); err != nil {
			return err
		}
	}
	if err := validateContractIndex(ctx, tx, "transcript_history_cutover_stream",
		[]string{"stream_uid", "source_branch_id", "source_generation", "cutover_id"}); err != nil {
		return err
	}
	if err := validateContractIndex(ctx, tx, "transcript_history_cutover_ready",
		[]string{"backfill_id", "status", "updated_at", "cutover_id"}); err != nil {
		return err
	}
	return validateContractIndex(ctx, tx, "transcript_history_cutover_cursor_target",
		[]string{"cutover_id", "target_branch_id", "target_message_index"})
}

func validateHistoryBackfillContract(ctx context.Context, tx *sql.Tx) error {
	for index, name := range HistoryBackfillV29TableNames() {
		var observed string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, name).
			Scan(&observed); err != nil {
			return schemaError(err)
		}
		if normalizeContractSQL(observed) != normalizeContractSQL(historyBackfillV29Statements[index]) {
			return ErrSchemaUnavailable
		}
	}
	checks := []struct {
		table       string
		primaryKey  []string
		foreignKeys []contractForeignKey
		clauses     []string
	}{
		{
			table:      "transcript_history_backfill_runs",
			primaryKey: []string{"backfill_id"},
			foreignKeys: []contractForeignKey{
				{table: "transcript_history_classification_runs", from: []string{"classification_run_id"}, to: []string{"run_id"}, onDelete: "NO ACTION"},
				{table: "transcript_branches", from: []string{"stream_uid", "branch_id"}, to: []string{"stream_uid", "branch_id"}, onDelete: "NO ACTION"},
			},
			clauses: []string{
				"check(length(backfill_id) = 32)", "check(contract_version = 1)",
				"check(length(source_sha256) = 32)", "check(length(staging_sha256) = 32)",
				"check(candidate_count > 0)",
			},
		},
		{
			table:      "transcript_history_backfill_candidates",
			primaryKey: []string{"backfill_id", "candidate_id"},
			foreignKeys: []contractForeignKey{
				{table: "transcript_history_backfill_runs", from: []string{"backfill_id", "classification_run_id"}, to: []string{"backfill_id", "classification_run_id"}, onDelete: "CASCADE"},
				{table: "transcript_history_classification_candidates", from: []string{"classification_run_id", "candidate_id"}, to: []string{"run_id", "candidate_id"}, onDelete: "NO ACTION"},
			},
			clauses: []string{
				"check(length(candidate_id) = 32)", "check(candidate_ordinal > 0)",
				"check(runner_attempt > 0)", "check(json_valid(prompt_json))", "check(json_valid(pending_json))",
				"check(json_valid(result_json))",
				"check(length(prompt_client_message_id) > 0)", "check(length(pending_client_message_id) > 0)",
				"check(length(terminal_client_message_id) > 0)",
				"check(prompt_client_message_id <> pending_client_message_id)",
				"check(prompt_client_message_id <> terminal_client_message_id)",
				"check(pending_client_message_id <> terminal_client_message_id)",
				"check(length(payload_sha256) = 32)", "check(length(evidence_sha256) = 32)",
			},
		},
		{
			table:      "transcript_history_backfill_cursor_map",
			primaryKey: []string{"backfill_id", "legacy_ordinal"},
			foreignKeys: []contractForeignKey{{
				table: "transcript_history_backfill_candidates", from: []string{"backfill_id", "candidate_id"},
				to: []string{"backfill_id", "candidate_id"}, onDelete: "CASCADE",
			}},
			clauses: []string{"check(length(candidate_id) = 32)", "check(legacy_ordinal > 0)"},
		},
	}
	for _, check := range checks {
		if err := validateContractPrimaryKey(ctx, tx, check.table, check.primaryKey); err != nil {
			return err
		}
		if err := validateContractForeignKeys(ctx, tx, check.table, check.foreignKeys); err != nil {
			return err
		}
		if err := validateContractTableClauses(ctx, tx, check.table, check.clauses); err != nil {
			return err
		}
	}
	if err := validateContractIndex(ctx, tx, "transcript_history_backfill_stream",
		[]string{"stream_uid", "branch_id", "branch_generation", "backfill_id"}); err != nil {
		return err
	}
	return validateContractIndex(ctx, tx, "transcript_history_backfill_candidate_source",
		[]string{"classification_run_id", "candidate_id", "backfill_id"})
}

func validateHistoryClassificationContract(ctx context.Context, tx *sql.Tx) error {
	canonicalTables := []struct {
		name      string
		statement string
	}{
		{name: "transcript_history_classification_runs", statement: historyClassificationV27Statements[0]},
		{name: "transcript_history_classification_candidates", statement: historyClassificationV27Statements[1]},
		{name: "transcript_history_shadow_comparisons", statement: historyClassificationV27Statements[2]},
	}
	for _, table := range canonicalTables {
		var observed string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table.name).
			Scan(&observed); err != nil {
			return schemaError(err)
		}
		if normalizeContractSQL(observed) != normalizeContractSQL(table.statement) {
			return ErrSchemaUnavailable
		}
	}
	checks := []struct {
		table       string
		primaryKey  []string
		foreignKeys []contractForeignKey
		clauses     []string
	}{
		{
			table:      "transcript_history_classification_runs",
			primaryKey: []string{"run_id"},
			foreignKeys: []contractForeignKey{{
				table: "transcript_branches", from: []string{"stream_uid", "branch_id"},
				to: []string{"stream_uid", "branch_id"}, onDelete: "CASCADE",
			}},
			clauses: []string{
				"check(length(run_id) = 32)",
				"check(candidate_count = native_count + eligible_count + poison_count + conflict_count)",
				"check(scan_status in ('complete','blocked'))",
			},
		},
		{
			table:      "transcript_history_classification_candidates",
			primaryKey: []string{"run_id", "candidate_id"},
			foreignKeys: []contractForeignKey{{
				table: "transcript_history_classification_runs", from: []string{"run_id"},
				to: []string{"run_id"}, onDelete: "CASCADE",
			}},
			clauses: []string{
				"check(length(candidate_id) = 32)",
				"check(disposition in ('native_v1','eligible','poison','conflict'))",
				"check(state_json is null or json_valid(state_json))",
			},
		},
		{
			table:      "transcript_history_shadow_comparisons",
			primaryKey: []string{"run_id", "dimension"},
			foreignKeys: []contractForeignKey{{
				table: "transcript_history_classification_runs", from: []string{"run_id"},
				to: []string{"run_id"}, onDelete: "CASCADE",
			}},
			clauses: []string{
				"check(dimension in ('stable_ids','branch_order','ask_user_states','terminal_facts','artifact_refs'))",
				"check(verdict in ('match','mismatch','blocked'))",
				"check((verdict='blocked' and candidate_sha256 is null) or (verdict in ('match','mismatch') and candidate_sha256 is not null))",
			},
		},
	}
	for _, check := range checks {
		if err := validateContractPrimaryKey(ctx, tx, check.table, check.primaryKey); err != nil {
			return err
		}
		if err := validateContractForeignKeys(ctx, tx, check.table, check.foreignKeys); err != nil {
			return err
		}
		if err := validateContractTableClauses(ctx, tx, check.table, check.clauses); err != nil {
			return err
		}
	}
	if err := validateContractIndex(ctx, tx, "transcript_history_classification_runs_latest",
		[]string{"stream_uid", "branch_id", "contract_version", "through_publication_seq", "run_id"}); err != nil {
		return err
	}
	if err := validateContractIndexSQL(ctx, tx, "transcript_history_classification_runs_latest",
		"through_publication_seq desc"); err != nil {
		return err
	}
	return validateContractIndex(ctx, tx, "transcript_history_classification_findings",
		[]string{"disposition", "reason_code", "run_id", "candidate_id"})
}

func validateContractIndexSQL(ctx context.Context, tx *sql.Tx, index, clause string) error {
	var statement string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&statement); err != nil {
		return schemaError(err)
	}
	if !strings.Contains(normalizeContractSQL(statement), normalizeContractSQL(clause)) {
		return ErrSchemaUnavailable
	}
	return nil
}

func validateBranchContract(ctx context.Context, tx *sql.Tx) error {
	checks := []struct {
		table       string
		primaryKey  []string
		foreignKeys []contractForeignKey
		clauses     []string
	}{
		{
			table:      "transcript_branches",
			primaryKey: []string{"stream_uid", "branch_id"},
			foreignKeys: []contractForeignKey{
				{table: "transcript_streams", from: []string{"stream_uid"}, to: []string{"stream_uid"}, onDelete: "CASCADE"},
				{table: "transcript_branches", from: []string{"stream_uid", "parent_branch_id"}, to: []string{"stream_uid", "branch_id"}, onDelete: "NO ACTION"},
				{table: "transcript_events", from: []string{"stream_uid", "fork_event_id"}, to: []string{"stream_uid", "event_id"}, onDelete: "NO ACTION"},
			},
			clauses: []string{
				"foreign key(stream_uid,parent_branch_id) references transcript_branches(stream_uid,branch_id) on delete no action deferrable initially deferred",
				"foreign key(stream_uid,fork_event_id) references transcript_events(stream_uid,event_id) on delete no action deferrable initially deferred",
			},
		},
		{
			table:      "transcript_branch_state",
			primaryKey: []string{"stream_uid"},
			foreignKeys: []contractForeignKey{
				{table: "transcript_streams", from: []string{"stream_uid"}, to: []string{"stream_uid"}, onDelete: "CASCADE"},
				{table: "transcript_branches", from: []string{"stream_uid", "active_branch_id"}, to: []string{"stream_uid", "branch_id"}, onDelete: "NO ACTION"},
			},
			clauses: []string{
				"foreign key(stream_uid,active_branch_id) references transcript_branches(stream_uid,branch_id) on delete no action deferrable initially deferred",
			},
		},
		{
			table:      "transcript_branch_events",
			primaryKey: []string{"stream_uid", "branch_id", "ordinal"},
			foreignKeys: []contractForeignKey{
				{table: "transcript_branches", from: []string{"stream_uid", "branch_id"}, to: []string{"stream_uid", "branch_id"}, onDelete: "CASCADE"},
				{table: "transcript_events", from: []string{"stream_uid", "event_id"}, to: []string{"stream_uid", "event_id"}, onDelete: "NO ACTION"},
			},
			clauses: []string{
				"foreign key(stream_uid,event_id) references transcript_events(stream_uid,event_id) on delete no action deferrable initially deferred",
			},
		},
	}
	for _, check := range checks {
		if err := validateContractPrimaryKey(ctx, tx, check.table, check.primaryKey); err != nil {
			return err
		}
		if err := validateContractForeignKeys(ctx, tx, check.table, check.foreignKeys); err != nil {
			return err
		}
		if err := validateContractTableClauses(ctx, tx, check.table, check.clauses); err != nil {
			return err
		}
	}
	if err := validateContractIndex(ctx, tx, "transcript_branch_events_event", []string{"stream_uid", "event_id", "branch_id"}); err != nil {
		return err
	}
	if err := validateContractUniqueIndex(ctx, tx, "transcript_branches", []string{"stream_uid", "client_mutation_id"}); err != nil {
		return err
	}
	return validateContractUniqueIndex(ctx, tx, "transcript_branch_events", []string{"stream_uid", "branch_id", "event_id"})
}

func validateContractPrimaryKey(ctx context.Context, tx *sql.Tx, table string, expected []string) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(`+quoteContractIdentifier(table)+`)`)
	if err != nil {
		return schemaError(err)
	}
	defer rows.Close()
	actual := make([]string, len(expected))
	primaryCount := 0
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return schemaError(err)
		}
		if primaryKey == 0 {
			continue
		}
		primaryCount++
		if primaryKey > len(actual) {
			return ErrSchemaUnavailable
		}
		actual[primaryKey-1] = name
	}
	if err := rows.Err(); err != nil {
		return schemaError(err)
	}
	if primaryCount != len(expected) || !contractStringSlicesEqual(actual, expected) {
		return ErrSchemaUnavailable
	}
	return nil
}

func validateContractForeignKeys(ctx context.Context, tx *sql.Tx, table string, expected []contractForeignKey) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_list(`+quoteContractIdentifier(table)+`)`)
	if err != nil {
		return schemaError(err)
	}
	defer rows.Close()
	groups := map[int]*observedContractForeignKey{}
	for rows.Next() {
		var id, sequence int
		var targetTable, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &sequence, &targetTable, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			return schemaError(err)
		}
		group := groups[id]
		if group == nil {
			group = &observedContractForeignKey{table: targetTable, onDelete: strings.ToUpper(onDelete)}
			groups[id] = group
		}
		if group.table != targetTable || group.onDelete != strings.ToUpper(onDelete) || sequence != len(group.from) {
			return ErrSchemaUnavailable
		}
		group.from = append(group.from, from)
		group.to = append(group.to, to)
	}
	if err := rows.Err(); err != nil {
		return schemaError(err)
	}
	if len(groups) != len(expected) {
		return ErrSchemaUnavailable
	}
	matched := make([]bool, len(expected))
	for _, actual := range groups {
		found := false
		for index, want := range expected {
			if matched[index] || actual.table != want.table || actual.onDelete != want.onDelete ||
				!contractStringSlicesEqual(actual.from, want.from) || !contractStringSlicesEqual(actual.to, want.to) {
				continue
			}
			matched[index] = true
			found = true
			break
		}
		if !found {
			return ErrSchemaUnavailable
		}
	}
	return nil
}

func validateContractTableClauses(ctx context.Context, tx *sql.Tx, table string, clauses []string) error {
	var statement string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&statement); err != nil {
		return schemaError(err)
	}
	normalized := normalizeContractSQL(statement)
	for _, clause := range clauses {
		if !strings.Contains(normalized, normalizeContractSQL(clause)) {
			return ErrSchemaUnavailable
		}
	}
	return nil
}

func validateContractIndex(ctx context.Context, tx *sql.Tx, index string, expected []string) error {
	actual, err := contractIndexColumns(ctx, tx, index)
	if err != nil {
		return err
	}
	if !contractStringSlicesEqual(actual, expected) {
		return ErrSchemaUnavailable
	}
	return nil
}

func validateContractUniqueIndex(ctx context.Context, tx *sql.Tx, table string, expected []string) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA index_list(`+quoteContractIdentifier(table)+`)`)
	if err != nil {
		return schemaError(err)
	}
	indexes := []string{}
	for rows.Next() {
		var sequence, unique, partial int
		var name, origin string
		if err := rows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
			_ = rows.Close()
			return schemaError(err)
		}
		if unique == 1 && partial == 0 {
			indexes = append(indexes, name)
		}
	}
	if err := rows.Close(); err != nil {
		return schemaError(err)
	}
	if err := rows.Err(); err != nil {
		return schemaError(err)
	}
	for _, index := range indexes {
		columns, err := contractIndexColumns(ctx, tx, index)
		if err != nil {
			return err
		}
		if contractStringSlicesEqual(columns, expected) {
			return nil
		}
	}
	return ErrSchemaUnavailable
}

func contractIndexColumns(ctx context.Context, tx *sql.Tx, index string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA index_info(`+quoteContractIdentifier(index)+`)`)
	if err != nil {
		return nil, schemaError(err)
	}
	defer rows.Close()
	columns := []string{}
	for rows.Next() {
		var sequence, cid int
		var name string
		if err := rows.Scan(&sequence, &cid, &name); err != nil {
			return nil, schemaError(err)
		}
		if sequence != len(columns) {
			return nil, ErrSchemaUnavailable
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		return nil, schemaError(err)
	}
	return columns, nil
}

func quoteContractIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func normalizeContractSQL(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func contractStringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
