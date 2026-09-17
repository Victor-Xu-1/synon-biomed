package server

import (
	"fmt"
	"strings"
	"time"

	secretstore "synon-go/internal/persistence/secrets"
)

type compatibilitySecretProjection struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Provider       string            `json:"provider"`
	CredentialType *string           `json:"credential_type"`
	Buckets        []string          `json:"buckets"`
	Region         *string           `json:"region"`
	Description    *string           `json:"description"`
	MaskedPreview  *string           `json:"masked_preview"`
	MaskedFields   map[string]string `json:"masked_fields"`
	CreatedAt      string            `json:"created_at"`
	UpdatedAt      string            `json:"updated_at"`
}

func projectCompatibilitySecret(secret secretstore.Secret) compatibilitySecretProjection {
	projection := compatibilitySecretProjection{
		ID: secret.ID, Name: secret.Name, Provider: secret.Provider,
		MaskedPreview: compatibilitySecretMaskedPreview(secret),
		MaskedFields:  compatibilitySecretMaskedFields(secret),
		CreatedAt:     compatibilitySecretTimestamp(secret.CreatedAt), UpdatedAt: compatibilitySecretTimestamp(secret.UpdatedAt),
	}
	if secret.CredentialTypePresent || secret.CredentialType != "" {
		value := secret.CredentialType
		projection.CredentialType = &value
	}
	if secret.BucketsPresent || secret.Buckets != nil {
		projection.Buckets = append([]string{}, secret.Buckets...)
	}
	if secret.RegionPresent || secret.Region != "" {
		value := secret.Region
		projection.Region = &value
	}
	if secret.DescriptionPresent || secret.Description != "" {
		value := secret.Description
		projection.Description = &value
	}
	return projection
}

func compatibilitySecretMaskedPreview(secret secretstore.Secret) *string {
	credentials := secret.CredentialObject()
	var result string
	switch secret.Provider {
	case "generic":
		result = compatibilityMask(secret.Value)
	case "github":
		value, ok := credentials["token"].(string)
		if !ok {
			return nil
		}
		result = compatibilityMask(value)
	case "aws":
		value, ok := credentials["access_key_id"].(string)
		if !ok {
			return nil
		}
		result = compatibilityMask(value)
	case "gcp":
		result = compatibilityGCPPreview(credentials)
	case "azure":
		if connection, ok := credentials["connection_string"].(string); ok {
			match := compatibilityAzureAccount.FindStringSubmatch(connection)
			if len(match) == 2 {
				result = compatibilityMask(strings.TrimSpace(match[1]))
			} else {
				result = "(connection string)"
			}
		} else if client, ok := credentials["client_id"].(string); ok {
			result = compatibilityMask(client)
		} else {
			return nil
		}
	case "modal":
		value, ok := credentials["token_id"].(string)
		if !ok {
			return nil
		}
		result = compatibilityMask(value)
	case "literature":
		count := 0
		for _, value := range credentials {
			if text, ok := value.(string); ok && text != "" {
				count++
			}
		}
		if count == 0 {
			result = "(empty)"
		} else {
			result = fmt.Sprintf("%d key(s) configured", count)
		}
	default:
		return nil
	}
	return &result
}

func compatibilitySecretMaskedFields(secret secretstore.Secret) map[string]string {
	credentials := secret.CredentialObject()
	switch secret.Provider {
	case "generic":
		return map[string]string{"value": "****"}
	case "github":
		return map[string]string{"token": compatibilityMask(compatibilityCredentialString(credentials, "token"))}
	case "aws":
		result := map[string]string{
			"access_key_id":     compatibilityMask(compatibilityCredentialString(credentials, "access_key_id")),
			"secret_access_key": compatibilityMask(compatibilityCredentialString(credentials, "secret_access_key")),
		}
		if endpoint, ok := credentials["endpoint"].(string); ok {
			result["endpoint"] = endpoint
		}
		return result
	case "gcp":
		if _, hmac := credentials["access_key_id"]; hmac {
			result := map[string]string{
				"access_key_id":     compatibilityMask(compatibilityCredentialString(credentials, "access_key_id")),
				"secret_access_key": strings.Repeat("\u2022", 8),
			}
			if projectID, ok := credentials["project_id"].(string); ok {
				result["project_id"] = projectID
			}
			return result
		}
		return map[string]string{"service_account_json": compatibilityGCPPreview(credentials)}
	case "azure":
		if _, connection := credentials["connection_string"].(string); connection {
			return map[string]string{"connection_string": strings.Repeat("\u2022", 8)}
		}
		result := map[string]string{
			"tenant_id":     compatibilityCredentialString(credentials, "tenant_id"),
			"client_id":     compatibilityCredentialString(credentials, "client_id"),
			"client_secret": strings.Repeat("\u2022", 8),
		}
		if subscription, ok := credentials["subscription_id"].(string); ok {
			result["subscription_id"] = subscription
		}
		return result
	case "modal":
		return map[string]string{
			"token_id":     compatibilityMask(compatibilityCredentialString(credentials, "token_id")),
			"token_secret": strings.Repeat("\u2022", 8),
		}
	case "literature":
		result := map[string]string{"email": ""}
		if email, ok := credentials["email"].(string); ok && email != "" {
			result["email"] = compatibilityMask(email)
		}
		for _, field := range compatibilityLiteratureFields {
			if value, ok := credentials[field].(string); ok && value != "" {
				if field == "ezproxy_url" {
					result[field] = value
				} else {
					result[field] = compatibilityMask(value)
				}
			}
		}
		return result
	default:
		return nil
	}
}

func compatibilityGCPPreview(credentials map[string]any) string {
	if value, ok := credentials["access_key_id"].(string); ok {
		if _, serviceAccount := credentials["service_account_json"]; !serviceAccount {
			return compatibilityMask(value)
		}
	}
	object, err := compatibilityServiceAccountObject(credentials["service_account_json"])
	if err != nil {
		return "service_account"
	}
	projectID, _ := object["project_id"].(string)
	email, _ := object["client_email"].(string)
	if projectID != "" && email != "" {
		prefix := strings.SplitN(email, "@", 2)[0]
		if compatibilityUTF16Units(prefix) > 20 {
			prefix = compatibilityUTF16PrefixString(prefix, 17) + "..."
		}
		return projectID + " \u2014 " + prefix
	}
	if projectID != "" {
		return projectID
	}
	return "service_account"
}

func compatibilityMask(value string) string {
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) >= 10 {
		return string(runes[:4]) + "\u00b7\u00b7\u00b7\u00b7" + string(runes[len(runes)-4:])
	}
	return "****\u00b7\u00b7\u00b7\u00b7"
}

func compatibilitySecretTimestamp(value time.Time) string {
	return value.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}
