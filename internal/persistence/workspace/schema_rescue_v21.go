package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const (
	schemaRescueV21CallbackID        = "normalize-model-providers-v21"
	schemaRescueV21PreflightIdentity = "model-provider-shape-v1"
	schemaRescueV21RuleSpec          = "synon.workspace.schema-rescue.model-providers.v21"
)

var schemaRescueV21Migration = versionedSchemaMigration{
	version: 21,
	name:    "canonical-model-provider-schema-rescue",
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        schemaRescueV21CallbackID,
		RuleSpec:          schemaRescueV21RuleSpec,
		PreflightIdentity: schemaRescueV21PreflightIdentity,
	},
}

type schemaMigrationQueryExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type modelProviderV21Shape struct {
	optionalColumns map[string]bool
	canonicalV21    bool
}

type sqliteColumnShape struct {
	name         string
	declaredType string
	notNull      bool
	defaultValue sql.NullString
	primaryKey   int
	hidden       int
}

type schemaRescueV21StageHook func(string) error

func schemaMigrationIdentityHandlersKnown(identity schemaMigrationIdentityV2) bool {
	switch identity.CallbackID {
	case schemaRescueV21CallbackID:
		return identity.PreflightIdentity == schemaRescueV21PreflightIdentity && identity.RuleSpec == schemaRescueV21RuleSpec
	case providerV22CallbackID:
		return identity.PreflightIdentity == providerV22PreflightIdentity && identity.RuleSpec == providerV22RuleSpec
	case transcriptSchemaNoopCallbackID:
		return transcriptSchemaIdentityKnown(identity)
	case transcriptV25CallbackID:
		return transcriptSchemaIdentityKnown(identity)
	case transcriptHistoryActivationV31CallbackID:
		return identity.PreflightIdentity == transcriptHistoryActivationV31PreflightIdentity &&
			identity.RuleSpec == transcriptHistoryActivationV31RuleSpec
	case modelProviderGenerationV36CallbackID:
		return identity.PreflightIdentity == modelProviderGenerationV36PreflightIdentity &&
			identity.RuleSpec == modelProviderGenerationV36RuleSpec
	case scientificComputeAuthorityV37CallbackID:
		return identity.PreflightIdentity == scientificComputeAuthorityV37PreflightIdentity &&
			identity.RuleSpec == scientificComputeAuthorityV37RuleSpec
	case transcriptWebReadModelV38CallbackID:
		return identity.PreflightIdentity == transcriptWebReadModelV38PreflightIdentity &&
			identity.RuleSpec == transcriptWebReadModelV38RuleSpec
	case kernelLocalOperationV39CallbackID:
		return identity.PreflightIdentity == kernelLocalOperationV39PreflightIdentity &&
			identity.RuleSpec == kernelLocalOperationV39RuleSpec
	case toolCallBatchV40CallbackID:
		return identity.PreflightIdentity == toolCallBatchV40PreflightIdentity &&
			identity.RuleSpec == toolCallBatchV40RuleSpec
	case kernelToolResultV41CallbackID:
		return identity.PreflightIdentity == kernelToolResultV41PreflightIdentity &&
			identity.RuleSpec == kernelToolResultV41RuleSpec
	case scientificComputeSubmissionV42CallbackID:
		return identity.PreflightIdentity == scientificComputeSubmissionV42PreflightIdentity &&
			identity.RuleSpec == scientificComputeSubmissionV42RuleSpec
	case artifactCollectionV43CallbackID:
		return identity.PreflightIdentity == artifactCollectionV43PreflightIdentity &&
			identity.RuleSpec == artifactCollectionV43RuleSpec
	case kernelDetachedExecutionV44CallbackID:
		return identity.PreflightIdentity == kernelDetachedExecutionV44PreflightIdentity &&
			identity.RuleSpec == kernelDetachedExecutionV44RuleSpec
	case kernelDetachedExecutionV45CallbackID:
		return identity.PreflightIdentity == kernelDetachedExecutionV45PreflightIdentity &&
			identity.RuleSpec == kernelDetachedExecutionV45RuleSpec
	case runtimeAuditDeleteV46CallbackID:
		return identity.PreflightIdentity == runtimeAuditDeleteV46PreflightIdentity &&
			identity.RuleSpec == runtimeAuditDeleteV46RuleSpec
	case transcriptWebProjectorV47CallbackID:
		return identity.PreflightIdentity == transcriptWebProjectorV47PreflightIdentity &&
			identity.RuleSpec == transcriptWebProjectorV47RuleSpec
	case transcriptWebProjectorV48CallbackID:
		return identity.PreflightIdentity == transcriptWebProjectorV48PreflightIdentity &&
			identity.RuleSpec == transcriptWebProjectorV48RuleSpec
	case transcriptWebProjectorV49CallbackID:
		return identity.PreflightIdentity == transcriptWebProjectorV49PreflightIdentity &&
			identity.RuleSpec == transcriptWebProjectorV49RuleSpec
	case runnerLargeToolResultV50CallbackID:
		return identity.PreflightIdentity == runnerLargeToolResultV50PreflightIdentity &&
			identity.RuleSpec == runnerLargeToolResultV50RuleSpec
	case kernelDetachedExecutionV51CallbackID:
		return identity.PreflightIdentity == kernelDetachedExecutionV51PreflightIdentity &&
			identity.RuleSpec == kernelDetachedExecutionV51RuleSpec
	case runnerLargeToolResultV52CallbackID:
		return identity.PreflightIdentity == runnerLargeToolResultV52PreflightIdentity &&
			identity.RuleSpec == runnerLargeToolResultV52RuleSpec
	case mcpToolCatalogV53CallbackID:
		return identity.PreflightIdentity == mcpToolCatalogV53PreflightIdentity &&
			identity.RuleSpec == mcpToolCatalogV53RuleSpec
	case transcriptDeliveryConvergenceV54CallbackID:
		return identity.PreflightIdentity == transcriptDeliveryConvergenceV54PreflightIdentity &&
			identity.RuleSpec == transcriptDeliveryConvergenceV54RuleSpec
	case kernelSoftwareRuntimeV55CallbackID:
		return identity.PreflightIdentity == kernelSoftwareRuntimeV55PreflightIdentity &&
			identity.RuleSpec == kernelSoftwareRuntimeV55RuleSpec
	case kernelBashV56CallbackID:
		return identity.PreflightIdentity == kernelBashV56PreflightIdentity &&
			identity.RuleSpec == kernelBashV56RuleSpec
	case toolBatchV57CallbackID:
		return identity.PreflightIdentity == toolBatchV57PreflightIdentity &&
			identity.RuleSpec == toolBatchV57RuleSpec
	case transcriptWebProjectorV58CallbackID:
		return identity.PreflightIdentity == transcriptWebProjectorV58PreflightIdentity &&
			identity.RuleSpec == transcriptWebProjectorV58RuleSpec
	case transcriptWebProjectorV59CallbackID:
		return identity.PreflightIdentity == transcriptWebProjectorV59PreflightIdentity &&
			identity.RuleSpec == transcriptWebProjectorV59RuleSpec
	case transcriptWebProjectorV60CallbackID:
		return identity.PreflightIdentity == transcriptWebProjectorV60PreflightIdentity &&
			identity.RuleSpec == transcriptWebProjectorV60RuleSpec
	case generatedPlanRetentionV61CallbackID:
		return identity.PreflightIdentity == generatedPlanRetentionV61PreflightIdentity &&
			identity.RuleSpec == generatedPlanRetentionV61RuleSpec
	case kernelTrustedMountV62CallbackID:
		return identity.PreflightIdentity == kernelTrustedMountV62PreflightIdentity &&
			identity.RuleSpec == kernelTrustedMountV62RuleSpec
	case kernelResultSpoolV63CallbackID:
		return identity.PreflightIdentity == kernelResultSpoolV63PreflightIdentity &&
			identity.RuleSpec == kernelResultSpoolV63RuleSpec
	case transcriptWebProjectorV64CallbackID:
		return identity.PreflightIdentity == transcriptWebProjectorV64PreflightIdentity &&
			identity.RuleSpec == transcriptWebProjectorV64RuleSpec
	case transcriptWebProjectorV65CallbackID:
		return identity.PreflightIdentity == transcriptWebProjectorV65PreflightIdentity &&
			identity.RuleSpec == transcriptWebProjectorV65RuleSpec
	case transcriptWebProjectorV66CallbackID:
		return identity.PreflightIdentity == transcriptWebProjectorV66PreflightIdentity &&
			identity.RuleSpec == transcriptWebProjectorV66RuleSpec
	case transcriptWebProjectorV67CallbackID:
		return identity.PreflightIdentity == transcriptWebProjectorV67PreflightIdentity &&
			identity.RuleSpec == transcriptWebProjectorV67RuleSpec
	default:
		return false
	}
}

