package server

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	secretstore "synon-go/internal/persistence/secrets"
)

const compatibilitySecretPayloadUnits = 64 * 1024

var (
	compatibilitySecretNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
	compatibilityAWSRegionPattern  = regexp.MustCompile(`^[a-z]{2}-[a-z]+-\d+$`)
	compatibilityGUIDPattern       = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	compatibilityAzureDomain       = regexp.MustCompile(`^[a-zA-Z0-9.-]+\.onmicrosoft\.(com|us|cn|de)$`)
	compatibilityAWSEndpoint       = regexp.MustCompile(`(?i)^https://[a-z0-9.-]+\.[a-z0-9-]+(:[0-9]+)?/?$`)
	compatibilityAzureAccount      = regexp.MustCompile(`(?i)(?:^|;)\s*AccountName\s*=\s*([^;]+)`)
	compatibilityAzureAccountKey   = regexp.MustCompile(`(?i)(?:^|;)\s*AccountKey\s*=`)
)

var compatibilitySecretProviders = map[string]string{
	"generic": "", "github": "GitHub", "aws": "AWS", "gcp": "Google Cloud",
	"azure": "Microsoft Azure", "literature": "Literature Access", "modal": "Modal",
}

var compatibilityMultiSecretProviders = map[string]bool{"gcp": true, "aws": true, "azure": true}

var compatibilityReservedSecretNames = func() map[string]struct{} {
	values := []string{
		"GITHUB_TOKEN", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_DEFAULT_REGION",
		"AZURE_TENANT_ID", "AZURE_CLIENT_ID", "AZURE_CLIENT_SECRET", "AZURE_SUBSCRIPTION_ID",
		"GOOGLE_APPLICATION_CREDENTIALS", "ELSEVIER_API_KEY", "ELSEVIER_INST_TOKEN",
		"SPRINGER_API_KEY", "SEMANTIC_SCHOLAR_API_KEY", "NCBI_API_KEY", "CORE_API_KEY",
		"OPERON_EZPROXY_URL", "OPERON_EZPROXY_COOKIE", "MODAL_TOKEN_ID", "MODAL_TOKEN_SECRET",
		"PATH", "HOME", "USER", "SHELL", "TMPDIR", "LANG", "LC_ALL", "PWD", "OLDPWD",
		"TERM", "HOSTNAME", "DISPLAY", "SSH_AUTH_SOCK", "LD_LIBRARY_PATH", "PYTHONPATH",
		"NODE_PATH", "CONDA_PREFIX", "VIRTUAL_ENV", "LD_PRELOAD", "BASH_ENV", "ENV",
		"PROMPT_COMMAND", "IFS", "PYTHONSTARTUP", "NODE_OPTIONS", "GIT_SSH_COMMAND",
		"GIT_ASKPASS", "SYNON_LLM_API_KEY_ENCRYPTION_KEY", "AWS_SESSION_TOKEN",
		"LLM_OAUTH_CLIENT_SECRET", "GCS_CONFIG_BUCKET", "OAUTH_CALLBACK_URL",
		"OAUTH_ENCRYPTION_KEY", "OPERON_AMPLITUDE_API_KEY_DEV", "OPERON_AMPLITUDE_API_KEY_PROD",
		"REDIS_URL", "_OPERON_GCP_SA_JSON", "SYNON_LLM_API_KEY", "SYNON_LLM_BASE_URL",
		"SYNON_LLM_IDENTITY_TOKEN", "SYNON_LLM_IDENTITY_TOKEN_FILE", "DATABASE_URL",
		"SECRET_KEY", "OPERON_CONTACT_EMAIL",
	}
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}()

var compatibilityLiteratureFields = []string{
	"elsevier_api_key", "elsevier_inst_token", "springer_api_key", "semantic_scholar_api_key",
	"ncbi_api_key", "core_api_key", "ezproxy_url", "ezproxy_cookie",
}

type compatibilitySecretValidationError struct{ message string }

func (err compatibilitySecretValidationError) Error() string { return err.message }

func validateCompatibilitySecretEnvelope(
	provider string, name, value compatibilityNullable[string], credentials compatibilityNullable[map[string]any],
	description, credentialType, region compatibilityNullable[string],
) error {
	if value.Present && !value.Null && compatibilityUTF16Units(value.Value) > compatibilitySecretPayloadUnits {
		return compatibilitySecretErrorf("Credentials payload too large (max %d bytes)", compatibilitySecretPayloadUnits)
	}
	if credentials.Present && !credentials.Null {
		raw, _ := json.Marshal(credentials.Value)
		if compatibilityUTF16Units(string(raw)) > compatibilitySecretPayloadUnits {
			return compatibilitySecretErrorf("Credentials payload too large (max %d bytes)", compatibilitySecretPayloadUnits)
		}
	}
	if name.Present && !name.Null && name.Value != "" && compatibilityUTF16Units(name.Value) > 128 {
		return compatibilitySecretErrorf("name must be at most 128 characters")
	}
	if value.Present && !value.Null && value.Value == "" {
		return compatibilitySecretErrorf("value must be at least 1 character")
	}
	if description.Present && !description.Null && description.Value != "" && compatibilityUTF16Units(description.Value) > 256 {
		return compatibilitySecretErrorf("description must be at most 256 characters")
	}
	if credentialType.Present && !credentialType.Null && credentialType.Value != "" && compatibilityUTF16Units(credentialType.Value) > 32 {
		return compatibilitySecretErrorf("credential_type must be at most 32 characters")
	}
	if region.Present && !region.Null && region.Value != "" && compatibilityUTF16Units(region.Value) > 64 {
		return compatibilitySecretErrorf("region must be at most 64 characters")
	}
	_ = provider
	return nil
}

