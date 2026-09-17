package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestProviderV22IdentityIsFrozen(t *testing.T) {
	checksum, err := providerV22Migration.validatedChecksum()
	if err != nil {
		t.Fatal(err)
	}
	const want = "5c3f8024e1f7b2b25011b5f65fafe2267560ad3d41a768094ee7ce7ea713a8d6"
	if checksum != want {
		t.Fatalf("v22 checksum=%s want %s", checksum, want)
	}
}

func TestProviderV22ClassifiesOnlyExplicitOrUnambiguousLegacySignals(t *testing.T) {
	db := openTargetTwentyOneProviderDB(t)
	tests := []struct {
		id, providerType, baseURL string
		want                      providerV22Resolution
	}{
		{"deepseek", "deepseek", "https://api.deepseek.com/v1", providerV22Resolution{"openai-compatible", "ready", "legacy_exact_type"}},
		{"responses", "openai-responses", "https://api.openai.com/v1/responses", providerV22Resolution{"openai-responses", "ready", "legacy_exact_type"}},
		{"anthropic", "anthropic", "https://api.anthropic.com", providerV22Resolution{"anthropic", "ready", "legacy_exact_type"}},
		{"gemini", "gemini", "https://generativelanguage.googleapis.com", providerV22Resolution{"gemini", "ready", "legacy_exact_type"}},
		{"azure", "azure-openai", "https://tenant.openai.azure.com/openai/deployments/model", providerV22Resolution{"azure-openai", "ready", "legacy_exact_type"}},
		{"official", "", "https://api.deepseek.com/v1", providerV22Resolution{"openai-compatible", "ready", "legacy_official_endpoint"}},
		{"official-responses", "", "https://api.openai.com/v1/responses", providerV22Resolution{"openai-responses", "ready", "legacy_official_endpoint"}},
		{"bedrock", "bedrock", "https://bedrock-runtime.us-east-1.amazonaws.com", providerV22Resolution{"", "unsupported", "unsupported_bedrock"}},
		{"vertex", "", "https://us-central1-aiplatform.googleapis.com/v1", providerV22Resolution{"", "unsupported", "unsupported_vertex"}},
		{"new-api", "new-api", "https://api.openai.com/v1", providerV22Resolution{"", "unresolved", "legacy_ambiguous"}},
		{"unknown", "private-label", "https://gateway.example.invalid/v1", providerV22Resolution{"", "unresolved", "legacy_ambiguous"}},
		{"conflict", "custom", "https://api.anthropic.com", providerV22Resolution{"", "unresolved", "legacy_ambiguous"}},
		{"malformed", "openai", "://not-a-url", providerV22Resolution{"", "unresolved", "legacy_ambiguous"}},
		{"suffix-attack", "", "https://api.openai.com.attacker.invalid/v1", providerV22Resolution{"", "unresolved", "legacy_ambiguous"}},
	}
	for _, test := range tests {
		insertProviderV22Fixture(t, db, test.id, test.providerType, test.baseURL, "secret://"+test.id)
	}
	applyProviderV22(t, db)
	for _, test := range tests {
		var got providerV22Resolution
		if err := db.QueryRow(`SELECT protocol,configuration_status,configuration_code FROM model_providers WHERE id=?`, test.id).
			Scan(&got.protocol, &got.status, &got.code); err != nil {
			t.Fatal(err)
		}
		if got != test.want {
			t.Errorf("%s resolution=%#v want %#v", test.id, got, test.want)
		}
	}
}