func runSchemaMigrationPreflight(ctx context.Context, executor schemaMigrationQueryExecutor, migration versionedSchemaMigration) error {
	if migration.identityV2 == nil {
		return nil
	}
	switch migration.identityV2.PreflightIdentity {
	case schemaRescueV21PreflightIdentity:
		_, err := inspectModelProviderV21Shape(ctx, executor)
		return err
	case providerV22PreflightIdentity:
		_, err := inspectProviderV22Shape(ctx, executor)
		return err
	case transcriptV23PreflightIdentity:
		return preflightTranscriptV23(ctx, executor)
	case transcriptV24PreflightIdentity:
		return preflightTranscriptV24(ctx, executor)
	case transcriptV25PreflightIdentity:
		return preflightTranscriptV25(ctx, executor)
	case frameIncarnationV26PreflightIdentity:
		return preflightFrameIncarnationV26(ctx, executor)
	case transcriptHistoryV27PreflightIdentity:
		return preflightTranscriptHistoryClassificationV27(ctx, executor)
	case notificationsV28PreflightIdentity:
		return preflightNotificationsV28(ctx, executor)
	case transcriptHistoryBackfillV29PreflightIdentity:
		return preflightTranscriptHistoryBackfillV29(ctx, executor)
	case transcriptHistoryCutoverV30PreflightIdentity:
		return preflightTranscriptHistoryCutoverV30(ctx, executor)
	case transcriptHistoryActivationV31PreflightIdentity:
		return preflightTranscriptHistoryActivationV31(ctx, executor)
	case transcriptPayloadGenesisV32PreflightIdentity:
		return preflightTranscriptPayloadGenesisV32(ctx, executor)
	case transcriptHistoryOrdinaryV33PreflightIdentity:
		return preflightTranscriptHistoryOrdinaryV33(ctx, executor)
	case transcriptHistoryOrdinaryCursorV34PreflightIdentity:
		return preflightTranscriptHistoryOrdinaryCursorV34(ctx, executor)
	case transcriptTypedHistoryBootstrapV35PreflightIdentity:
		return preflightTranscriptTypedHistoryBootstrapV35(ctx, executor)
	case modelProviderGenerationV36PreflightIdentity:
		return preflightModelProviderGenerationV36(ctx, executor)
	case scientificComputeAuthorityV37PreflightIdentity:
		return preflightScientificComputeAuthorityV37(ctx, executor)
	case transcriptWebReadModelV38PreflightIdentity:
		return preflightTranscriptWebReadModelV38(ctx, executor)
	case kernelLocalOperationV39PreflightIdentity:
		return preflightKernelLocalOperationV39(ctx, executor)
	case toolCallBatchV40PreflightIdentity:
		return preflightToolCallBatchV40(ctx, executor)
	case kernelToolResultV41PreflightIdentity:
		return preflightKernelToolResultV41(ctx, executor)
	case scientificComputeSubmissionV42PreflightIdentity:
		return preflightScientificComputeSubmissionV42(ctx, executor)
	case artifactCollectionV43PreflightIdentity:
		return preflightArtifactCollectionV43(ctx, executor)
	case kernelDetachedExecutionV44PreflightIdentity:
		return preflightKernelDetachedExecutionV44(ctx, executor)
	case kernelDetachedExecutionV45PreflightIdentity:
		return preflightKernelDetachedExecutionV45(ctx, executor)
	case runtimeAuditDeleteV46PreflightIdentity:
		return preflightRuntimeAuditDeleteV46(ctx, executor)
	case transcriptWebProjectorV47PreflightIdentity:
		return preflightTranscriptWebProjectorV47(ctx, executor)
	case transcriptWebProjectorV48PreflightIdentity:
		return preflightTranscriptWebProjectorV48(ctx, executor)
	case transcriptWebProjectorV49PreflightIdentity:
		return preflightTranscriptWebProjectorV49(ctx, executor)
	case runnerLargeToolResultV50PreflightIdentity:
		return nil
	case kernelDetachedExecutionV51PreflightIdentity:
		return preflightKernelDetachedExecutionV51(ctx, executor)
	case runnerLargeToolResultV52PreflightIdentity:
		return preflightRunnerLargeToolResultV52(ctx, executor)
	case mcpToolCatalogV53PreflightIdentity:
		return preflightMCPToolCatalogV53(ctx, executor)
	case transcriptDeliveryConvergenceV54PreflightIdentity:
		return preflightTranscriptDeliveryConvergenceV54(ctx, executor)
	case kernelSoftwareRuntimeV55PreflightIdentity:
		return preflightKernelSoftwareRuntimeV55(ctx, executor)
	case kernelBashV56PreflightIdentity:
		return preflightKernelBashV56(ctx, executor)
	case toolBatchV57PreflightIdentity:
		return preflightToolBatchV57(ctx, executor)
	case transcriptWebProjectorV58PreflightIdentity:
		return preflightTranscriptWebProjectorV58(ctx, executor)
	case transcriptWebProjectorV59PreflightIdentity:
		return preflightTranscriptWebProjectorV59(ctx, executor)
	case transcriptWebProjectorV60PreflightIdentity:
		return preflightTranscriptWebProjectorV60(ctx, executor)
	case generatedPlanRetentionV61PreflightIdentity:
		return preflightGeneratedPlanRetentionV61(ctx, executor)
	case kernelTrustedMountV62PreflightIdentity:
		return preflightKernelTrustedMountV62(ctx, executor)
	case kernelResultSpoolV63PreflightIdentity:
		return preflightKernelResultSpoolV63(ctx, executor)
	case transcriptWebProjectorV64PreflightIdentity:
		return preflightTranscriptWebProjectorV64(ctx, executor)
	case transcriptWebProjectorV65PreflightIdentity:
		return preflightTranscriptWebProjectorV65(ctx, executor)
	case transcriptWebProjectorV66PreflightIdentity:
		return preflightTranscriptWebProjectorV66(ctx, executor)
	case transcriptWebProjectorV67PreflightIdentity:
		return preflightTranscriptWebProjectorV67(ctx, executor)
	default:
		return errors.New("schema migration preflight is not registered")
	}
}