func validateCompatibilitySecretUpdateEnvelope(
	value compatibilityNullable[string], credentials compatibilityNullable[map[string]any], name,
	description, region compatibilityNullable[string],
) error {
	if value.Present && !value.Null && compatibilityUTF16Units(value.Value) > compatibilitySecretPayloadUnits {
		return compatibilitySecretErrorf("Credentials payload too large (max %d bytes)", compatibilitySecretPayloadUnits)
	}
	if credentials.Present && !credentials.Null {
		raw, _ := json.Marshal(credentials.Value)
		if compatibilityUTF16Units(string(raw)) > compatibilitySecretPayloadUnits {
			return compatibilitySecretErrorf("Credentials payload too large (max %d bytes)", compatibilitySecretPayloadUnits)
		}
	}
	if name.Present && !name.Null && compatibilityUTF16Units(name.Value) > 128 {
		return compatibilitySecretErrorf("name must be at most 128 characters")
	}
	if description.Present && !description.Null && compatibilityUTF16Units(description.Value) > 256 {
		return compatibilitySecretErrorf("description must be at most 256 characters")
	}
	if region.Present && !region.Null && compatibilityUTF16Units(region.Value) > 64 {
		return compatibilitySecretErrorf("region must be at most 64 characters")
	}
	return nil
}

func validateCompatibilityProviderCredentials(provider string, credentials map[string]any) error {
	switch provider {
	case "github":
		if token, ok := credentials["token"].(string); !ok || token == "" {
			return compatibilitySecretErrorf("GitHub credentials require a non-empty 'token'")
		}
	case "aws":
		accessID, accessOK := credentials["access_key_id"].(string)
		secret, secretOK := credentials["secret_access_key"].(string)
		if !accessOK || len(accessID) < 16 || len(accessID) > 128 {
			return compatibilitySecretErrorf("AWS access_key_id must be 16-128 characters")
		}
		if !secretOK || secret == "" {
			return compatibilitySecretErrorf("AWS secret_access_key is required")
		}
		if region, ok := credentials["region"].(string); ok && !compatibilityAWSRegionPattern.MatchString(region) {
			return compatibilitySecretErrorf("AWS region format invalid (expected e.g. 'us-west-2')")
		}
		if endpoint, present := credentials["endpoint"]; present && endpoint != nil {
			text, ok := endpoint.(string)
			if !ok || !compatibilityAWSEndpoint.MatchString(text) {
				return compatibilitySecretErrorf("AWS endpoint must be an https URL with a DNS hostname (e.g. https://ns.compat.objectstorage.region.oraclecloud.com)")
			}
		}
	case "gcp":
		serviceAccount := credentials["service_account_json"]
		accessID, hasAccessID := credentials["access_key_id"].(string)
		secret, hasSecret := credentials["secret_access_key"].(string)
		hmac := hasAccessID && accessID != "" && hasSecret && secret != "" && serviceAccount == nil
		serviceAccountPresent := serviceAccount != nil
		if text, ok := serviceAccount.(string); ok {
			serviceAccountPresent = text != ""
		}
		if !serviceAccountPresent && !hmac {
			return compatibilitySecretErrorf("GCP credentials require either 'service_account_json' or 'access_key_id' + 'secret_access_key'")
		}
		if serviceAccountPresent {
			object, err := compatibilityServiceAccountObject(serviceAccount)
			if err != nil {
				return err
			}
			for _, field := range []string{"type", "project_id", "private_key", "client_email"} {
				if !compatibilityTruthy(object[field]) {
					return compatibilitySecretErrorf("GCP service account JSON missing required field '%s'", field)
				}
			}
			if object["type"] != "service_account" {
				return compatibilitySecretErrorf("GCP service account JSON 'type' must be 'service_account'")
			}
		}
	case "azure":
		if raw, present := credentials["connection_string"]; present && raw != nil {
			connection, ok := raw.(string)
			if !ok || len(connection) < 4 {
				return compatibilitySecretErrorf("Azure connection_string must be at least 4 characters")
			}
			if !compatibilityAzureAccount.MatchString(connection) {
				return compatibilitySecretErrorf("Azure connection_string must include AccountName")
			}
			if !compatibilityAzureAccountKey.MatchString(connection) {
				return compatibilitySecretErrorf("Azure connection_string must include AccountKey (SAS tokens are not supported)")
			}
			return nil
		}
		tenant, tenantOK := credentials["tenant_id"].(string)
		client, clientOK := credentials["client_id"].(string)
		clientSecret, secretOK := credentials["client_secret"].(string)
		if !tenantOK || !(compatibilityGUIDPattern.MatchString(tenant) || compatibilityAzureDomain.MatchString(tenant)) {
			return compatibilitySecretErrorf("Azure tenant_id must be a GUID or a verified domain (e.g. contoso.onmicrosoft.com)")
		}
		if !clientOK || !compatibilityGUIDPattern.MatchString(client) {
			return compatibilitySecretErrorf("Azure client_id must be a GUID (application/client ID from the app registration)")
		}
		if !secretOK || len(clientSecret) < 4 {
			return compatibilitySecretErrorf("Azure client_secret must be at least 4 characters")
		}
		if raw, present := credentials["subscription_id"]; present && raw != nil {
			subscription, ok := raw.(string)
			if !ok || !compatibilityGUIDPattern.MatchString(subscription) {
				return compatibilitySecretErrorf("Azure subscription_id must be a GUID when provided")
			}
		}
	case "modal":
		tokenID, idOK := credentials["token_id"].(string)
		tokenSecret, secretOK := credentials["token_secret"].(string)
		if !idOK || tokenID == "" {
			return compatibilitySecretErrorf("Modal credentials require a non-empty 'token_id'")
		}
		if !secretOK || tokenSecret == "" {
			return compatibilitySecretErrorf("Modal credentials require a non-empty 'token_secret'")
		}
	case "literature":
		for _, field := range compatibilityLiteratureFields {
			if value, present := credentials[field]; present && value != nil {
				if _, ok := value.(string); !ok {
					return compatibilitySecretErrorf("Literature field '%s' must be a string", field)
				}
			}
		}
	}
	return nil
}