func TestProviderV22PreservesProviderAndQuarantineAuthority(t *testing.T) {
	db, _ := openTargetTwentyProviderDB(t)
	insertProviderV22Fixture(t, db, "provider", "deepseek", "https://api.deepseek.com/v1", " secret://opaque ")
	applySchemaRescueV21(t, db)
	beforeMain := providerV22MainFingerprint(t, db, "provider")
	beforeRaw := providerV22RawFingerprint(t, db, "provider")
	applyProviderV22(t, db)
	beforeSecondRun := providerV22MainFingerprint(t, db, "provider")
	applyProviderV22(t, db)
	if afterSecondRun := providerV22MainFingerprint(t, db, "provider"); afterSecondRun != beforeSecondRun {
		t.Fatalf("idempotent migration changed provider before=%q after=%q", beforeSecondRun, afterSecondRun)
	}
	if got := providerV22MainFingerprint(t, db, "provider"); got != beforeMain {
		t.Fatalf("main authority changed before=%q after=%q", beforeMain, got)
	}
	if got := providerV22RawFingerprint(t, db, "provider"); got != beforeRaw {
		t.Fatalf("raw quarantine changed before=%q after=%q", beforeRaw, got)
	}
	var owner, secretRef string
	var enabled bool
	if err := db.QueryRow(`SELECT user_id,secret_ref,enabled FROM model_providers WHERE id='provider'`).Scan(&owner, &secretRef, &enabled); err != nil {
		t.Fatal(err)
	}
	if owner != "owner" || secretRef != " secret://opaque " || !enabled {
		t.Fatalf("preserved authority owner=%q secret=%q enabled=%v", owner, secretRef, enabled)
	}
}

func TestProviderV22DefaultsNewRowsToFailClosedUnresolved(t *testing.T) {
	db := openTargetTwentyOneProviderDB(t)
	applyProviderV22(t, db)
	insertProviderV22Fixture(t, db, "new", "custom", "https://gateway.example.invalid/v1", "secret://new")
	var protocol, status, code string
	if err := db.QueryRow(`SELECT protocol,configuration_status,configuration_code FROM model_providers WHERE id='new'`).Scan(&protocol, &status, &code); err != nil {
		t.Fatal(err)
	}
	if protocol != "" || status != "unresolved" || code != "" {
		t.Fatalf("new provider defaults=(%q,%q,%q)", protocol, status, code)
	}
	if _, err := db.Exec(`UPDATE model_providers SET protocol='unknown' WHERE id='new'`); err == nil {
		t.Fatal("unknown protocol bypassed database constraint")
	}
	if _, err := db.Exec(`UPDATE model_providers SET configuration_code='secret text' WHERE id='new'`); err == nil {
		t.Fatal("unbounded configuration code bypassed database constraint")
	}
}

func TestProviderV22RejectsPartialSchemaBeforeClassification(t *testing.T) {
	db := openTargetTwentyOneProviderDB(t)
	insertProviderV22Fixture(t, db, "provider", "deepseek", "https://api.deepseek.com/v1", "secret://provider")
	if _, err := db.Exec(`ALTER TABLE model_providers ADD COLUMN protocol TEXT NOT NULL DEFAULT ''`); err != nil {
		t.Fatal(err)
	}
	before := providerV22MainFingerprint(t, db, "provider")
	err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 22)
	if err == nil || !strings.Contains(err.Error(), "partial provider v22 schema") {
		t.Fatalf("migration error=%v", err)
	}
	if after := providerV22MainFingerprint(t, db, "provider"); after != before {
		t.Fatalf("provider changed before=%q after=%q", before, after)
	}
	assertSchemaJournalVersion(t, db, 21)
}