func runSchemaMigrationCallback(ctx context.Context, tx *sql.Tx, migration versionedSchemaMigration) error {
	if migration.identityV2 == nil {
		return nil
	}
	switch migration.identityV2.CallbackID {
	case schemaRescueV21CallbackID:
		return normalizeModelProvidersV21(ctx, tx, nil)
	case providerV22CallbackID:
		return normalizeProvidersV22(ctx, tx, nil)
	case transcriptSchemaNoopCallbackID:
		return nil
	case transcriptV25CallbackID:
		return backfillTranscriptBranchesV25(ctx, tx)
	case transcriptHistoryActivationV31CallbackID:
		return backfillTranscriptFrameAuthorityV31(ctx, tx)
	case modelProviderGenerationV36CallbackID:
		return nil
	case scientificComputeAuthorityV37CallbackID:
		return nil
	case transcriptWebReadModelV38CallbackID:
		return nil
	case kernelLocalOperationV39CallbackID:
		return nil
	case toolCallBatchV40CallbackID:
		return nil
	case kernelToolResultV41CallbackID:
		return backfillKernelToolResultV41(ctx, tx)
	case scientificComputeSubmissionV42CallbackID:
		return nil
	case artifactCollectionV43CallbackID:
		return nil
	case kernelDetachedExecutionV44CallbackID:
		return nil
	case kernelDetachedExecutionV45CallbackID:
		return nil
	case runtimeAuditDeleteV46CallbackID:
		return nil
	case transcriptWebProjectorV47CallbackID:
		return migrateTranscriptWebProjectorV47(ctx, tx)
	case transcriptWebProjectorV48CallbackID:
		return migrateTranscriptWebProjectorV48(ctx, tx)
	case transcriptWebProjectorV49CallbackID:
		return migrateTranscriptWebProjectorV49(ctx, tx)
	case runnerLargeToolResultV50CallbackID:
		return nil
	case kernelDetachedExecutionV51CallbackID:
		return nil
	case runnerLargeToolResultV52CallbackID:
		return nil
	case mcpToolCatalogV53CallbackID:
		return nil
	case transcriptDeliveryConvergenceV54CallbackID:
		return nil
	case kernelSoftwareRuntimeV55CallbackID:
		return rebuildKernelLocalOperationHeadV55(ctx, tx)
	case kernelBashV56CallbackID:
		return rebuildKernelLocalOperationHeadV56(ctx, tx)
	case toolBatchV57CallbackID:
		return nil
	case transcriptWebProjectorV58CallbackID:
		return migrateTranscriptWebProjectorV58(ctx, tx)
	case transcriptWebProjectorV59CallbackID:
		return migrateTranscriptWebProjectorV59(ctx, tx)
	case transcriptWebProjectorV60CallbackID:
		return migrateTranscriptWebProjectorV60(ctx, tx)
	case generatedPlanRetentionV61CallbackID:
		return nil
	case kernelTrustedMountV62CallbackID:
		return backfillKernelTrustedMountV62(ctx, tx)
	case kernelResultSpoolV63CallbackID:
		return nil
	case transcriptWebProjectorV64CallbackID:
		return migrateTranscriptWebProjectorV64(ctx, tx)
	case transcriptWebProjectorV65CallbackID:
		return migrateTranscriptWebProjectorV65(ctx, tx)
	case transcriptWebProjectorV66CallbackID:
		return migrateTranscriptWebProjectorV66(ctx, tx)
	case transcriptWebProjectorV67CallbackID:
		return migrateTranscriptWebProjectorV67(ctx, tx)
	default:
		return errors.New("schema migration callback is not registered")
	}
}

