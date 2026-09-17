package v11

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	secretstore "synon-go/internal/persistence/secrets"
	settingsstore "synon-go/internal/persistence/settings"
)

type legacyModelProfiles struct {
	Version         int                  `json:"version"`
	ActiveProfileID string               `json:"activeProfileId"`
	Profiles        []legacyModelProfile `json:"profiles"`
}

type legacyModelProfile struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Provider  string    `json:"provider"`
	BaseURL   string    `json:"baseUrl"`
	Model     string    `json:"model"`
	APIKey    string    `json:"apiKey,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func importModelProfiles(ctx context.Context, sourceHome, targetHome, targetDB string) (int64, error) {
	path := filepath.Join(sourceHome, "llm-providers.json")
	raw, err := readOptionalRegularFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read v1.1 model profiles: %w", err)
	}
	var source legacyModelProfiles
	if err := json.Unmarshal(raw, &source); err != nil {
		return 0, fmt.Errorf("decode v1.1 model profiles: %w", err)
	}
	db, err := sql.Open("sqlite", targetDB)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	userID, err := primaryMigratedUser(ctx, db)
	if err != nil {
		return 0, err
	}
	vault := secretstore.New(targetHome)
	now := time.Now().UTC()
	sanitized := legacyModelProfiles{Version: source.Version, ActiveProfileID: source.ActiveProfileID}
	for _, profile := range source.Profiles {
		profile.ID = strings.TrimSpace(profile.ID)
		profile.Name = strings.TrimSpace(profile.Name)
		profile.Provider = strings.TrimSpace(profile.Provider)
		profile.BaseURL = strings.TrimSpace(profile.BaseURL)
		profile.Model = strings.TrimSpace(profile.Model)
		if profile.ID == "" || profile.Name == "" || profile.Provider == "" || profile.BaseURL == "" || profile.Model == "" {
			return 0, errors.New("v1.1 model profile has incomplete identity fields")
		}
		createdAt, updatedAt := profile.CreatedAt.UTC(), profile.UpdatedAt.UTC()
		if createdAt.IsZero() {
			createdAt = now
		}
		if updatedAt.IsZero() {
			updatedAt = createdAt
		}
		secretRef := ""
		if profile.APIKey != "" {
			secretID := "v11-model-" + profile.ID
			if _, err := vault.Create(secretstore.Secret{
				ID: secretID, UserID: userID, Provider: profile.Provider, Name: profile.Name,
				Value: profile.APIKey, CredentialType: "api-key",
				Description: "Migrated from SynonBiomed v1.1 model profile " + profile.ID,
			}); err != nil {
				return 0, fmt.Errorf("encrypt v1.1 model credential %s: %w", profile.ID, err)
			}
			secretRef = "secret://" + secretID
		}
		if _, err := db.ExecContext(ctx, `
			INSERT INTO model_providers (id, user_id, name, type, base_url, model, secret_ref, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			profile.ID, userID, profile.Name, profile.Provider, profile.BaseURL, profile.Model,
			secretRef, createdAt, updatedAt,
		); err != nil {
			return 0, fmt.Errorf("import v1.1 model profile %s: %w", profile.ID, err)
		}
		profile.APIKey = ""
		sanitized.Profiles = append(sanitized.Profiles, profile)
	}
	metadataPath := filepath.Join(targetHome, "migration", "v1.1", "llm-providers.metadata.json")
	if err := os.MkdirAll(filepath.Dir(metadataPath), 0o700); err != nil {
		return 0, err
	}
	metadata, err := json.MarshalIndent(sanitized, "", "  ")
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(metadataPath, append(metadata, '\n'), 0o600); err != nil {
		return 0, err
	}
	settings := settingsstore.New(filepath.Join(targetHome, "settings.json"))
	if source.ActiveProfileID != "" {
		if _, err := settings.Set("model.activeProviderId", source.ActiveProfileID); err != nil {
			return 0, err
		}
	}
	return int64(len(source.Profiles)), nil
}

