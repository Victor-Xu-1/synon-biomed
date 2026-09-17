package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSchemaRescueV21IdentityIsFrozen(t *testing.T) {
	checksum, err := schemaRescueV21Migration.validatedChecksum()
	if err != nil {
		t.Fatal(err)
	}
	const want = "81c39ca1ecd2ef31a374c670f15e4e1cbe73e104c2fd3a4e43d9caf979807384"
	if checksum != want {
		t.Fatalf("v21 checksum=%s want %s", checksum, want)
	}
}

func TestSchemaRescueV21IdentityRejectsUnknownHandlersAndRule(t *testing.T) {
	for name, mutate := range map[string]func(*schemaMigrationIdentityV2){
		"callback":  func(identity *schemaMigrationIdentityV2) { identity.CallbackID = "unknown" },
		"preflight": func(identity *schemaMigrationIdentityV2) { identity.PreflightIdentity = "unknown" },
		"rule":      func(identity *schemaMigrationIdentityV2) { identity.RuleSpec = "unknown" },
	} {
		t.Run(name, func(t *testing.T) {
			migration := schemaRescueV21Migration
			identity := *migration.identityV2
			mutate(&identity)
			migration.identityV2 = &identity
			migrations := append([]versionedSchemaMigration(nil), workspaceSchemaMigrations...)
			migrations[20] = migration
			if err := validateSchemaMigrationIdentityRegistry(migrations); err == nil || !strings.Contains(err.Error(), "handler is not registered") {
				t.Fatalf("registry error=%v", err)
			}
		})
	}
}