func compatibilityGenericSecretName(value string) (string, error) {
	value = strings.ToUpper(strings.TrimSpace(value))
	if compatibilityUTF16Units(value) == 0 || compatibilityUTF16Units(value) > 128 {
		return "", compatibilitySecretErrorf("Secret name must be 1-128 characters")
	}
	if !compatibilitySecretNamePattern.MatchString(value) {
		return "", compatibilitySecretErrorf("Secret name must be a valid environment variable name (letters, digits, underscores; must start with a letter)")
	}
	if _, reserved := compatibilityReservedSecretNames[value]; reserved {
		return "", compatibilitySecretErrorf("Secret name '%s' is reserved and cannot be used", value)
	}
	return value, nil
}

func compatibilityProviderSecretName(value string) string {
	var result strings.Builder
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			continue
		}
		result.WriteRune(character)
	}
	return compatibilityUTF16PrefixString(result.String(), 128)
}

func compatibilityCredentialsMerge(provider, credentialType string) bool {
	return provider == "aws" || provider == "azure" || provider == "modal" || provider == "literature" ||
		provider == "gcp" && credentialType == "hmac_key"
}

func compatibilitySecretNameExists(existing []secretstore.Secret, userID, name, excludedID string) bool {
	for _, item := range existing {
		if item.UserID == userID && item.ID != excludedID && item.Name == name {
			return true
		}
	}
	return false
}

func compatibilityServiceAccountObject(value any) (map[string]any, error) {
	if text, ok := value.(string); ok {
		var object map[string]any
		if err := json.Unmarshal([]byte(text), &object); err != nil {
			return nil, compatibilitySecretErrorf("GCP service_account_json is not valid JSON")
		}
		return object, nil
	}
	if object, ok := value.(map[string]any); ok && object != nil {
		return object, nil
	}
	return nil, compatibilitySecretErrorf("GCP service_account_json is not valid JSON")
}

func compatibilityCredentialString(credentials map[string]any, key string) string {
	value, _ := credentials[key].(string)
	return value
}

func compatibilityTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return typed != ""
	case bool:
		return typed
	case json.Number:
		return typed.String() != "0"
	case float64:
		return typed != 0
	default:
		return true
	}
}

func compatibilityUTF16Units(value string) int {
	units := 0
	for _, character := range value {
		if character > 0xffff {
			units += 2
		} else {
			units++
		}
	}
	return units
}

func compatibilityUTF16PrefixString(value string, limit int) string {
	var output strings.Builder
	used := 0
	for _, character := range value {
		width := 1
		if character > 0xffff {
			width = 2
		}
		if used+width > limit {
			break
		}
		output.WriteRune(character)
		used += width
	}
	return output.String()
}

func compatibilitySecretErrorf(format string, args ...any) error {
	return compatibilitySecretValidationError{message: fmt.Sprintf(format, args...)}
}