func importPreferences(ctx context.Context, sourceHome, targetHome, targetDB string) error {
	path := filepath.Join(sourceHome, "preferences.json")
	raw, err := readOptionalRegularFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read v1.1 preferences: %w", err)
	}
	var preferences map[string]any
	if err := json.Unmarshal(raw, &preferences); err != nil {
		return fmt.Errorf("decode v1.1 preferences: %w", err)
	}
	vault := secretstore.New(targetHome)
	if _, err := vault.Create(secretstore.Secret{
		ID: "v11-preferences-archive", UserID: secretstore.DefaultUserID,
		Provider: SchemaName, Name: "preferences.json", Value: string(raw),
		CredentialType: "encrypted-archive", Description: "Exact pre-migration preferences payload",
	}); err != nil {
		return fmt.Errorf("encrypt v1.1 preferences archive: %w", err)
	}
	settings := settingsstore.New(filepath.Join(targetHome, "settings.json"))
	keys := make([]string, 0, len(preferences))
	for key := range preferences {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := redactSensitivePreference(preferences[key], key)
		if _, err := settings.Set("legacy.v1_1."+safeSettingSegment(key), value); err != nil {
			return fmt.Errorf("import v1.1 preference %s: %w", key, err)
		}
	}
	for sourceKey, targetKey := range map[string]string{
		"userAllowedDomains":             "network.allowedDomains",
		"builtinAllowlistDisabled":       "network.builtinAllowlistDisabled",
		"builtinAllowlistDisabledGroups": "network.builtinAllowlistDisabledGroups",
		"allowlistOnboardingSeen":        "onboarding.allowlistSeen",
		"firstRunOnboardingComplete":     "onboarding.firstRunCompleted",
	} {
		if value, ok := preferences[sourceKey]; ok {
			if _, err := settings.Set(targetKey, redactSensitivePreference(value, sourceKey)); err != nil {
				return err
			}
		}
	}
	db, err := sql.Open("sqlite", targetDB)
	if err != nil {
		return err
	}
	defer db.Close()
	var latestIntent string
	if err := db.QueryRowContext(ctx, `
		SELECT intent FROM use_intent_declarations ORDER BY created_at DESC, id DESC LIMIT 1`).Scan(&latestIntent); err == nil {
		if _, err := settings.Set("onboarding.useIntent", latestIntent); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

func primaryMigratedUser(ctx context.Context, db *sql.DB) (string, error) {
	var userID string
	err := db.QueryRowContext(ctx, `SELECT user_id FROM projects WHERE user_id <> '' ORDER BY user_id LIMIT 1`).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return secretstore.DefaultUserID, nil
	}
	if err != nil {
		return "", err
	}
	return userID, nil
}

func readOptionalRegularFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("source state file is not a regular non-symlink file")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return nil, errors.New("source state file path contains symbolic links")
	}
	return os.ReadFile(path)
}

func redactSensitivePreference(value any, path string) any {
	if sensitivePreferenceKey(path) {
		return map[string]any{"secretRef": "secret://v11-preferences-archive#" + path}
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			childPath := path + "." + key
			out[key] = redactSensitivePreference(child, childPath)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, child := range typed {
			out[index] = redactSensitivePreference(child, fmt.Sprintf("%s.%d", path, index))
		}
		return out
	default:
		return value
	}
}

func sensitivePreferenceKey(path string) bool {
	lower := strings.ToLower(path)
	for _, token := range []string{"apikey", "api_key", "token", "password", "secret", "credential", "privatekey", "private_key"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func safeSettingSegment(value string) string {
	var builder strings.Builder
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z', character >= '0' && character <= '9', character == '_', character == '-':
			builder.WriteRune(character)
		default:
			builder.WriteByte('_')
		}
	}
	if builder.Len() == 0 {
		return "unknown"
	}
	return builder.String()
}