func TestSchemaRescueV21MigratesCleanShapeAndLeavesFutureRowsUnquarantined(t *testing.T) {
	db, path := openTargetTwentyProviderDB(t)
	insertProviderV21Fixture(t, db, "provider-a", "Shared", "2026-07-22T01:00:00Z")
	insertProviderV21Fixture(t, db, "provider-b", "Shared", "2026-07-22T02:00:00Z")

	applySchemaRescueV21(t, db)
	assertSchemaRescueV21Canonical(t, db, 2, 2)
	var presenceSum int
	if err := db.QueryRow(`SELECT SUM(legacy_protocol_present+legacy_temperature_present+legacy_max_tokens_present)
		FROM model_provider_legacy_raw`).Scan(&presenceSum); err != nil || presenceSum != 0 {
		t.Fatalf("legacy presence sum=%d err=%v", presenceSum, err)
	}
	insertProviderV21Fixture(t, db, "provider-c", "Shared", "2026-07-22T03:00:00Z")
	applySchemaRescueV21(t, db)
	assertSchemaRescueV21Canonical(t, db, 3, 2)
	beforeUpgrade := providerRowsFingerprint(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open(sqliteDriver, path)
	if err != nil {
		t.Fatal(err)
	}
	assertSchemaRescueV21Canonical(t, db, 3, 2)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open target workspace after v21 rescue: %v", err)
	}
	status, err := store.SchemaStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.CurrentVersion != workspaceSchemaVersion || status.TargetVersion != workspaceSchemaVersion {
		t.Fatalf("schema status after open = %#v", status)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open(sqliteDriver, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if afterUpgrade := providerRowsFingerprint(t, db); afterUpgrade != beforeUpgrade {
		t.Fatalf("target upgrade changed provider rows before=%q after=%q", beforeUpgrade, afterUpgrade)
	}
}

func TestSchemaRescueV21PreservesPollutedCellsWithoutConversion(t *testing.T) {
	db, _ := openTargetTwentyProviderDB(t)
	addProviderV21OptionalColumns(t, db, "protocol", "temperature", "max_tokens")
	if _, err := db.Exec(`INSERT INTO model_providers
		(id,user_id,name,type,base_url,model,secret_ref,enabled,created_at,updated_at,protocol,temperature,max_tokens)
		VALUES('provider','owner',' Mixed Name ','custom',' https://proxy.invalid/v1 ',' model ','secret-ref',0,
		'2026-07-22T01:00:00Z',CAST(x'323032362D30372D32325430323A30303A30305A' AS BLOB),
		'  Anthropic  ',CAST(x'00312E35FF' AS BLOB),1.5)`); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"source_updated_at_raw":  providerCellFingerprint(t, db, "model_providers", "updated_at", "provider"),
		"legacy_protocol_raw":    providerCellFingerprint(t, db, "model_providers", "protocol", "provider"),
		"legacy_temperature_raw": providerCellFingerprint(t, db, "model_providers", "temperature", "provider"),
		"legacy_max_tokens_raw":  providerCellFingerprint(t, db, "model_providers", "max_tokens", "provider"),
	}

	applySchemaRescueV21(t, db)
	assertSchemaRescueV21Canonical(t, db, 1, 1)
	for column, fingerprint := range want {
		if got := providerCellFingerprint(t, db, "model_provider_legacy_raw", column, "provider"); got != fingerprint {
			t.Errorf("%s fingerprint=%q want %q", column, got, fingerprint)
		}
	}
	var protocolPresent, temperaturePresent, maxTokensPresent int
	if err := db.QueryRow(`SELECT legacy_protocol_present,legacy_temperature_present,legacy_max_tokens_present
		FROM model_provider_legacy_raw WHERE provider_id='provider'`).Scan(&protocolPresent, &temperaturePresent, &maxTokensPresent); err != nil {
		t.Fatal(err)
	}
	if protocolPresent != 1 || temperaturePresent != 1 || maxTokensPresent != 1 {
		t.Fatalf("presence=(%d,%d,%d)", protocolPresent, temperaturePresent, maxTokensPresent)
	}
	var name, baseURL, model, secretRef string
	var enabled int
	if err := db.QueryRow(`SELECT name,base_url,model,secret_ref,enabled FROM model_providers WHERE id='provider'`).Scan(&name, &baseURL, &model, &secretRef, &enabled); err != nil {
		t.Fatal(err)
	}
	if name != " Mixed Name " || baseURL != " https://proxy.invalid/v1 " || model != " model " || secretRef != "secret-ref" || enabled != 0 {
		t.Fatalf("canonical values changed: name=%q url=%q model=%q secret=%q enabled=%d", name, baseURL, model, secretRef, enabled)
	}
}

func TestSchemaRescueV21AcceptsEveryKnownOptionalSubset(t *testing.T) {
	optionalNames := []string{"protocol", "temperature", "max_tokens"}
	for mask := 0; mask < 1<<len(optionalNames); mask++ {
		t.Run(fmt.Sprintf("mask_%d", mask), func(t *testing.T) {
			db, _ := openTargetTwentyProviderDB(t)
			selected := make([]string, 0, len(optionalNames))
			for index, name := range optionalNames {
				if mask&(1<<index) != 0 {
					selected = append(selected, name)
				}
			}
			addProviderV21OptionalColumns(t, db, selected...)
			insertProviderV21Fixture(t, db, "provider", "Provider", "2026-07-22T01:00:00Z")
			applySchemaRescueV21(t, db)
			assertSchemaRescueV21Canonical(t, db, 1, 1)
			var got [3]int
			if err := db.QueryRow(`SELECT legacy_protocol_present,legacy_temperature_present,legacy_max_tokens_present
				FROM model_provider_legacy_raw`).Scan(&got[0], &got[1], &got[2]); err != nil {
				t.Fatal(err)
			}
			for index := range optionalNames {
				want := 0
				if mask&(1<<index) != 0 {
					want = 1
				}
				if got[index] != want {
					t.Errorf("%s presence=%d want %d", optionalNames[index], got[index], want)
				}
			}
		})
	}
}

func TestSchemaRescueV21AcceptsObservedDirtyFreshColumnOrder(t *testing.T) {
	db, _ := openTargetTwentyProviderDB(t)
	if _, err := db.Exec(`DROP INDEX model_providers_user_updated_idx; DROP TABLE model_providers;
		CREATE TABLE model_providers (
			id TEXT PRIMARY KEY,user_id TEXT NOT NULL,name TEXT NOT NULL,type TEXT NOT NULL,
			protocol TEXT NOT NULL,base_url TEXT NOT NULL,model TEXT NOT NULL,
			secret_ref TEXT NOT NULL DEFAULT '',enabled INTEGER NOT NULL DEFAULT 1,
			temperature REAL,max_tokens INTEGER,created_at TIMESTAMP NOT NULL,updated_at TIMESTAMP NOT NULL);
		CREATE INDEX model_providers_user_updated_idx ON model_providers(user_id,updated_at DESC,id DESC)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_providers
		(id,user_id,name,type,protocol,base_url,model,secret_ref,enabled,temperature,max_tokens,created_at,updated_at)
		VALUES('provider','owner','Provider','custom','anthropic','https://provider.invalid/v1','model','secret-ref',1,NULL,NULL,
		'2026-07-22T00:00:00Z','2026-07-22T01:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	applySchemaRescueV21(t, db)
	assertSchemaRescueV21Canonical(t, db, 1, 1)
}

func TestSchemaRescueV21RemovesOnlyKnownLegacyNameUniqueness(t *testing.T) {
	db, _ := openTargetTwentyProviderDB(t)
	if _, err := db.Exec(`DROP INDEX model_providers_user_updated_idx; DROP TABLE model_providers;
		CREATE TABLE model_providers (
			id TEXT PRIMARY KEY,user_id TEXT NOT NULL,name TEXT NOT NULL,type TEXT NOT NULL,
			base_url TEXT NOT NULL,model TEXT NOT NULL,secret_ref TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,created_at TIMESTAMP NOT NULL,updated_at TIMESTAMP NOT NULL,
			UNIQUE(user_id,name));
		CREATE INDEX model_providers_user_updated_idx ON model_providers(user_id,updated_at DESC,id DESC)`); err != nil {
		t.Fatal(err)
	}
	insertProviderV21Fixture(t, db, "provider-a", "Shared", "2026-07-22T01:00:00Z")
	applySchemaRescueV21(t, db)
	insertProviderV21Fixture(t, db, "provider-b", "Shared", "2026-07-22T02:00:00Z")
	assertSchemaRescueV21Canonical(t, db, 2, 1)
}

func TestSchemaRescueV21RestoresMissingCanonicalIndex(t *testing.T) {
	db, _ := openTargetTwentyProviderDB(t)
	if _, err := db.Exec(`DROP INDEX model_providers_user_updated_idx`); err != nil {
		t.Fatal(err)
	}
	insertProviderV21Fixture(t, db, "provider", "Provider", "2026-07-22T01:00:00Z")
	applySchemaRescueV21(t, db)
	assertSchemaRescueV21Canonical(t, db, 1, 1)
}

func TestSchemaRescueV21RejectsUnknownShapesBeforeWrite(t *testing.T) {
	tests := map[string]func(*testing.T, *sql.DB){
		"extra column": func(t *testing.T, db *sql.DB) {
			_, err := db.Exec(`ALTER TABLE model_providers ADD COLUMN surprise TEXT`)
			if err != nil {
				t.Fatal(err)
			}
		},
		"explicit index": func(t *testing.T, db *sql.DB) {
			_, err := db.Exec(`CREATE INDEX model_providers_name_idx ON model_providers(name)`)
			if err != nil {
				t.Fatal(err)
			}
		},
		"trigger": func(t *testing.T, db *sql.DB) {
			_, err := db.Exec(`CREATE TRIGGER model_providers_guard BEFORE UPDATE ON model_providers BEGIN SELECT RAISE(ABORT,'guard'); END`)
			if err != nil {
				t.Fatal(err)
			}
		},
		"staging collision": func(t *testing.T, db *sql.DB) {
			_, err := db.Exec(`CREATE TABLE model_providers_v21_new(id TEXT)`)
			if err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			db, _ := openTargetTwentyProviderDB(t)
			insertProviderV21Fixture(t, db, "provider", "Provider", "2026-07-22T01:00:00Z")
			mutate(t, db)
			before := providerSchemaFingerprint(t, db)
			err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 21)
			if err == nil || !strings.Contains(err.Error(), "preflight workspace schema migration 21") {
				t.Fatalf("migration error=%v", err)
			}
			if after := providerSchemaFingerprint(t, db); after != before {
				t.Fatalf("schema changed before=%q after=%q", before, after)
			}
			assertSchemaJournalVersion(t, db, 20)
		})
	}
}

func TestSchemaRescueV21RollsBackEveryRebuildStage(t *testing.T) {
	for _, stage := range []string{"validated-source", "created-staging", "copied-and-verified", "installed-canonical-schema"} {
		t.Run(stage, func(t *testing.T) {
			db, path := openTargetTwentyProviderDB(t)
			addProviderV21OptionalColumns(t, db, "protocol", "temperature", "max_tokens")
			insertProviderV21Fixture(t, db, "provider", "Provider", "2026-07-22T01:00:00Z")
			before := providerSchemaFingerprint(t, db)
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			sentinel := errors.New("fault injection")
			err = normalizeModelProvidersV21(context.Background(), tx, func(current string) error {
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
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = sql.Open(sqliteDriver, path)
			if err != nil {
				t.Fatal(err)
			}
			if after := providerSchemaFingerprint(t, db); after != before {
				t.Fatalf("schema changed after rollback before=%q after=%q", before, after)
			}
			assertSchemaJournalVersion(t, db, 20)
			var staging int
			if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name IN
				('model_providers_v21_new','model_provider_legacy_raw_v21_staging')`).Scan(&staging); err != nil || staging != 0 {
				t.Fatalf("staging objects=%d err=%v", staging, err)
			}
		})
	}
}