func schemaMigrationCallbackRunsAfterStatements(migration versionedSchemaMigration) bool {
	return migration.identityV2 != nil &&
		(migration.identityV2.CallbackID == transcriptV25CallbackID ||
			migration.identityV2.CallbackID == transcriptHistoryActivationV31CallbackID ||
			migration.identityV2.CallbackID == kernelToolResultV41CallbackID)
}

func inspectModelProviderV21Shape(ctx context.Context, executor schemaMigrationQueryExecutor) (modelProviderV21Shape, error) {
	for _, staging := range []string{"model_providers_v21_new", "model_provider_legacy_raw_v21_staging"} {
		var count int
		if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name = ?`, staging).Scan(&count); err != nil {
			return modelProviderV21Shape{}, errors.New("inspect provider rescue staging objects")
		}
		if count != 0 {
			return modelProviderV21Shape{}, errors.New("provider rescue staging collision")
		}
	}

	columns, tableSQL, err := readSQLiteTableShape(ctx, executor, "model_providers")
	if err != nil {
		return modelProviderV21Shape{}, err
	}
	optional, err := validateModelProviderColumns(columns)
	if err != nil {
		return modelProviderV21Shape{}, err
	}
	if err := validateModelProviderTableSQL(tableSQL); err != nil {
		return modelProviderV21Shape{}, err
	}
	if err := validateModelProviderIndexes(ctx, executor); err != nil {
		return modelProviderV21Shape{}, err
	}
	if err := rejectModelProviderSchemaDependencies(ctx, executor); err != nil {
		return modelProviderV21Shape{}, err
	}

	var rawCount int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='model_provider_legacy_raw'`).Scan(&rawCount); err != nil {
		return modelProviderV21Shape{}, errors.New("inspect provider rescue quarantine table")
	}
	if rawCount == 0 {
		return modelProviderV21Shape{optionalColumns: optional}, nil
	}
	if len(optional) != 0 {
		return modelProviderV21Shape{}, errors.New("provider rescue quarantine conflicts with legacy optional columns")
	}
	if err := validateModelProviderLegacyRawShape(ctx, executor); err != nil {
		return modelProviderV21Shape{}, err
	}
	if err := validateModelProviderV21RowCounts(ctx, executor); err != nil {
		return modelProviderV21Shape{}, err
	}
	return modelProviderV21Shape{optionalColumns: optional, canonicalV21: true}, nil
}

