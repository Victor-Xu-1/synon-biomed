package workspace

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"
)

const (
	providerV22CallbackID        = "classify-model-provider-protocol-v22"
	providerV22PreflightIdentity = "canonical-provider-v21-or-v22-shape-v1"
	providerV22RuleSpec          = "synon.workspace.model-provider.protocol.v22"

	providerConfigurationReady       = "ready"
	providerConfigurationUnresolved  = "unresolved"
	providerConfigurationUnsupported = "unsupported"
)

var providerV22Migration = versionedSchemaMigration{
	version: 22,
	name:    "canonical-model-provider-protocol",
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        providerV22CallbackID,
		RuleSpec:          providerV22RuleSpec,
		PreflightIdentity: providerV22PreflightIdentity,
	},
}

var providerV22ExactTypes = map[string]string{
	"deepseek": "openai-compatible", "dashscope": "openai-compatible", "dashscope-coding": "openai-compatible",
	"zhipu": "openai-compatible", "moonshot": "openai-compatible", "siliconflow-cn": "openai-compatible",
	"siliconflow": "openai-compatible", "moonshot-global": "openai-compatible", "minimax": "openai-compatible",
	"xai": "openai-compatible", "volcengine-ark": "openai-compatible", "baidu-qianfan": "openai-compatible",
	"tencent-hunyuan": "openai-compatible", "novita": "openai-compatible", "modelscope": "openai-compatible",
	"openrouter": "openai-compatible", "openai": "openai-compatible", "ppio": "openai-compatible",
	"infiniai": "openai-compatible", "stepfun": "openai-compatible", "ollama": "openai-compatible",
	"lm-studio": "openai-compatible", "vllm": "openai-compatible", "custom": "openai-compatible",
	"openai-compatible": "openai-compatible", "openai-responses": "openai-responses",
	"azure-openai": "azure-openai", "azure-openai-responses": "azure-openai-responses",
	"gemini": "gemini", "anthropic": "anthropic",
}

var providerV22OfficialOpenAICompatibleHosts = map[string]bool{
	"api.deepseek.com": true, "dashscope.aliyuncs.com": true, "coding.dashscope.aliyuncs.com": true,
	"open.bigmodel.cn": true, "api.moonshot.cn": true, "api.moonshot.ai": true,
	"api.siliconflow.cn": true, "api.siliconflow.com": true, "api.minimaxi.com": true,
	"api.x.ai": true, "ark.cn-beijing.volces.com": true, "qianfan.baidubce.com": true,
	"api.hunyuan.cloud.tencent.com": true, "api.novita.ai": true, "api-inference.modelscope.cn": true,
	"openrouter.ai": true, "api.ppinfra.com": true, "cloud.infini-ai.com": true,
	"api.stepfun.com": true,
}

type providerV22Shape struct {
	canonical bool
}

type providerV22Resolution struct {
	protocol string
	status   string
	code     string
}

type providerV22EndpointSignal struct {
	protocol        string
	unsupportedCode string
	valid           bool
}

