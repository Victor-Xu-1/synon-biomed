package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const DefaultModalAppName = "synonbiomed-default"

var ErrBYOCRevisionConflict = errors.New("BYOC settings revision conflict")

type BYOCSettings struct {
	Provider          string         `json:"provider"`
	Enabled           bool           `json:"enabled"`
	DetailsMD         string         `json:"detailsMd"`
	MaxConcurrentJobs *int           `json:"maxConcurrentJobs,omitempty"`
	MaxTimeoutSec     *int           `json:"maxTimeoutSec,omitempty"`
	AppName           string         `json:"appName,omitempty"`
	PriorAppNames     []string       `json:"-"`
	EgressPolicy      map[string]any `json:"egressPolicy,omitempty"`
	EnvironmentName   string         `json:"environmentName,omitempty"`
	DetailsRev        int64          `json:"-"`
}

func (s *Store) GetBYOCSettings(provider, userID string) (BYOCSettings, bool, error) {
	name, err := normalizeBYOCProvider(provider)
	if err != nil {
		return BYOCSettings{}, false, err
	}
	var settings BYOCSettings
	var maxJobs, maxTimeout sql.NullInt64
	var appName, egress, environment sql.NullString
	var prior string
	err = s.db.QueryRowContext(context.Background(), `
		SELECT enabled,details_md,max_concurrent_jobs,max_timeout_sec,app_name,prior_app_names,egress_policy,modal_environment,details_rev
		FROM compute_providers WHERE name=? AND owner_user_id=? AND family='byoc'`,
		name, strings.TrimSpace(userID)).Scan(
		&settings.Enabled, &settings.DetailsMD, &maxJobs, &maxTimeout,
		&appName, &prior, &egress, &environment, &settings.DetailsRev,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return BYOCSettings{Provider: strings.TrimPrefix(name, "byoc:"), PriorAppNames: []string{}}, false, nil
	}
	if err != nil {
		return BYOCSettings{}, false, fmt.Errorf("get BYOC settings: %w", err)
	}
	settings.Provider = strings.TrimPrefix(name, "byoc:")
	if maxJobs.Valid {
		value := int(maxJobs.Int64)
		settings.MaxConcurrentJobs = &value
	}
	if maxTimeout.Valid {
		value := int(maxTimeout.Int64)
		settings.MaxTimeoutSec = &value
	}
	if appName.Valid {
		settings.AppName = appName.String
	}
	if environment.Valid {
		settings.EnvironmentName = environment.String
	}
	if err := json.Unmarshal([]byte(prior), &settings.PriorAppNames); err != nil {
		return BYOCSettings{}, false, fmt.Errorf("decode BYOC prior apps: %w", err)
	}
	if egress.Valid && strings.TrimSpace(egress.String) != "" {
		if err := json.Unmarshal([]byte(egress.String), &settings.EgressPolicy); err != nil {
			return BYOCSettings{}, false, fmt.Errorf("decode BYOC egress policy: %w", err)
		}
	}
	return settings, true, nil
}

func (s *Store) SetBYOCEnabled(provider, userID string, enabled bool) (BYOCSettings, error) {
	name, err := normalizeBYOCProvider(provider)
	if err != nil {
		return BYOCSettings{}, err
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return BYOCSettings{}, errors.New("BYOC user id is required")
	}
	now := s.now().UTC()
	appName := any(nil)
	if enabled {
		appName = DefaultModalAppName
	}
	result, err := s.db.ExecContext(context.Background(), `
		INSERT INTO compute_providers(name,owner_user_id,family,environments,updated_at,enabled,app_name)
		VALUES(?,?,'byoc','[]',?,?,?)
		ON CONFLICT(name) DO UPDATE SET
			enabled=excluded.enabled,
			app_name=CASE WHEN compute_providers.app_name IS NULL AND excluded.enabled=1 THEN excluded.app_name ELSE compute_providers.app_name END,
			updated_at=excluded.updated_at
		WHERE compute_providers.owner_user_id=excluded.owner_user_id AND compute_providers.family='byoc'`,
		name, userID, now, enabled, appName)
	if err != nil {
		return BYOCSettings{}, fmt.Errorf("set BYOC enabled: %w", err)
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return BYOCSettings{}, fmt.Errorf("BYOC provider %q belongs to another user or family", name)
	}
	settings, _, err := s.GetBYOCSettings(provider, userID)
	return settings, err
}