func readSQLiteTableShape(ctx context.Context, executor schemaMigrationQueryExecutor, table string) ([]sqliteColumnShape, string, error) {
	var tableSQL string
	if err := executor.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&tableSQL); err != nil {
		return nil, "", errors.New("required provider table is unavailable")
	}
	rows, err := executor.QueryContext(ctx, `SELECT cid,name,type,"notnull",dflt_value,pk,hidden FROM pragma_table_xinfo(?) ORDER BY cid`, table)
	if err != nil {
		return nil, "", errors.New("inspect provider table columns")
	}
	defer rows.Close()
	columns := make([]sqliteColumnShape, 0)
	for rows.Next() {
		var column sqliteColumnShape
		var notNull int
		if err := rows.Scan(new(int), &column.name, &column.declaredType, &notNull, &column.defaultValue, &column.primaryKey, &column.hidden); err != nil {
			return nil, "", errors.New("scan provider table columns")
		}
		column.notNull = notNull != 0
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, "", errors.New("iterate provider table columns")
	}
	return columns, tableSQL, nil
}

func validateModelProviderColumns(columns []sqliteColumnShape) (map[string]bool, error) {
	required := []sqliteColumnShape{
		{name: "id", declaredType: "TEXT", primaryKey: 1},
		{name: "user_id", declaredType: "TEXT", notNull: true},
		{name: "name", declaredType: "TEXT", notNull: true},
		{name: "type", declaredType: "TEXT", notNull: true},
		{name: "base_url", declaredType: "TEXT", notNull: true},
		{name: "model", declaredType: "TEXT", notNull: true},
		{name: "secret_ref", declaredType: "TEXT", notNull: true, defaultValue: sql.NullString{String: "''", Valid: true}},
		{name: "enabled", declaredType: "INTEGER", notNull: true, defaultValue: sql.NullString{String: "1", Valid: true}},
		{name: "created_at", declaredType: "TIMESTAMP", notNull: true},
		{name: "updated_at", declaredType: "TIMESTAMP", notNull: true},
	}
	optionalDefinitions := map[string]sqliteColumnShape{
		"protocol":    {name: "protocol", declaredType: "TEXT", notNull: true},
		"temperature": {name: "temperature", declaredType: "REAL"},
		"max_tokens":  {name: "max_tokens", declaredType: "INTEGER"},
	}
	optional := map[string]bool{}
	filtered := make([]sqliteColumnShape, 0, len(columns))
	for _, column := range columns {
		if definition, ok := optionalDefinitions[column.name]; ok {
			if optional[column.name] || !sameSQLiteColumnShape(column, definition, column.name == "protocol") {
				return nil, errors.New("unsupported provider optional column declaration")
			}
			optional[column.name] = true
			continue
		}
		filtered = append(filtered, column)
	}
	if len(filtered) != len(required) {
		return nil, errors.New("unsupported provider column set")
	}
	for index := range required {
		if !sameSQLiteColumnShape(filtered[index], required[index], false) {
			return nil, errors.New("unsupported provider required column declaration")
		}
	}
	return optional, nil
}

func sameSQLiteColumnShape(got, want sqliteColumnShape, allowEmptyDefault bool) bool {
	if got.name != want.name || strings.ToUpper(got.declaredType) != want.declaredType || got.notNull != want.notNull || got.primaryKey != want.primaryKey || got.hidden != 0 {
		return false
	}
	if allowEmptyDefault && got.defaultValue.Valid && got.defaultValue.String == "''" {
		return true
	}
	return got.defaultValue.Valid == want.defaultValue.Valid && (!got.defaultValue.Valid || got.defaultValue.String == want.defaultValue.String)
}

func validateModelProviderTableSQL(tableSQL string) error {
	normalized := strings.ToLower(strings.Join(strings.Fields(tableSQL), " "))
	compact := strings.ReplaceAll(normalized, " ", "")
	for _, forbidden := range []string{"check(", "foreignkey(", "references", "generated", "asselect", "constraint"} {
		if strings.Contains(compact, forbidden) {
			return errors.New("unsupported provider table constraint")
		}
	}
	if strings.Count(compact, "unique(user_id,name)") > 1 {
		return errors.New("unsupported provider unique constraint")
	}
	withoutKnownUnique := strings.ReplaceAll(compact, "unique(user_id,name)", "")
	if strings.Contains(withoutKnownUnique, "unique") {
		return errors.New("unsupported provider unique constraint")
	}
	return nil
}