func TestSchemaRescueV21RecoversCanonicalShapeWithoutJournal(t *testing.T) {
	db, _ := openTargetTwentyProviderDB(t)
	insertProviderV21Fixture(t, db, "provider", "Provider", "2026-07-22T01:00:00Z")
	applySchemaRescueV21(t, db)
	if _, err := db.Exec(`DELETE FROM workspace_schema_migrations WHERE version=21`); err != nil {
		t.Fatal(err)
	}
	applySchemaRescueV21(t, db)
	assertSchemaRescueV21Canonical(t, db, 1, 1)
}

func openTargetTwentyProviderDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workspace.sqlite")
	seedTargetThreeAuthorityFixture(t, path)
	db, err := sql.Open(sqliteDriver, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 20); err != nil {
		db.Close()
		t.Fatalf("prepare target 20: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

func addProviderV21OptionalColumns(t *testing.T, db *sql.DB, names ...string) {
	t.Helper()
	statements := map[string]string{
		"protocol":    `ALTER TABLE model_providers ADD COLUMN protocol TEXT NOT NULL DEFAULT ''`,
		"temperature": `ALTER TABLE model_providers ADD COLUMN temperature REAL`,
		"max_tokens":  `ALTER TABLE model_providers ADD COLUMN max_tokens INTEGER`,
	}
	for _, name := range names {
		statement, ok := statements[name]
		if !ok {
			t.Fatalf("unknown optional column %q", name)
		}
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func insertProviderV21Fixture(t *testing.T, db *sql.DB, id, name, updatedAt string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO model_providers
		(id,user_id,name,type,base_url,model,secret_ref,enabled,created_at,updated_at)
		VALUES(?, 'owner', ?, 'custom', 'https://provider.invalid/v1', 'model', 'secret-ref', 1,
		'2026-07-22T00:00:00Z', ?)`, id, name, updatedAt); err != nil {
		t.Fatal(err)
	}
}

func applySchemaRescueV21(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 21); err != nil {
		t.Fatal(err)
	}
}

func assertSchemaRescueV21Canonical(t *testing.T, db *sql.DB, mainRows, rawRows int) {
	t.Helper()
	assertSchemaJournalVersion(t, db, 21)
	columns, _, err := readSQLiteTableShape(context.Background(), db, "model_providers")
	if err != nil {
		t.Fatal(err)
	}
	if optional, err := validateModelProviderColumns(columns); err != nil || len(optional) != 0 {
		t.Fatalf("canonical columns optional=%v err=%v", optional, err)
	}
	var gotMain, gotRaw int
	if err := db.QueryRow(`SELECT COUNT(*) FROM model_providers`).Scan(&gotMain); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM model_provider_legacy_raw`).Scan(&gotRaw); err != nil {
		t.Fatal(err)
	}
	if gotMain != mainRows || gotRaw != rawRows {
		t.Fatalf("row counts main=%d raw=%d want %d/%d", gotMain, gotRaw, mainRows, rawRows)
	}
	var quick string
	if err := db.QueryRow(`PRAGMA quick_check`).Scan(&quick); err != nil || quick != "ok" {
		t.Fatalf("quick_check=%q err=%v", quick, err)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation after schema rescue")
	}
}

func assertSchemaJournalVersion(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM workspace_schema_migrations`).Scan(&version); err != nil || version != want {
		t.Fatalf("journal version=%d want %d err=%v", version, want, err)
	}
}

func providerCellFingerprint(t *testing.T, db *sql.DB, table, column, providerID string) string {
	t.Helper()
	idColumn := "id"
	if table == "model_provider_legacy_raw" {
		idColumn = "provider_id"
	}
	query := fmt.Sprintf(`SELECT printf('%%s|%%s|%%s',typeof(%s),quote(%s),hex(%s)) FROM %s WHERE %s=?`,
		column, column, column, table, idColumn)
	var fingerprint string
	if err := db.QueryRow(query, providerID).Scan(&fingerprint); err != nil {
		t.Fatal(err)
	}
	return fingerprint
}

func providerSchemaFingerprint(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`SELECT type||':'||name||':'||coalesce(sql,'') FROM sqlite_schema
		WHERE name LIKE 'model_provider%' ORDER BY type,name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	parts := make([]string, 0)
	for rows.Next() {
		var part string
		if err := rows.Scan(&part); err != nil {
			t.Fatal(err)
		}
		parts = append(parts, part)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var rowsFingerprint string
	if err := db.QueryRow(`SELECT coalesce(group_concat(id||':'||quote(secret_ref)||':'||quote(updated_at),'|'),'')
		FROM (SELECT * FROM model_providers ORDER BY id)`).Scan(&rowsFingerprint); err != nil {
		t.Fatal(err)
	}
	return strings.Join(parts, "\n") + "\nrows=" + rowsFingerprint
}

func providerRowsFingerprint(t *testing.T, db *sql.DB) string {
	t.Helper()
	var fingerprint string
	if err := db.QueryRow(`SELECT coalesce(group_concat(id||':'||quote(secret_ref)||':'||quote(updated_at),'|'),'')
		FROM (SELECT * FROM model_providers ORDER BY id)`).Scan(&fingerprint); err != nil {
		t.Fatal(err)
	}
	return fingerprint
}