func inspectProviderV22Shape(ctx context.Context, executor schemaMigrationQueryExecutor) (providerV22Shape, error) {
	columns, tableSQL, err := readSQLiteTableShape(ctx, executor, "model_providers")
	if err != nil {
		return providerV22Shape{}, err
	}
	v22Definitions := map[string]sqliteColumnShape{
		"protocol": {name: "protocol", declaredType: "TEXT", notNull: true, defaultValue: sql.NullString{String: "''", Valid: true}},
		"configuration_status": {
			name: "configuration_status", declaredType: "TEXT", notNull: true,
			defaultValue: sql.NullString{String: "'unresolved'", Valid: true},
		},
		"configuration_code": {name: "configuration_code", declaredType: "TEXT", notNull: true, defaultValue: sql.NullString{String: "''", Valid: true}},
	}
	found := map[string]bool{}
	baseColumns := make([]sqliteColumnShape, 0, len(columns))
	for _, column := range columns {
		definition, ok := v22Definitions[column.name]
		if !ok {
			baseColumns = append(baseColumns, column)
			continue
		}
		if found[column.name] || !sameSQLiteColumnShape(column, definition, false) {
			return providerV22Shape{}, errors.New("unsupported provider v22 column declaration")
		}
		found[column.name] = true
	}
	if len(found) == 0 {
		if _, err := inspectModelProviderV21Shape(ctx, executor); err != nil {
			return providerV22Shape{}, err
		}
		return providerV22Shape{}, nil
	}
	if len(found) != len(v22Definitions) {
		return providerV22Shape{}, errors.New("partial provider v22 schema")
	}
	if optional, err := validateModelProviderColumns(baseColumns); err != nil || len(optional) != 0 {
		return providerV22Shape{}, errors.New("unsupported provider v22 base schema")
	}
	compact := strings.ToLower(strings.ReplaceAll(strings.Join(strings.Fields(tableSQL), ""), " ", ""))
	for _, required := range []string{
		"check(protocolin('','openai-compatible','openai-responses','azure-openai','azure-openai-responses','gemini','anthropic'))",
		"check(configuration_statusin('ready','unresolved','unsupported'))",
		"check(configuration_codein('','explicit','legacy_exact_type','legacy_official_endpoint','legacy_ambiguous','unsupported_bedrock','unsupported_vertex','invalid_protocol'))",
	} {
		if !strings.Contains(compact, required) {
			return providerV22Shape{}, errors.New("unsupported provider v22 constraint")
		}
	}
	if strings.Count(compact, "check(") != 3 || strings.Contains(compact, "foreignkey(") ||
		strings.Contains(compact, "references") || strings.Contains(compact, "generated") || strings.Contains(compact, "constraint") {
		return providerV22Shape{}, errors.New("unsupported provider v22 constraint set")
	}
	if err := validateModelProviderIndexes(ctx, executor); err != nil {
		return providerV22Shape{}, err
	}
	if err := rejectModelProviderSchemaDependencies(ctx, executor); err != nil {
		return providerV22Shape{}, err
	}
	if err := validateModelProviderLegacyRawShape(ctx, executor); err != nil {
		return providerV22Shape{}, err
	}
	if err := validateProviderV22Rows(ctx, executor); err != nil {
		return providerV22Shape{}, err
	}
	return providerV22Shape{canonical: true}, nil
}

func normalizeProvidersV22(ctx context.Context, tx *sql.Tx, hook schemaRescueV21StageHook) error {
	shape, err := inspectProviderV22Shape(ctx, tx)
	if err != nil {
		return err
	}
	if shape.canonical {
		return validateProviderV22Rows(ctx, tx)
	}
	if err := runSchemaRescueV21Stage(hook, "validated-v21-source"); err != nil {
		return err
	}
	for _, statement := range []string{
		`ALTER TABLE model_providers ADD COLUMN protocol TEXT NOT NULL DEFAULT ''
			CHECK (protocol IN ('','openai-compatible','openai-responses','azure-openai','azure-openai-responses','gemini','anthropic'))`,
		`ALTER TABLE model_providers ADD COLUMN configuration_status TEXT NOT NULL DEFAULT 'unresolved'
			CHECK (configuration_status IN ('ready','unresolved','unsupported'))`,
		`ALTER TABLE model_providers ADD COLUMN configuration_code TEXT NOT NULL DEFAULT ''
			CHECK (configuration_code IN ('','explicit','legacy_exact_type','legacy_official_endpoint','legacy_ambiguous','unsupported_bedrock','unsupported_vertex','invalid_protocol'))`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return errors.New("install provider v22 columns")
		}
	}
	if err := runSchemaRescueV21Stage(hook, "installed-v22-columns"); err != nil {
		return err
	}

	rows, err := tx.QueryContext(ctx, `SELECT id,type,base_url FROM model_providers ORDER BY id`)
	if err != nil {
		return errors.New("read providers for v22 classification")
	}
	type candidate struct{ id, providerType, baseURL string }
	candidates := make([]candidate, 0)
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.providerType, &item.baseURL); err != nil {
			rows.Close()
			return errors.New("scan provider for v22 classification")
		}
		candidates = append(candidates, item)
	}
	if err := rows.Close(); err != nil {
		return errors.New("close provider v22 classification rows")
	}
	if err := rows.Err(); err != nil {
		return errors.New("iterate provider v22 classification rows")
	}
	for _, item := range candidates {
		resolution := classifyProviderV22(item.providerType, item.baseURL)
		if _, err := tx.ExecContext(ctx, `UPDATE model_providers
			SET protocol=?,configuration_status=?,configuration_code=? WHERE id=?`,
			resolution.protocol, resolution.status, resolution.code, item.id); err != nil {
			return errors.New("persist provider v22 classification")
		}
	}
	if err := runSchemaRescueV21Stage(hook, "classified-v22-rows"); err != nil {
		return err
	}
	return validateProviderV22Rows(ctx, tx)
}