func validateModelProviderIndexes(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	rows, err := executor.QueryContext(ctx, `SELECT name,"unique",origin,partial FROM pragma_index_list('model_providers')`)
	if err != nil {
		return errors.New("inspect provider indexes")
	}
	type providerIndex struct {
		name, origin    string
		unique, partial int
	}
	indexes := make([]providerIndex, 0, 2)
	for rows.Next() {
		var index providerIndex
		if err := rows.Scan(&index.name, &index.unique, &index.origin, &index.partial); err != nil {
			_ = rows.Close()
			return errors.New("scan provider indexes")
		}
		indexes = append(indexes, index)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return errors.New("close provider indexes")
	}
	for _, index := range indexes {
		if index.origin == "pk" || index.origin == "u" {
			if index.partial != 0 {
				return errors.New("unsupported provider implicit index")
			}
			continue
		}
		if index.name != "model_providers_user_updated_idx" || index.unique != 0 || index.origin != "c" || index.partial != 0 {
			return errors.New("unsupported provider explicit index")
		}
		var columns string
		if err := executor.QueryRowContext(ctx, `SELECT group_concat(name, ',') FROM (SELECT name FROM pragma_index_info(?) ORDER BY seqno)`, index.name).Scan(&columns); err != nil || columns != "user_id,updated_at,id" {
			return errors.New("unsupported provider index columns")
		}
	}
	return nil
}

func rejectModelProviderSchemaDependencies(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var dependentObjects int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE type IN ('trigger','view') AND (tbl_name='model_providers' OR lower(sql) LIKE '%model_providers%')`).Scan(&dependentObjects); err != nil {
		return errors.New("inspect provider schema dependencies")
	}
	if dependentObjects != 0 {
		return errors.New("unsupported provider schema dependency")
	}
	rows, err := executor.QueryContext(ctx, `SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return errors.New("inspect provider foreign key dependencies")
	}
	defer rows.Close()
	tables := make([]string, 0)
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return errors.New("scan provider foreign key dependencies")
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		return errors.New("iterate provider foreign key dependencies")
	}
	for _, table := range tables {
		if table == "model_provider_legacy_raw" {
			continue
		}
		var references int
		if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_list(?) WHERE "table"='model_providers'`, table).Scan(&references); err != nil {
			return errors.New("inspect provider foreign key dependency")
		}
		if references != 0 {
			return errors.New("unsupported provider foreign key dependency")
		}
	}
	return nil
}

func validateModelProviderLegacyRawShape(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	columns, tableSQL, err := readSQLiteTableShape(ctx, executor, "model_provider_legacy_raw")
	if err != nil {
		return err
	}
	want := []sqliteColumnShape{
		{name: "provider_id", declaredType: "TEXT", primaryKey: 1},
		{name: "source_updated_at_raw", declaredType: "BLOB", notNull: true},
		{name: "legacy_protocol_present", declaredType: "INTEGER", notNull: true},
		{name: "legacy_protocol_raw", declaredType: "BLOB"},
		{name: "legacy_temperature_present", declaredType: "INTEGER", notNull: true},
		{name: "legacy_temperature_raw", declaredType: "BLOB"},
		{name: "legacy_max_tokens_present", declaredType: "INTEGER", notNull: true},
		{name: "legacy_max_tokens_raw", declaredType: "BLOB"},
	}
	if len(columns) != len(want) {
		return errors.New("unsupported provider quarantine shape")
	}
	for index := range want {
		if !sameSQLiteColumnShape(columns[index], want[index], false) {
			return errors.New("unsupported provider quarantine columns")
		}
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(tableSQL), " "))
	compact := strings.ReplaceAll(normalized, " ", "")
	for _, required := range []string{
		"foreign key (provider_id) references model_providers(id) on update cascade on delete cascade",
		"check (legacy_protocol_present in (0,1))",
		"check (legacy_protocol_present=1 or legacy_protocol_raw is null)",
		"check (legacy_temperature_present in (0,1))",
		"check (legacy_temperature_present=1 or legacy_temperature_raw is null)",
		"check (legacy_max_tokens_present in (0,1))",
		"check (legacy_max_tokens_present=1 or legacy_max_tokens_raw is null)",
	} {
		if !strings.Contains(normalized, required) {
			return errors.New("unsupported provider quarantine constraint")
		}
	}
	if strings.Count(compact, "check(") != 6 || strings.Count(compact, "foreignkey(") != 1 {
		return errors.New("unsupported provider quarantine constraint set")
	}
	var foreignKeys int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_list('model_provider_legacy_raw')
		WHERE "table"='model_providers' AND "from"='provider_id' AND "to"='id'
		AND on_update='CASCADE' AND on_delete='CASCADE'`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		return errors.New("unsupported provider quarantine foreign key")
	}
	var indexes int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_index_list('model_provider_legacy_raw') WHERE origin!='pk'`).Scan(&indexes); err != nil || indexes != 0 {
		return errors.New("unsupported provider quarantine index")
	}
	var dependencies int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE type IN ('trigger','view') AND (tbl_name='model_provider_legacy_raw' OR lower(sql) LIKE '%model_provider_legacy_raw%')`).Scan(&dependencies); err != nil || dependencies != 0 {
		return errors.New("unsupported provider quarantine dependency")
	}
	return nil
}

