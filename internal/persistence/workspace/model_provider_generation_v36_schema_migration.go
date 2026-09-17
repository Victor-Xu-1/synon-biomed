package workspace

import (
	"context"
	"errors"
)

const (
	modelProviderGenerationV36CallbackID        = "model-provider-generation-controls-v36-noop"
	modelProviderGenerationV36PreflightIdentity = "canonical-provider-v22-without-generation-controls-v1"
	modelProviderGenerationV36RuleSpec          = "synon.workspace.model-provider.generation-controls.v36"
)

var modelProviderGenerationV36Migration = versionedSchemaMigration{
	version: 36,
	name:    "model-provider-generation-controls",
	statements: []string{
		`ALTER TABLE model_providers ADD COLUMN temperature REAL
			CHECK (temperature IS NULL OR (typeof(temperature) IN ('integer','real') AND temperature >= 0 AND temperature <= 2))`,
		`ALTER TABLE model_providers ADD COLUMN max_tokens INTEGER
			CHECK (max_tokens IS NULL OR (typeof(max_tokens) = 'integer' AND max_tokens >= 1 AND max_tokens <= 1000000))`,
		`UPDATE model_providers
			SET temperature = (
				SELECT CAST(raw.legacy_temperature_raw AS REAL)
				FROM model_provider_legacy_raw raw
				WHERE raw.provider_id=model_providers.id
					AND raw.legacy_temperature_present=1
					AND typeof(raw.legacy_temperature_raw) IN ('integer','real')
					AND raw.legacy_temperature_raw >= 0
					AND raw.legacy_temperature_raw <= 2
			)
			WHERE temperature IS NULL AND EXISTS (
				SELECT 1 FROM model_provider_legacy_raw raw
				WHERE raw.provider_id=model_providers.id
					AND raw.legacy_temperature_present=1
					AND typeof(raw.legacy_temperature_raw) IN ('integer','real')
					AND raw.legacy_temperature_raw >= 0
					AND raw.legacy_temperature_raw <= 2
			)`,
		`UPDATE model_providers
			SET max_tokens = (
				SELECT raw.legacy_max_tokens_raw
				FROM model_provider_legacy_raw raw
				WHERE raw.provider_id=model_providers.id
					AND raw.legacy_max_tokens_present=1
					AND typeof(raw.legacy_max_tokens_raw) = 'integer'
					AND raw.legacy_max_tokens_raw >= 1
					AND raw.legacy_max_tokens_raw <= 1000000
			)
			WHERE max_tokens IS NULL AND EXISTS (
				SELECT 1 FROM model_provider_legacy_raw raw
				WHERE raw.provider_id=model_providers.id
					AND raw.legacy_max_tokens_present=1
					AND typeof(raw.legacy_max_tokens_raw) = 'integer'
					AND raw.legacy_max_tokens_raw >= 1
					AND raw.legacy_max_tokens_raw <= 1000000
			)`,
	},
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        modelProviderGenerationV36CallbackID,
		RuleSpec:          modelProviderGenerationV36RuleSpec,
		PreflightIdentity: modelProviderGenerationV36PreflightIdentity,
	},
}

func preflightModelProviderGenerationV36(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	shape, err := inspectProviderV22Shape(ctx, executor)
	if err != nil {
		return err
	}
	if !shape.canonical {
		return errors.New("provider v22 schema is required before generation controls")
	}
	return nil
}