func (s *Store) UpdateBYOCSettings(provider, userID string, update BYOCSettings) (BYOCSettings, error) {
	name, err := normalizeBYOCProvider(provider)
	if err != nil {
		return BYOCSettings{}, err
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BYOCSettings{}, err
	}
	defer tx.Rollback()
	var currentApp, priorRaw sql.NullString
	var currentRev int64
	if err := tx.QueryRowContext(ctx, `
		SELECT app_name,prior_app_names,details_rev FROM compute_providers
		WHERE name=? AND owner_user_id=? AND family='byoc'`,
		name, strings.TrimSpace(userID)).Scan(&currentApp, &priorRaw, &currentRev); err != nil {
		return BYOCSettings{}, err
	}
	if currentRev != update.DetailsRev {
		return BYOCSettings{}, ErrBYOCRevisionConflict
	}
	priorApps := append([]string(nil), update.PriorAppNames...)
	if currentApp.Valid && currentApp.String != "" && currentApp.String != update.AppName {
		priorApps = append(priorApps, currentApp.String)
	}
	priorApps = normalizePriorAppNames(priorApps, update.AppName)
	priorJSON, err := json.Marshal(priorApps)
	if err != nil {
		return BYOCSettings{}, err
	}
	var appName, environment any
	if update.AppName != "" {
		appName = update.AppName
	}
	if update.EnvironmentName != "" {
		environment = update.EnvironmentName
	}
	var egress any
	if update.EgressPolicy != nil {
		raw, marshalErr := json.Marshal(update.EgressPolicy)
		if marshalErr != nil {
			return BYOCSettings{}, marshalErr
		}
		egress = string(raw)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE compute_providers SET details_md=?,max_concurrent_jobs=?,max_timeout_sec=?,
			app_name=?,prior_app_names=?,egress_policy=?,modal_environment=?,details_rev=details_rev+1,updated_at=?
		WHERE name=? AND owner_user_id=? AND family='byoc' AND details_rev=?`,
		update.DetailsMD, update.MaxConcurrentJobs, update.MaxTimeoutSec,
		appName, string(priorJSON), egress, environment, s.now().UTC(), name, strings.TrimSpace(userID), update.DetailsRev)
	if err != nil {
		return BYOCSettings{}, fmt.Errorf("update BYOC settings: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return BYOCSettings{}, err
	}
	if rows != 1 {
		return BYOCSettings{}, ErrBYOCRevisionConflict
	}
	if err := tx.Commit(); err != nil {
		return BYOCSettings{}, err
	}
	settings, found, err := s.GetBYOCSettings(provider, userID)
	if err != nil {
		return BYOCSettings{}, err
	}
	if !found {
		return BYOCSettings{}, sql.ErrNoRows
	}
	return settings, nil
}

func normalizeBYOCProvider(provider string) (string, error) {
	provider = strings.TrimSpace(strings.TrimPrefix(provider, "byoc:"))
	if provider != "modal" {
		return "", fmt.Errorf("unknown BYOC provider %q", provider)
	}
	return "byoc:" + provider, nil
}

func (s *Store) HasEnabledBYOCProvider(provider string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("workspace store is unavailable")
	}
	name, err := normalizeBYOCProvider(provider)
	if err != nil {
		return false, err
	}
	var enabled int
	err = s.db.QueryRow(`SELECT EXISTS(
		SELECT 1 FROM compute_providers WHERE name=? AND family='byoc' AND enabled=1
	)`, name).Scan(&enabled)
	if err != nil {
		return false, fmt.Errorf("inspect enabled BYOC provider: %w", err)
	}
	return enabled == 1, nil
}

func normalizePriorAppNames(values []string, current string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for index := len(values) - 1; index >= 0; index-- {
		value := strings.TrimSpace(values[index])
		if value == "" || value == current {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
		if len(out) == 8 {
			break
		}
	}
	for left, right := 0, len(out)-1; left < right; left, right = left+1, right-1 {
		out[left], out[right] = out[right], out[left]
	}
	return out
}