func validateModelProviderV21RowCounts(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var mainCount, rawCount int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_providers`).Scan(&mainCount); err != nil {
		return errors.New("count canonical provider rows")
	}
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_provider_legacy_raw`).Scan(&rawCount); err != nil {
		return errors.New("count provider quarantine rows")
	}
	if rawCount > mainCount {
		return errors.New("provider rescue row count mismatch")
	}
	return nil
}

func normalizeModelProvidersV21(ctx context.Context, tx *sql.Tx, hook schemaRescueV21StageHook) error {
	shape, err := inspectModelProviderV21Shape(ctx, tx)
	if err != nil {
		return err
	}
	if shape.canonicalV21 {
		return validateModelProviderV21Integrity(ctx, tx)
	}
	if err := runSchemaRescueV21Stage(hook, "validated-source"); err != nil {
		return err
	}

	statements := []string{
		`CREATE TABLE model_providers_v21_new (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			name TEXT NOT NULL,
			type TEXT NOT NULL,
			base_url TEXT NOT NULL,
			model TEXT NOT NULL,
			secret_ref TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE TABLE model_provider_legacy_raw_v21_staging (
			provider_id TEXT PRIMARY KEY,
			source_updated_at_raw BLOB NOT NULL,
			legacy_protocol_present INTEGER NOT NULL,
			legacy_protocol_raw BLOB,
			legacy_temperature_present INTEGER NOT NULL,
			legacy_temperature_raw BLOB,
			legacy_max_tokens_present INTEGER NOT NULL,
			legacy_max_tokens_raw BLOB
		)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return errors.New("create provider rescue staging tables")
		}
	}
	if err := runSchemaRescueV21Stage(hook, "created-staging"); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO model_providers_v21_new
		(id,user_id,name,type,base_url,model,secret_ref,enabled,created_at,updated_at)
		SELECT id,user_id,name,type,base_url,model,secret_ref,enabled,created_at,updated_at FROM model_providers`); err != nil {
		return errors.New("copy canonical provider columns")
	}
	optional := shape.optionalColumns
	legacyInsert := fmt.Sprintf(`INSERT INTO model_provider_legacy_raw_v21_staging
		(provider_id,source_updated_at_raw,legacy_protocol_present,legacy_protocol_raw,
		 legacy_temperature_present,legacy_temperature_raw,legacy_max_tokens_present,legacy_max_tokens_raw)
		SELECT id,updated_at,%s,%s,%s,%s,%s,%s FROM model_providers`,
		presenceLiteral(optional["protocol"]), sourceColumnOrNull(optional["protocol"], "protocol"),
		presenceLiteral(optional["temperature"]), sourceColumnOrNull(optional["temperature"], "temperature"),
		presenceLiteral(optional["max_tokens"]), sourceColumnOrNull(optional["max_tokens"], "max_tokens"))
	if _, err := tx.ExecContext(ctx, legacyInsert); err != nil {
		return errors.New("copy provider quarantine columns")
	}
	if err := validateModelProviderV21Staging(ctx, tx, optional); err != nil {
		return err
	}
	if err := runSchemaRescueV21Stage(hook, "copied-and-verified"); err != nil {
		return err
	}

	for _, statement := range []string{
		`DROP TABLE model_providers`,
		`ALTER TABLE model_providers_v21_new RENAME TO model_providers`,
		`CREATE INDEX model_providers_user_updated_idx ON model_providers (user_id, updated_at DESC, id DESC)`,
		`CREATE TABLE model_provider_legacy_raw (
			provider_id TEXT PRIMARY KEY,
			source_updated_at_raw BLOB NOT NULL,
			legacy_protocol_present INTEGER NOT NULL CHECK (legacy_protocol_present IN (0,1)),
			legacy_protocol_raw BLOB,
			legacy_temperature_present INTEGER NOT NULL CHECK (legacy_temperature_present IN (0,1)),
			legacy_temperature_raw BLOB,
			legacy_max_tokens_present INTEGER NOT NULL CHECK (legacy_max_tokens_present IN (0,1)),
			legacy_max_tokens_raw BLOB,
			CHECK (legacy_protocol_present=1 OR legacy_protocol_raw IS NULL),
			CHECK (legacy_temperature_present=1 OR legacy_temperature_raw IS NULL),
			CHECK (legacy_max_tokens_present=1 OR legacy_max_tokens_raw IS NULL),
			FOREIGN KEY (provider_id) REFERENCES model_providers(id) ON UPDATE CASCADE ON DELETE CASCADE
		)`,
		`INSERT INTO model_provider_legacy_raw
			(provider_id,source_updated_at_raw,legacy_protocol_present,legacy_protocol_raw,
			 legacy_temperature_present,legacy_temperature_raw,legacy_max_tokens_present,legacy_max_tokens_raw)
		 SELECT provider_id,source_updated_at_raw,legacy_protocol_present,legacy_protocol_raw,
			legacy_temperature_present,legacy_temperature_raw,legacy_max_tokens_present,legacy_max_tokens_raw
		 FROM model_provider_legacy_raw_v21_staging`,
		`DROP TABLE model_provider_legacy_raw_v21_staging`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return errors.New("install canonical provider rescue schema")
		}
	}
	if err := runSchemaRescueV21Stage(hook, "installed-canonical-schema"); err != nil {
		return err
	}
	return validateModelProviderV21Integrity(ctx, tx)
}