func classifyProviderV22(providerType, baseURL string) providerV22Resolution {
	typeProtocol, exactType := providerV22ExactTypes[providerType]
	typeUnsupported := ""
	switch providerType {
	case "bedrock", "aws-bedrock":
		typeUnsupported = "unsupported_bedrock"
	case "vertex", "vertex-ai":
		typeUnsupported = "unsupported_vertex"
	}
	endpoint := classifyProviderV22Endpoint(baseURL)
	if !endpoint.valid {
		return providerV22Resolution{status: providerConfigurationUnresolved, code: "legacy_ambiguous"}
	}
	if exactType {
		if endpoint.unsupportedCode != "" || (endpoint.protocol != "" && endpoint.protocol != typeProtocol) {
			return providerV22Resolution{status: providerConfigurationUnresolved, code: "legacy_ambiguous"}
		}
		return providerV22Resolution{protocol: typeProtocol, status: providerConfigurationReady, code: "legacy_exact_type"}
	}
	if typeUnsupported != "" {
		if endpoint.protocol != "" || (endpoint.unsupportedCode != "" && endpoint.unsupportedCode != typeUnsupported) {
			return providerV22Resolution{status: providerConfigurationUnresolved, code: "legacy_ambiguous"}
		}
		return providerV22Resolution{status: providerConfigurationUnsupported, code: typeUnsupported}
	}
	if providerType != "" {
		return providerV22Resolution{status: providerConfigurationUnresolved, code: "legacy_ambiguous"}
	}
	if endpoint.unsupportedCode != "" {
		return providerV22Resolution{status: providerConfigurationUnsupported, code: endpoint.unsupportedCode}
	}
	if endpoint.protocol != "" {
		return providerV22Resolution{protocol: endpoint.protocol, status: providerConfigurationReady, code: "legacy_official_endpoint"}
	}
	return providerV22Resolution{status: providerConfigurationUnresolved, code: "legacy_ambiguous"}
}

func classifyProviderV22Endpoint(raw string) providerV22EndpointSignal {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return providerV22EndpointSignal{}
	}
	host := strings.ToLower(parsed.Hostname())
	path := strings.ToLower(strings.TrimRight(parsed.EscapedPath(), "/"))
	signal := providerV22EndpointSignal{valid: true}
	switch {
	case host == "api.anthropic.com":
		signal.protocol = "anthropic"
	case host == "generativelanguage.googleapis.com":
		signal.protocol = "gemini"
	case host == "api.openai.com" && strings.HasSuffix(path, "/responses"):
		signal.protocol = "openai-responses"
	case host == "api.openai.com" && (path == "" || path == "/v1" || strings.HasSuffix(path, "/chat/completions")):
		signal.protocol = "openai-compatible"
	case isProviderV22AzureHost(host) && strings.HasSuffix(path, "/responses"):
		signal.protocol = "azure-openai-responses"
	case isProviderV22AzureHost(host):
		signal.protocol = "azure-openai"
	case providerV22OfficialOpenAICompatibleHosts[host]:
		signal.protocol = "openai-compatible"
	case strings.HasPrefix(host, "bedrock-runtime.") && strings.HasSuffix(host, ".amazonaws.com"):
		signal.unsupportedCode = "unsupported_bedrock"
	case host == "aiplatform.googleapis.com" || strings.HasSuffix(host, "-aiplatform.googleapis.com"):
		signal.unsupportedCode = "unsupported_vertex"
	}
	return signal
}

func isProviderV22AzureHost(host string) bool {
	return strings.HasSuffix(host, ".openai.azure.com") ||
		strings.HasSuffix(host, ".services.ai.azure.com") ||
		strings.HasSuffix(host, ".api.cognitive.microsoft.com")
}

func validateProviderV22Rows(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var invalid int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_providers WHERE
		configuration_status NOT IN ('ready','unresolved','unsupported')
		OR protocol NOT IN ('','openai-compatible','openai-responses','azure-openai','azure-openai-responses','gemini','anthropic')
		OR configuration_code NOT IN ('','explicit','legacy_exact_type','legacy_official_endpoint','legacy_ambiguous','unsupported_bedrock','unsupported_vertex','invalid_protocol')
		OR (configuration_status='ready' AND (protocol NOT IN ('openai-compatible','openai-responses','azure-openai','azure-openai-responses','gemini','anthropic')
			OR configuration_code NOT IN ('explicit','legacy_exact_type','legacy_official_endpoint')))
		OR (configuration_status='unresolved' AND (protocol!='' OR configuration_code NOT IN ('','legacy_ambiguous','invalid_protocol')))
		OR (configuration_status='unsupported' AND (protocol!='' OR configuration_code NOT IN ('unsupported_bedrock','unsupported_vertex')))`).Scan(&invalid); err != nil || invalid != 0 {
		return errors.New("provider v22 classification is invalid")
	}
	return nil
}
