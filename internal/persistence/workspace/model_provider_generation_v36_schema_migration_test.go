package workspace

import (
	"context"
	"testing"
	"time"
)

func TestModelProviderGenerationV36BackfillsOnlyTypedValidLegacyValues(t *testing.T) {
	db, _ := openTargetTwentyTwoProviderDB(t)
	for version := 23; version <= 35; version++ {
		applyTranscriptMigration(t, db, version)
	}
	for _, provider := range []string{"valid", "text", "range", "fractional"} {
		insertProviderV22Fixture(t, db, provider, "custom", "https://provider.invalid/v1", "secret-ref")
	}
	fixtures := []struct {
		providerID  string
		temperature any
		maxTokens   any
	}{
		{providerID: "valid", temperature: 0.45, maxTokens: int64(32768)},
		{providerID: "text", temperature: "0.6", maxTokens: "4096"},
		{providerID: "range", temperature: 3.0, maxTokens: int64(0)},
		{providerID: "fractional", temperature: 1, maxTokens: 2048.5},
	}
	for _, fixture := range fixtures {
		if _, err := db.Exec(`INSERT INTO model_provider_legacy_raw
			(provider_id,source_updated_at_raw,legacy_protocol_present,legacy_protocol_raw,
			 legacy_temperature_present,legacy_temperature_raw,legacy_max_tokens_present,legacy_max_tokens_raw)
			VALUES(?,CURRENT_TIMESTAMP,0,NULL,1,?,1,?)`, fixture.providerID, fixture.temperature, fixture.maxTokens); err != nil {
			t.Fatalf("insert %s legacy values: %v", fixture.providerID, err)
		}
	}

	if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 36); err != nil {
		t.Fatal(err)
	}
	var temperature *float64
	var maxTokens *int
	if err := db.QueryRow(`SELECT temperature,max_tokens FROM model_providers WHERE id='valid'`).Scan(&temperature, &maxTokens); err != nil {
		t.Fatal(err)
	}
	if temperature == nil || *temperature != 0.45 || maxTokens == nil || *maxTokens != 32768 {
		t.Fatalf("valid backfill temperature=%v maxTokens=%v", temperature, maxTokens)
	}
	for _, provider := range []string{"text", "range"} {
		temperature, maxTokens = nil, nil
		if err := db.QueryRow(`SELECT temperature,max_tokens FROM model_providers WHERE id=?`, provider).Scan(&temperature, &maxTokens); err != nil {
			t.Fatal(err)
		}
		if temperature != nil || maxTokens != nil {
			t.Fatalf("provider %s unexpectedly backfilled temperature=%v maxTokens=%v", provider, temperature, maxTokens)
		}
	}
	temperature, maxTokens = nil, nil
	if err := db.QueryRow(`SELECT temperature,max_tokens FROM model_providers WHERE id='fractional'`).Scan(&temperature, &maxTokens); err != nil {
		t.Fatal(err)
	}
	if temperature == nil || *temperature != 1 || maxTokens != nil {
		t.Fatalf("fractional max token fixture temperature=%v maxTokens=%v", temperature, maxTokens)
	}
	for _, statement := range []string{
		`UPDATE model_providers SET temperature=2.1 WHERE id='valid'`,
		`UPDATE model_providers SET max_tokens=0 WHERE id='valid'`,
		`UPDATE model_providers SET max_tokens=2048.5 WHERE id='valid'`,
	} {
		if _, err := db.Exec(statement); err == nil {
			t.Fatalf("invalid generation control accepted: %s", statement)
		}
	}
	if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 36); err != nil {
		t.Fatalf("reopen v36 migration: %v", err)
	}
	assertSchemaJournalVersion(t, db, 36)
}