func runSchemaRescueV21Stage(hook schemaRescueV21StageHook, stage string) error {
	if hook == nil {
		return nil
	}
	if err := hook(stage); err != nil {
		return fmt.Errorf("provider rescue stage %s: %w", stage, err)
	}
	return nil
}

func presenceLiteral(present bool) string {
	if present {
		return "1"
	}
	return "0"
}

func sourceColumnOrNull(present bool, column string) string {
	if present {
		return column
	}
	return "NULL"
}

func validateModelProviderV21Staging(ctx context.Context, tx *sql.Tx, optional map[string]bool) error {
	canonicalColumns := "id,user_id,name,type,base_url,model,secret_ref,enabled,created_at,updated_at"
	var differences int
	query := fmt.Sprintf(`SELECT COUNT(*) FROM (
		SELECT %s FROM model_providers EXCEPT SELECT %s FROM model_providers_v21_new
		UNION ALL
		SELECT %s FROM model_providers_v21_new EXCEPT SELECT %s FROM model_providers
	)`, canonicalColumns, canonicalColumns, canonicalColumns, canonicalColumns)
	if err := tx.QueryRowContext(ctx, query).Scan(&differences); err != nil || differences != 0 {
		return errors.New("canonical provider copy mismatch")
	}
	var sourceCount, canonicalCount, rawCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_providers`).Scan(&sourceCount); err != nil {
		return errors.New("count source provider rows")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_providers_v21_new`).Scan(&canonicalCount); err != nil {
		return errors.New("count canonical provider staging rows")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_provider_legacy_raw_v21_staging`).Scan(&rawCount); err != nil {
		return errors.New("count provider quarantine staging rows")
	}
	if sourceCount != canonicalCount || sourceCount != rawCount {
		return errors.New("provider rescue staging row count mismatch")
	}
	if err := validateRawColumnCopy(ctx, tx, "updated_at", "source_updated_at_raw"); err != nil {
		return err
	}
	for _, column := range []string{"protocol", "temperature", "max_tokens"} {
		rawColumn := "legacy_" + column + "_raw"
		presentColumn := "legacy_" + column + "_present"
		if !optional[column] {
			var invalid int
			query := fmt.Sprintf(`SELECT COUNT(*) FROM model_provider_legacy_raw_v21_staging WHERE %s!=0 OR %s IS NOT NULL`, presentColumn, rawColumn)
			if err := tx.QueryRowContext(ctx, query).Scan(&invalid); err != nil || invalid != 0 {
				return errors.New("absent provider legacy column was not quarantined deterministically")
			}
			continue
		}
		if err := validateRawColumnCopy(ctx, tx, column, rawColumn); err != nil {
			return err
		}
		var missingPresence int
		query := fmt.Sprintf(`SELECT COUNT(*) FROM model_provider_legacy_raw_v21_staging WHERE %s!=1`, presentColumn)
		if err := tx.QueryRowContext(ctx, query).Scan(&missingPresence); err != nil || missingPresence != 0 {
			return errors.New("present provider legacy column lost presence metadata")
		}
	}
	return nil
}

func validateRawColumnCopy(ctx context.Context, tx *sql.Tx, sourceColumn, rawColumn string) error {
	query := fmt.Sprintf(`SELECT COUNT(*) FROM %s source
		JOIN model_provider_legacy_raw_v21_staging raw ON raw.provider_id=source.id
		WHERE typeof(raw.%s)!=typeof(source.%s)
		   OR quote(raw.%s)!=quote(source.%s)
		   OR hex(raw.%s)!=hex(source.%s)`, "model_providers", rawColumn, sourceColumn, rawColumn, sourceColumn, rawColumn, sourceColumn)
	var differences int
	if err := tx.QueryRowContext(ctx, query).Scan(&differences); err != nil || differences != 0 {
		return errors.New("provider quarantine value fidelity mismatch")
	}
	return nil
}

func validateModelProviderV21Integrity(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	if err := validateModelProviderLegacyRawShape(ctx, executor); err != nil {
		return err
	}
	if err := validateModelProviderV21RowCounts(ctx, executor); err != nil {
		return err
	}
	var canonicalIndexes int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_index_list('model_providers')
		WHERE name='model_providers_user_updated_idx' AND "unique"=0 AND origin='c' AND partial=0`).Scan(&canonicalIndexes); err != nil || canonicalIndexes != 1 {
		return errors.New("canonical provider index is unavailable")
	}
	var foreignKeyErrors int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignKeyErrors); err != nil || foreignKeyErrors != 0 {
		return errors.New("provider rescue foreign key validation failed")
	}
	var invalidPresence int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_provider_legacy_raw
		WHERE legacy_protocol_present NOT IN (0,1)
		   OR legacy_temperature_present NOT IN (0,1)
		   OR legacy_max_tokens_present NOT IN (0,1)`).Scan(&invalidPresence); err != nil || invalidPresence != 0 {
		return errors.New("provider quarantine presence metadata is invalid")
	}
	return nil
}