func TestProviderV22RollsBackEveryClassificationStage(t *testing.T) {
	for _, stage := range []string{"validated-v21-source", "installed-v22-columns", "classified-v22-rows"} {
		t.Run(stage, func(t *testing.T) {
			db := openTargetTwentyOneProviderDB(t)
			insertProviderV22Fixture(t, db, "provider", "deepseek", "https://api.deepseek.com/v1", "secret://provider")
			before := providerV22MainFingerprint(t, db, "provider")
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			sentinel := errors.New("fault injection")
			err = normalizeProvidersV22(context.Background(), tx, func(current string) error {
				if current == stage {
					return sentinel
				}
				return nil
			})
			if !errors.Is(err, sentinel) {
				t.Fatalf("callback error=%v", err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if after := providerV22MainFingerprint(t, db, "provider"); after != before {
				t.Fatalf("provider changed after rollback before=%q after=%q", before, after)
			}
			var v22Columns int
			if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('model_providers')
				WHERE name IN ('protocol','configuration_status','configuration_code')`).Scan(&v22Columns); err != nil || v22Columns != 0 {
				t.Fatalf("v22 columns=%d err=%v", v22Columns, err)
			}
		})
	}
}

func TestProviderV22RecoversCanonicalShapeWithoutJournal(t *testing.T) {
	db := openTargetTwentyOneProviderDB(t)
	insertProviderV22Fixture(t, db, "provider", "deepseek", "https://api.deepseek.com/v1", "secret://provider")
	applyProviderV22(t, db)
	if _, err := db.Exec(`DELETE FROM workspace_schema_migrations WHERE version=22`); err != nil {
		t.Fatal(err)
	}
	applyProviderV22(t, db)
	assertSchemaJournalVersion(t, db, 22)
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM model_providers WHERE id='provider' AND protocol='openai-compatible'
		AND configuration_status='ready' AND configuration_code='legacy_exact_type'`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("recovered rows=%d err=%v", rows, err)
	}
}

func TestProviderV22RecoveryRejectsSemanticallyCorruptRows(t *testing.T) {
	db := openTargetTwentyOneProviderDB(t)
	insertProviderV22Fixture(t, db, "provider", "deepseek", "https://api.deepseek.com/v1", "secret://provider")
	applyProviderV22(t, db)
	if _, err := db.Exec(`DELETE FROM workspace_schema_migrations WHERE version=22`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA ignore_check_constraints=ON; UPDATE model_providers
		SET configuration_status='corrupt',configuration_code='legacy_exact_type' WHERE id='provider'; PRAGMA ignore_check_constraints=OFF`); err != nil {
		t.Fatal(err)
	}
	err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 22)
	if err == nil || !strings.Contains(err.Error(), "classification is invalid") {
		t.Fatalf("migration error=%v", err)
	}
	assertSchemaJournalVersion(t, db, 21)
}

func openTargetTwentyOneProviderDB(t *testing.T) *sql.DB {
	t.Helper()
	db, _ := openTargetTwentyProviderDB(t)
	applySchemaRescueV21(t, db)
	return db
}

func insertProviderV22Fixture(t *testing.T, db *sql.DB, id, providerType, baseURL, secretRef string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO model_providers
		(id,user_id,name,type,base_url,model,secret_ref,enabled,created_at,updated_at)
		VALUES(?, 'owner', ?, ?, ?, 'model', ?, 1, '2026-07-22T00:00:00Z', '2026-07-22T01:00:00Z')`,
		id, "Provider "+id, providerType, baseURL, secretRef); err != nil {
		t.Fatal(err)
	}
}

func applyProviderV22(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 22); err != nil {
		t.Fatal(err)
	}
}

func providerV22MainFingerprint(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var fingerprint string
	if err := db.QueryRow(`SELECT printf('%s|%s|%s|%s|%s|%s|%s|%d|%s|%s',
		id,user_id,name,type,base_url,model,quote(secret_ref),enabled,quote(created_at),quote(updated_at))
		FROM model_providers WHERE id=?`, id).Scan(&fingerprint); err != nil {
		t.Fatal(err)
	}
	return fingerprint
}

func providerV22RawFingerprint(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	columns := []string{"source_updated_at_raw", "legacy_protocol_raw", "legacy_temperature_raw", "legacy_max_tokens_raw"}
	parts := make([]string, 0, len(columns))
	for _, column := range columns {
		parts = append(parts, providerCellFingerprint(t, db, "model_provider_legacy_raw", column, id))
	}
	var presence string
	if err := db.QueryRow(`SELECT printf('%d|%d|%d',legacy_protocol_present,legacy_temperature_present,legacy_max_tokens_present)
		FROM model_provider_legacy_raw WHERE provider_id=?`, id).Scan(&presence); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%s|%s", strings.Join(parts, ";"), presence)
}
