package workspace

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var ErrManagedEndpointStopInProgress = errors.New("managed endpoint stop is already in progress")
var ErrManagedEndpointStartInProgress = errors.New("managed endpoint start is already in progress")
var ErrManagedEndpointFailed = errors.New("managed endpoint is failed and must be stopped or re-registered before retry")
var ErrManagedEndpointOwnerConflict = errors.New("managed endpoint belongs to another owner")

const managedEndpointClaimHandlePrefix = "synon-claim-v1."

type ComputeGPUSettings struct {
	Enabled  bool   `json:"enabled"`
	Override *bool  `json:"override"`
	Present  bool   `json:"present"`
	Name     string `json:"name"`
}

type ComputeBioNeMoSettings struct {
	Enabled    bool   `json:"enabled"`
	Override   *bool  `json:"override"`
	Mode       string `json:"mode"`
	HostedHost string `json:"hostedHost"`
}

type ManagedEndpoint struct {
	Name               string     `json:"name"`
	URL                string     `json:"url"`
	Port               int        `json:"port"`
	State              string     `json:"state"`
	Location           string     `json:"location"`
	SkillName          string     `json:"skillName"`
	CredentialName     *string    `json:"credentialName"`
	LivePath           string     `json:"livePath"`
	StartScript        string     `json:"startScript"`
	StopScript         string     `json:"stopScript"`
	ApprovedScriptHash string     `json:"approvedScriptHash"`
	LastError          *string    `json:"lastError"`
	Transcript         *string    `json:"transcript"`
	StateChangedAt     *time.Time `json:"-"`
	StateChangedAtISO  *string    `json:"stateChangedAt"`
	ServiceDir         string     `json:"serviceDir"`
	ServiceDirBytes    *int64     `json:"serviceDirBytes"`
	RegisteredBy       string     `json:"-"`
	ClaimToken         string     `json:"-"`
	ClaimGeneration    int64      `json:"-"`
	ClaimHandle        string     `json:"-"`
}

type ComputeJob struct {
	JobID           string    `json:"jobId"`
	Environment     string    `json:"environment"`
	TierType        string    `json:"tierType"`
	Provider        string    `json:"provider"`
	FrameID         *string   `json:"frameId"`
	ProjectID       string    `json:"projectId"`
	State           string    `json:"state"`
	StartedAt       time.Time `json:"startedAt"`
	StartedAtISO    string    `json:"startedAtIso"`
	Intent          any       `json:"intent"`
	HardwareDetails any       `json:"hardwareDetails"`
	OriginToolUseID *string   `json:"originToolUseId"`
	RootFrameID     *string   `json:"rootFrameId"`
	ProviderFamily  string    `json:"providerFamily"`
	ProviderLabel   string    `json:"providerLabel"`
	ExternalID      *string   `json:"externalId"`
	ExternalURL     *string   `json:"externalUrl"`
	SupportsTail    bool      `json:"supportsTail"`
	EndedAtISO      *string   `json:"endedAtIso,omitempty"`
	ErrorKind       *string   `json:"errorKind,omitempty"`
	LeftOnRemote    []string  `json:"leftOnRemote,omitempty"`
	SystemHint      *string   `json:"systemHint,omitempty"`
}

type ComputeJobLog struct {
	Exists    bool   `json:"exists"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
	Text      string `json:"text"`
}

func (s *Store) ensureComputeWorkbenchSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	return s.ensureComputeWorkbenchSchemaWithExecutor(ctx, s.db)
}

func (s *Store) ensureComputeWorkbenchSchemaWithExecutor(ctx context.Context, executor schemaMigrationExecutor) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS compute_host_settings (id INTEGER PRIMARY KEY CHECK (id = 1), gpu_enabled INTEGER NOT NULL DEFAULT 0, gpu_override INTEGER, gpu_present INTEGER NOT NULL DEFAULT 0, gpu_name TEXT NOT NULL DEFAULT '', updated_at TIMESTAMP NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS compute_bionemo_settings (id INTEGER PRIMARY KEY CHECK (id = 1), enabled INTEGER NOT NULL DEFAULT 0, enabled_override INTEGER, mode TEXT NOT NULL DEFAULT 'hosted', hosted_host TEXT NOT NULL DEFAULT 'health.api.nvidia.com', updated_at TIMESTAMP NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS compute_managed_endpoints (name TEXT PRIMARY KEY, owner_user_id TEXT, url TEXT NOT NULL, port INTEGER NOT NULL, state TEXT NOT NULL, location TEXT NOT NULL DEFAULT 'local', skill_name TEXT NOT NULL, credential_name TEXT, live_path TEXT NOT NULL, start_script TEXT NOT NULL, stop_script TEXT NOT NULL, approved_script_hash TEXT NOT NULL, last_error TEXT, transcript TEXT, state_changed_at TIMESTAMP NOT NULL, service_dir TEXT NOT NULL DEFAULT '', service_dir_bytes INTEGER, stop_claim_token TEXT NOT NULL DEFAULT '', stop_claim_generation INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS compute_workbench_jobs (job_id TEXT PRIMARY KEY, owner_user_id TEXT NOT NULL, project_id TEXT NOT NULL, frame_id TEXT, environment TEXT NOT NULL, tier_type TEXT NOT NULL, provider TEXT NOT NULL, state TEXT NOT NULL, started_at TIMESTAMP NOT NULL, ended_at TIMESTAMP, intent_json TEXT, hardware_json TEXT, origin_tool_use_id TEXT, root_frame_id TEXT, provider_family TEXT NOT NULL, provider_label TEXT NOT NULL, external_id TEXT, external_url TEXT, supports_tail INTEGER NOT NULL DEFAULT 0, error_kind TEXT, left_on_remote_json TEXT NOT NULL DEFAULT '[]', system_hint TEXT, stdout_text TEXT NOT NULL DEFAULT '', stderr_text TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE IF NOT EXISTS compute_reconcile_leases (owner_user_id TEXT NOT NULL, provider TEXT NOT NULL, holder TEXT NOT NULL, expires_at TIMESTAMP NOT NULL, heartbeat_at TIMESTAMP NOT NULL, PRIMARY KEY(owner_user_id,provider))`,
		`CREATE TABLE IF NOT EXISTS compute_session_enabled (root_frame_id TEXT NOT NULL, owner_user_id TEXT NOT NULL, provider_name TEXT NOT NULL, checked INTEGER NOT NULL, updated_at TIMESTAMP NOT NULL, PRIMARY KEY(owner_user_id, root_frame_id, provider_name))`,
	}
	for _, statement := range statements {
		if _, err := executor.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate compute workbench: %w", err)
		}
	}
	if err := s.ensureComputeWorkbenchColumns(ctx, executor); err != nil {
		return err
	}
	if err := s.ensureSessionComputeOwnerKey(ctx, executor); err != nil {
		return err
	}
	for _, statement := range []string{
		`CREATE INDEX IF NOT EXISTS compute_workbench_jobs_owner_project_state_idx ON compute_workbench_jobs(owner_user_id, project_id, state, started_at DESC)`,
		`DROP INDEX IF EXISTS compute_workbench_jobs_owner_provider_external_idx`,
		`CREATE UNIQUE INDEX IF NOT EXISTS compute_workbench_jobs_owner_provider_active_external_idx ON compute_workbench_jobs(owner_user_id,provider,external_id) WHERE external_id IS NOT NULL AND state IN ('pending','staging','queued','running','harvesting')`,
	} {
		if _, err := executor.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create compute workbench index: %w", err)
		}
	}
	return nil
}

type computeSchemaColumn struct {
	name       string
	definition string
}

func (s *Store) ensureComputeWorkbenchColumns(ctx context.Context, executor schemaMigrationExecutor) error {
	tables := map[string][]computeSchemaColumn{
		"compute_host_settings": {
			{"gpu_enabled", "INTEGER NOT NULL DEFAULT 0"},
			{"gpu_override", "INTEGER"},
			{"gpu_present", "INTEGER NOT NULL DEFAULT 0"},
			{"gpu_name", "TEXT NOT NULL DEFAULT ''"},
			{"updated_at", "TIMESTAMP NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		},
		"compute_bionemo_settings": {
			{"enabled", "INTEGER NOT NULL DEFAULT 0"},
			{"enabled_override", "INTEGER"},
			{"mode", "TEXT NOT NULL DEFAULT 'hosted'"},
			{"hosted_host", "TEXT NOT NULL DEFAULT 'health.api.nvidia.com'"},
			{"updated_at", "TIMESTAMP NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		},
		"compute_managed_endpoints": {
			{"owner_user_id", "TEXT NOT NULL DEFAULT ''"},
			{"url", "TEXT NOT NULL DEFAULT ''"},
			{"port", "INTEGER NOT NULL DEFAULT 0"},
			{"state", "TEXT NOT NULL DEFAULT 'stopped'"},
			{"location", "TEXT NOT NULL DEFAULT 'local'"},
			{"skill_name", "TEXT NOT NULL DEFAULT ''"},
			{"credential_name", "TEXT"},
			{"live_path", "TEXT NOT NULL DEFAULT ''"},
			{"start_script", "TEXT NOT NULL DEFAULT ''"},
			{"stop_script", "TEXT NOT NULL DEFAULT ''"},
			{"approved_script_hash", "TEXT NOT NULL DEFAULT ''"},
			{"last_error", "TEXT"},
			{"transcript", "TEXT"},
			{"state_changed_at", "TIMESTAMP NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
			{"service_dir", "TEXT NOT NULL DEFAULT ''"},
			{"service_dir_bytes", "INTEGER"},
			{"stop_claim_token", "TEXT NOT NULL DEFAULT ''"},
			{"stop_claim_generation", "INTEGER NOT NULL DEFAULT 0"},
		},
		"compute_workbench_jobs": {
			{"owner_user_id", "TEXT NOT NULL DEFAULT 'local'"},
			{"project_id", "TEXT NOT NULL DEFAULT ''"},
			{"frame_id", "TEXT"},
			{"environment", "TEXT NOT NULL DEFAULT ''"},
			{"tier_type", "TEXT NOT NULL DEFAULT ''"},
			{"provider", "TEXT NOT NULL DEFAULT ''"},
			{"state", "TEXT NOT NULL DEFAULT 'pending'"},
			{"started_at", "TIMESTAMP NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
			{"ended_at", "TIMESTAMP"},
			{"intent_json", "TEXT"},
			{"hardware_json", "TEXT"},
			{"origin_tool_use_id", "TEXT"},
			{"root_frame_id", "TEXT"},
			{"provider_family", "TEXT NOT NULL DEFAULT 'unknown'"},
			{"provider_label", "TEXT NOT NULL DEFAULT ''"},
			{"external_id", "TEXT"},
			{"external_url", "TEXT"},
			{"supports_tail", "INTEGER NOT NULL DEFAULT 0"},
			{"error_kind", "TEXT"},
			{"left_on_remote_json", "TEXT NOT NULL DEFAULT '[]'"},
			{"system_hint", "TEXT"},
			{"stdout_text", "TEXT NOT NULL DEFAULT ''"},
			{"stderr_text", "TEXT NOT NULL DEFAULT ''"},
		},
		"compute_reconcile_leases": {
			{"owner_user_id", "TEXT NOT NULL DEFAULT 'local'"},
			{"provider", "TEXT NOT NULL DEFAULT ''"},
			{"holder", "TEXT NOT NULL DEFAULT ''"},
			{"expires_at", "TIMESTAMP NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
			{"heartbeat_at", "TIMESTAMP NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		},
		"compute_session_enabled": {
			{"root_frame_id", "TEXT NOT NULL DEFAULT ''"},
			{"owner_user_id", "TEXT NOT NULL DEFAULT 'local'"},
			{"provider_name", "TEXT NOT NULL DEFAULT ''"},
			{"checked", "INTEGER NOT NULL DEFAULT 0"},
			{"updated_at", "TIMESTAMP NOT NULL DEFAULT '1970-01-01T00:00:00Z'"},
		},
	}
	for table, columns := range tables {
		rows, err := executor.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
		if err != nil {
			return fmt.Errorf("inspect %s columns: %w", table, err)
		}
		found := map[string]bool{}
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, columnType string
			var defaultValue sql.NullString
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan %s column: %w", table, err)
			}
			found[name] = true
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, column := range columns {
			if found[column.name] {
				continue
			}
			if _, err := executor.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+column.name+` `+column.definition); err != nil {
				return fmt.Errorf("add %s.%s: %w", table, column.name, err)
			}
		}
	}
	return nil
}

func (s *Store) ensureSessionComputeOwnerKey(ctx context.Context, executor schemaMigrationExecutor) error {
	rows, err := executor.QueryContext(ctx, `PRAGMA table_info(compute_session_enabled)`)
	if err != nil {
		return fmt.Errorf("inspect compute session primary key: %w", err)
	}
	primaryKey := map[int]string{}
	for rows.Next() {
		var cid, notNull, keyPosition int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &keyPosition); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan compute session primary key: %w", err)
		}
		if keyPosition > 0 {
			primaryKey[keyPosition] = name
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if primaryKey[1] == "owner_user_id" && primaryKey[2] == "root_frame_id" && primaryKey[3] == "provider_name" && len(primaryKey) == 3 {
		return nil
	}

	tx, err := executor.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`ALTER TABLE compute_session_enabled RENAME TO compute_session_enabled_legacy_owner_key`,
		`CREATE TABLE compute_session_enabled (root_frame_id TEXT NOT NULL, owner_user_id TEXT NOT NULL, provider_name TEXT NOT NULL, checked INTEGER NOT NULL, updated_at TIMESTAMP NOT NULL, PRIMARY KEY(owner_user_id, root_frame_id, provider_name))`,
		`INSERT INTO compute_session_enabled(root_frame_id,owner_user_id,provider_name,checked,updated_at) SELECT root_frame_id,owner_user_id,provider_name,checked,updated_at FROM compute_session_enabled_legacy_owner_key`,
		`DROP TABLE compute_session_enabled_legacy_owner_key`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate compute session owner key: %w", err)
		}
	}
	return tx.Commit()
}

func (s *Store) GetComputeGPUSettings() (ComputeGPUSettings, error) {
	ctx := context.Background()
	if err := s.ensureComputeWorkbenchSchema(ctx); err != nil {
		return ComputeGPUSettings{}, err
	}
	var out ComputeGPUSettings
	var override sql.NullBool
	err := s.db.QueryRowContext(ctx, `SELECT gpu_enabled, gpu_override, gpu_present, gpu_name FROM compute_host_settings WHERE id=1`).Scan(&out.Enabled, &override, &out.Present, &out.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if override.Valid {
		value := override.Bool
		out.Override = &value
	}
	return out, nil
}

func (s *Store) SetComputeGPUEnabled(enabled bool) (ComputeGPUSettings, error) {
	ctx := context.Background()
	if err := s.ensureComputeWorkbenchSchema(ctx); err != nil {
		return ComputeGPUSettings{}, err
	}
	now := s.now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO compute_host_settings(id,gpu_enabled,gpu_override,gpu_present,gpu_name,updated_at) VALUES(1,?,?,0,'',?) ON CONFLICT(id) DO UPDATE SET gpu_enabled=excluded.gpu_enabled,gpu_override=excluded.gpu_override,updated_at=excluded.updated_at`, enabled, enabled, now)
	if err != nil {
		return ComputeGPUSettings{}, err
	}
	return s.GetComputeGPUSettings()
}

func (s *Store) GetComputeBioNeMoSettings() (ComputeBioNeMoSettings, error) {
	ctx := context.Background()
	if err := s.ensureComputeWorkbenchSchema(ctx); err != nil {
		return ComputeBioNeMoSettings{}, err
	}
	out := ComputeBioNeMoSettings{Mode: "local", HostedHost: "health.api.nvidia.com"}
	var override sql.NullBool
	err := s.db.QueryRowContext(ctx, `SELECT enabled, enabled_override, mode, hosted_host FROM compute_bionemo_settings WHERE id=1`).Scan(&out.Enabled, &override, &out.Mode, &out.HostedHost)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if override.Valid {
		value := override.Bool
		out.Override = &value
	}
	return out, nil
}

func (s *Store) SetComputeBioNeMoSettings(settings ComputeBioNeMoSettings) (ComputeBioNeMoSettings, error) {
	ctx := context.Background()
	if err := s.ensureComputeWorkbenchSchema(ctx); err != nil {
		return ComputeBioNeMoSettings{}, err
	}
	settings.Mode = strings.TrimSpace(settings.Mode)
	settings.HostedHost = strings.TrimSpace(settings.HostedHost)
	if settings.Mode == "" || settings.HostedHost == "" {
		return ComputeBioNeMoSettings{}, errors.New("BioNeMo mode and hosted host are required")
	}
	now := s.now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO compute_bionemo_settings(id,enabled,enabled_override,mode,hosted_host,updated_at) VALUES(1,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET enabled=excluded.enabled,enabled_override=excluded.enabled_override,mode=excluded.mode,hosted_host=excluded.hosted_host,updated_at=excluded.updated_at`,
		settings.Enabled, settings.Enabled, settings.Mode, settings.HostedHost, now)
	if err != nil {
		return ComputeBioNeMoSettings{}, err
	}
	return s.GetComputeBioNeMoSettings()
}

func (s *Store) UpsertManagedEndpoint(endpoint ManagedEndpoint) error {
	ctx := context.Background()
	if err := s.ensureComputeWorkbenchSchema(ctx); err != nil {
		return err
	}
	endpoint.Name = strings.TrimSpace(endpoint.Name)
	if endpoint.Name == "" || strings.TrimSpace(endpoint.RegisteredBy) == "" {
		return errors.New("managed endpoint name and owner are required")
	}
	if endpoint.State == "" {
		endpoint.State = "stopped"
	}
	if endpoint.Location == "" {
		endpoint.Location = "local"
	}
	changed := s.now().UTC()
	if endpoint.StateChangedAt != nil {
		changed = endpoint.StateChangedAt.UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO compute_managed_endpoints(name,owner_user_id,url,port,state,location,skill_name,credential_name,live_path,start_script,stop_script,approved_script_hash,last_error,transcript,state_changed_at,service_dir,service_dir_bytes) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(name) DO UPDATE SET url=excluded.url,port=excluded.port,state=excluded.state,location=excluded.location,skill_name=excluded.skill_name,credential_name=excluded.credential_name,live_path=excluded.live_path,start_script=excluded.start_script,stop_script=excluded.stop_script,approved_script_hash=excluded.approved_script_hash,last_error=excluded.last_error,transcript=excluded.transcript,state_changed_at=excluded.state_changed_at,service_dir=excluded.service_dir,service_dir_bytes=excluded.service_dir_bytes WHERE COALESCE(compute_managed_endpoints.owner_user_id,'')=excluded.owner_user_id`,
		endpoint.Name, endpoint.RegisteredBy, endpoint.URL, endpoint.Port, endpoint.State, endpoint.Location, endpoint.SkillName, endpoint.CredentialName, endpoint.LivePath, endpoint.StartScript, endpoint.StopScript, endpoint.ApprovedScriptHash, endpoint.LastError, endpoint.Transcript, changed, endpoint.ServiceDir, endpoint.ServiceDirBytes)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrManagedEndpointOwnerConflict
	}
	stored, err := scanManagedEndpoint(tx.QueryRowContext(ctx, `SELECT `+managedEndpointColumns+` FROM compute_managed_endpoints WHERE name=? AND COALESCE(owner_user_id,'')=?`, endpoint.Name, endpoint.RegisteredBy))
	if err != nil {
		return err
	}
	if err := s.enqueueManagedEndpointTx(ctx, tx, "managed_endpoint_update", "upserted", stored, ""); err != nil {
		return err
	}
	return tx.Commit()
}

const managedEndpointColumns = `name,url,port,state,location,skill_name,credential_name,live_path,start_script,stop_script,approved_script_hash,last_error,transcript,state_changed_at,service_dir,service_dir_bytes,COALESCE(owner_user_id,''),stop_claim_token,stop_claim_generation`

func scanManagedEndpoint(scanner interface{ Scan(...any) error }) (ManagedEndpoint, error) {
	var endpoint ManagedEndpoint
	var credential, lastError, transcript sql.NullString
	var changed time.Time
	var size sql.NullInt64
	err := scanner.Scan(&endpoint.Name, &endpoint.URL, &endpoint.Port, &endpoint.State, &endpoint.Location, &endpoint.SkillName, &credential, &endpoint.LivePath, &endpoint.StartScript, &endpoint.StopScript, &endpoint.ApprovedScriptHash, &lastError, &transcript, &changed, &endpoint.ServiceDir, &size, &endpoint.RegisteredBy, &endpoint.ClaimToken, &endpoint.ClaimGeneration)
	if err != nil {
		return endpoint, err
	}
	if credential.Valid {
		endpoint.CredentialName = &credential.String
	}
	if lastError.Valid {
		endpoint.LastError = &lastError.String
	}
	if transcript.Valid {
		endpoint.Transcript = &transcript.String
	}
	endpoint.StateChangedAt = &changed
	iso := changed.UTC().Format(time.RFC3339Nano)
	endpoint.StateChangedAtISO = &iso
	if size.Valid {
		endpoint.ServiceDirBytes = &size.Int64
	}
	return endpoint, nil
}

func (s *Store) ListManagedEndpoints(userID, name string, includeSizes bool) ([]ManagedEndpoint, error) {
	ctx := context.Background()
	if err := s.ensureComputeWorkbenchSchema(ctx); err != nil {
		return nil, err
	}
	query := `SELECT ` + managedEndpointColumns + ` FROM compute_managed_endpoints WHERE COALESCE(owner_user_id,'')=?`
	args := []any{strings.TrimSpace(userID)}
	if strings.TrimSpace(name) != "" {
		query += ` AND name=?`
		args = append(args, strings.TrimSpace(name))
	}
	query += ` ORDER BY name`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ManagedEndpoint{}
	for rows.Next() {
		endpoint, err := scanManagedEndpoint(rows)
		if err != nil {
			return nil, err
		}
		if !includeSizes {
			endpoint.ServiceDirBytes = nil
		}
		out = append(out, endpoint)
	}
	return out, rows.Err()
}

func (s *Store) HasManagedEndpoints() (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("workspace store is unavailable")
	}
	if err := s.ensureComputeWorkbenchSchema(context.Background()); err != nil {
		return false, err
	}
	var exists int
	if err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM compute_managed_endpoints)`).Scan(&exists); err != nil {
		return false, err
	}
	return exists == 1, nil
}

func (s *Store) ClaimManagedEndpointStop(name, userID string) (ManagedEndpoint, bool, error) {
	ctx := context.Background()
	if err := s.ensureComputeWorkbenchSchema(ctx); err != nil {
		return ManagedEndpoint{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ManagedEndpoint{}, false, err
	}
	defer tx.Rollback()
	endpoint, err := scanManagedEndpoint(tx.QueryRowContext(ctx, `SELECT `+managedEndpointColumns+` FROM compute_managed_endpoints WHERE name=? AND COALESCE(owner_user_id,'')=?`, strings.TrimSpace(name), strings.TrimSpace(userID)))
	if err != nil {
		return ManagedEndpoint{}, false, err
	}
	if endpoint.State == "stopped" {
		return endpoint, false, tx.Commit()
	}
	if endpoint.State == "stopping" {
		return ManagedEndpoint{}, false, ErrManagedEndpointStopInProgress
	}
	claimToken, err := newManagedEndpointClaimToken()
	if err != nil {
		return ManagedEndpoint{}, false, err
	}
	now := s.now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE compute_managed_endpoints SET state='stopping',state_changed_at=?,stop_claim_token=?,stop_claim_generation=stop_claim_generation+1 WHERE name=? AND COALESCE(owner_user_id,'')=? AND state=? AND stop_claim_generation=?`, now, claimToken, endpoint.Name, strings.TrimSpace(userID), endpoint.State, endpoint.ClaimGeneration)
	if err != nil {
		return ManagedEndpoint{}, false, err
	}
	if count, err := result.RowsAffected(); err != nil {
		return ManagedEndpoint{}, false, err
	} else if count != 1 {
		return ManagedEndpoint{}, false, ErrManagedEndpointStopInProgress
	}
	endpoint.State = "stopping"
	endpoint.StateChangedAt = &now
	endpoint.ClaimToken = claimToken
	endpoint.ClaimGeneration++
	endpoint.ClaimHandle = managedEndpointClaimHandle(endpoint.Name, endpoint.ClaimToken, endpoint.ClaimGeneration)
	if err := s.enqueueManagedEndpointTx(ctx, tx, "managed_endpoint_update", "stop_claimed", endpoint, ""); err != nil {
		return ManagedEndpoint{}, false, err
	}
	return endpoint, true, tx.Commit()
}

func (s *Store) ClaimManagedEndpointStart(name, userID string) (ManagedEndpoint, bool, error) {
	ctx := context.Background()
	if err := s.ensureComputeWorkbenchSchema(ctx); err != nil {
		return ManagedEndpoint{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ManagedEndpoint{}, false, err
	}
	defer tx.Rollback()
	endpoint, err := scanManagedEndpoint(tx.QueryRowContext(ctx, `SELECT `+managedEndpointColumns+` FROM compute_managed_endpoints WHERE name=? AND COALESCE(owner_user_id,'')=?`, strings.TrimSpace(name), strings.TrimSpace(userID)))
	if err != nil {
		return ManagedEndpoint{}, false, err
	}
	if endpoint.Location == "remote" || endpoint.State == "live" || endpoint.State == "running" {
		return endpoint, false, tx.Commit()
	}
	if endpoint.State == "failed" {
		return ManagedEndpoint{}, false, ErrManagedEndpointFailed
	}
	if endpoint.State == "starting" || endpoint.State == "stopping" {
		return ManagedEndpoint{}, false, ErrManagedEndpointStartInProgress
	}
	if endpoint.State != "stopped" {
		return ManagedEndpoint{}, false, fmt.Errorf("managed endpoint cannot start from state %q", endpoint.State)
	}
	claimToken, err := newManagedEndpointClaimToken()
	if err != nil {
		return ManagedEndpoint{}, false, err
	}
	now := s.now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE compute_managed_endpoints SET state='starting',state_changed_at=?,stop_claim_token=?,stop_claim_generation=stop_claim_generation+1 WHERE name=? AND COALESCE(owner_user_id,'')=? AND state='stopped' AND stop_claim_generation=?`, now, claimToken, endpoint.Name, strings.TrimSpace(userID), endpoint.ClaimGeneration)
	if err != nil {
		return ManagedEndpoint{}, false, err
	}
	if count, err := result.RowsAffected(); err != nil {
		return ManagedEndpoint{}, false, err
	} else if count != 1 {
		return ManagedEndpoint{}, false, ErrManagedEndpointStartInProgress
	}
	endpoint.State = "starting"
	endpoint.StateChangedAt = &now
	endpoint.ClaimToken = claimToken
	endpoint.ClaimGeneration++
	endpoint.ClaimHandle = managedEndpointClaimHandle(endpoint.Name, endpoint.ClaimToken, endpoint.ClaimGeneration)
	if err := s.enqueueManagedEndpointTx(ctx, tx, "managed_endpoint_update", "start_claimed", endpoint, ""); err != nil {
		return ManagedEndpoint{}, false, err
	}
	return endpoint, true, tx.Commit()
}

func (s *Store) FinishManagedEndpointStart(handle, transcript string, startErr error) (string, error) {
	ctx := context.Background()
	if err := s.ensureComputeWorkbenchSchema(ctx); err != nil {
		return "", err
	}
	endpointName, claimToken, claimGeneration, ok := parseManagedEndpointClaimHandle(handle)
	if !ok {
		return "", sql.ErrNoRows
	}
	state := "live"
	var lastError any
	if startErr != nil {
		state = "failed"
		lastError = startErr.Error()
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	endpoint, err := scanManagedEndpoint(tx.QueryRowContext(ctx, `SELECT `+managedEndpointColumns+` FROM compute_managed_endpoints WHERE name=? AND state='starting' AND stop_claim_token=? AND stop_claim_generation=?`, endpointName, claimToken, claimGeneration))
	if err != nil {
		return "", err
	}
	result, err := tx.ExecContext(ctx, `UPDATE compute_managed_endpoints SET state=?,last_error=?,transcript=?,state_changed_at=?,stop_claim_token='' WHERE name=? AND state='starting' AND stop_claim_token=? AND stop_claim_generation=?`, state, lastError, nullString(transcript), now, endpointName, claimToken, claimGeneration)
	if err != nil {
		return "", err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return "", sql.ErrNoRows
	}
	endpoint.State = state
	endpoint.StateChangedAt = &now
	endpoint.ClaimToken = ""
	if startErr != nil {
		value := startErr.Error()
		endpoint.LastError = &value
	}
	if strings.TrimSpace(transcript) != "" {
		if err := s.enqueueManagedEndpointTx(ctx, tx, "managed_endpoint_transcript", "start_finished", endpoint, transcript); err != nil {
			return "", err
		}
	}
	if err := s.enqueueManagedEndpointTx(ctx, tx, "managed_endpoint_update", "start_finished", endpoint, ""); err != nil {
		return "", err
	}
	return state, tx.Commit()
}

func (s *Store) FinishManagedEndpointStop(name, transcript string, stopErr error) (string, error) {
	ctx := context.Background()
	if err := s.ensureComputeWorkbenchSchema(ctx); err != nil {
		return "", err
	}
	endpointName, claimToken, claimGeneration, ok := parseManagedEndpointClaimHandle(name)
	if !ok {
		return "", sql.ErrNoRows
	}
	state := "stopped"
	var lastError any
	if stopErr != nil {
		state = "failed"
		lastError = stopErr.Error()
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	endpoint, err := scanManagedEndpoint(tx.QueryRowContext(ctx, `SELECT `+managedEndpointColumns+` FROM compute_managed_endpoints WHERE name=? AND state='stopping' AND stop_claim_token=? AND stop_claim_generation=?`, endpointName, claimToken, claimGeneration))
	if err != nil {
		return "", err
	}
	result, err := tx.ExecContext(ctx, `UPDATE compute_managed_endpoints SET state=?,last_error=?,transcript=?,state_changed_at=?,stop_claim_token='' WHERE name=? AND state='stopping' AND stop_claim_token=? AND stop_claim_generation=?`, state, lastError, nullString(transcript), now, endpointName, claimToken, claimGeneration)
	if err != nil {
		return "", err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return "", sql.ErrNoRows
	}
	endpoint.State = state
	endpoint.StateChangedAt = &now
	endpoint.ClaimToken = ""
	if stopErr != nil {
		value := stopErr.Error()
		endpoint.LastError = &value
	}
	if strings.TrimSpace(transcript) != "" {
		if err := s.enqueueManagedEndpointTx(ctx, tx, "managed_endpoint_transcript", "stop_finished", endpoint, transcript); err != nil {
			return "", err
		}
	}
	if err := s.enqueueManagedEndpointTx(ctx, tx, "managed_endpoint_update", "stop_finished", endpoint, ""); err != nil {
		return "", err
	}
	return state, tx.Commit()
}

func newManagedEndpointClaimToken() (string, error) {
	var token [24]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("create managed endpoint claim token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(token[:]), nil
}

func managedEndpointClaimHandle(name, token string, generation int64) string {
	return managedEndpointClaimHandlePrefix + base64.RawURLEncoding.EncodeToString([]byte(name)) + "." + strconv.FormatInt(generation, 10) + "." + token
}

func parseManagedEndpointClaimHandle(handle string) (string, string, int64, bool) {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(handle), managedEndpointClaimHandlePrefix), ".")
	if !strings.HasPrefix(strings.TrimSpace(handle), managedEndpointClaimHandlePrefix) || len(parts) != 3 || parts[2] == "" {
		return "", "", 0, false
	}
	name, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(name) == 0 {
		return "", "", 0, false
	}
	generation, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || generation <= 0 {
		return "", "", 0, false
	}
	return string(name), parts[2], generation, true
}

func (s *Store) DeleteManagedEndpoint(name, userID string) (bool, error) {
	ctx := context.Background()
	if err := s.ensureComputeWorkbenchSchema(ctx); err != nil {
		return false, err
	}
	name = strings.TrimSpace(name)
	userID = strings.TrimSpace(userID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	endpoint, err := scanManagedEndpoint(tx.QueryRowContext(ctx, `SELECT `+managedEndpointColumns+` FROM compute_managed_endpoints WHERE name=? AND COALESCE(owner_user_id,'')=?`, name, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM compute_managed_endpoints WHERE name=? AND COALESCE(owner_user_id,'')=?`, name, userID)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if count != 1 {
		return false, nil
	}
	if err := s.enqueueManagedEndpointTx(ctx, tx, "managed_endpoint_removed", "deleted", endpoint, ""); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

const computeJobColumns = `job_id,environment,tier_type,provider,frame_id,project_id,state,started_at,ended_at,intent_json,hardware_json,origin_tool_use_id,root_frame_id,provider_family,provider_label,external_id,external_url,supports_tail,error_kind,left_on_remote_json,system_hint`

func scanComputeJob(scanner interface{ Scan(...any) error }) (ComputeJob, error) {
	var job ComputeJob
	var ended sql.NullTime
	var intent, hardware, left string
	err := scanner.Scan(&job.JobID, &job.Environment, &job.TierType, &job.Provider, &job.FrameID, &job.ProjectID, &job.State, &job.StartedAt, &ended, &intent, &hardware, &job.OriginToolUseID, &job.RootFrameID, &job.ProviderFamily, &job.ProviderLabel, &job.ExternalID, &job.ExternalURL, &job.SupportsTail, &job.ErrorKind, &left, &job.SystemHint)
	if err != nil {
		return job, err
	}
	job.StartedAtISO = job.StartedAt.UTC().Format(time.RFC3339Nano)
	if ended.Valid {
		value := ended.Time.UTC().Format(time.RFC3339Nano)
		job.EndedAtISO = &value
	}
	_ = json.Unmarshal([]byte(intent), &job.Intent)
	_ = json.Unmarshal([]byte(hardware), &job.HardwareDetails)
	if err := json.Unmarshal([]byte(left), &job.LeftOnRemote); err != nil || job.LeftOnRemote == nil {
		job.LeftOnRemote = []string{}
	}
	return job, nil
}

func (s *Store) ListComputeJobs(userID, projectID string) ([]ComputeJob, error) {
	ctx := context.Background()
	if err := s.ensureComputeWorkbenchSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+computeJobColumns+` FROM compute_workbench_jobs WHERE owner_user_id=? AND project_id=? ORDER BY started_at DESC,job_id`, strings.TrimSpace(userID), strings.TrimSpace(projectID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ComputeJob{}
	for rows.Next() {
		job, err := scanComputeJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

func (s *Store) GetComputeJob(userID, jobID string) (ComputeJob, bool, error) {
	ctx := context.Background()
	if err := s.ensureComputeWorkbenchSchema(ctx); err != nil {
		return ComputeJob{}, false, err
	}
	job, err := scanComputeJob(s.db.QueryRowContext(ctx, `SELECT `+computeJobColumns+` FROM compute_workbench_jobs WHERE owner_user_id=? AND job_id=?`, strings.TrimSpace(userID), strings.TrimSpace(jobID)))
	if errors.Is(err, sql.ErrNoRows) {
		return ComputeJob{}, false, nil
	}
	return job, err == nil, err
}

func (s *Store) AppendComputeJobLog(userID, jobID, stream, text string) error {
	ctx := context.Background()
	if err := s.ensureComputeWorkbenchSchema(ctx); err != nil {
		return err
	}
	column := ""
	switch strings.TrimSpace(stream) {
	case "stdout":
		column = "stdout_text"
	case "stderr":
		column = "stderr_text"
	default:
		return errors.New("compute job log stream must be stdout or stderr")
	}
	userID = strings.TrimSpace(userID)
	jobID = strings.TrimSpace(jobID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "UPDATE compute_workbench_jobs SET "+column+"="+column+"||? WHERE owner_user_id=? AND job_id=?",
		text, userID, jobID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrComputeJobNotFound
	}
	job, err := scanComputeJob(tx.QueryRowContext(ctx, `SELECT `+computeJobColumns+` FROM compute_workbench_jobs WHERE owner_user_id=? AND job_id=?`, userID, jobID))
	if err != nil {
		return err
	}
	if err := s.enqueueComputeJobLogTx(ctx, tx, userID, strings.TrimSpace(stream), text, job); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) GetComputeJobLog(userID, jobID, stream string, tail int) (ComputeJobLog, bool, error) {
	ctx := context.Background()
	if err := s.ensureComputeWorkbenchSchema(ctx); err != nil {
		return ComputeJobLog{}, false, err
	}
	if tail < 0 {
		tail = 0
	}
	if tail > 262144 {
		tail = 262144
	}
	column := "stdout_text"
	if stream == "stderr" {
		column = "stderr_text"
	}
	var text string
	err := s.db.QueryRowContext(ctx, `SELECT `+column+` FROM compute_workbench_jobs WHERE owner_user_id=? AND job_id=?`, strings.TrimSpace(userID), strings.TrimSpace(jobID)).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return ComputeJobLog{}, false, nil
	}
	if err != nil {
		return ComputeJobLog{}, false, err
	}
	data := []byte(text)
	start := 0
	if len(data) > tail {
		start = len(data) - tail
	}
	for start < len(data) && !utf8.RuneStart(data[start]) {
		start++
	}
	return ComputeJobLog{Exists: true, Size: int64(len(data)), Truncated: start > 0, Text: string(data[start:])}, true, nil
}

func (s *Store) ComputeRootOwnedBy(userID, rootID string) (bool, error) {
	var found int
	err := s.db.QueryRowContext(context.Background(), `SELECT 1 FROM frames f JOIN projects p ON p.id=f.project_id WHERE f.id=? AND f.root_frame_id=f.id AND p.user_id=?`, strings.TrimSpace(rootID), strings.TrimSpace(userID)).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) ComputeProjectRoot(userID, projectID string) (string, bool, error) {
	var rootID string
	err := s.db.QueryRowContext(context.Background(), `SELECT f.id FROM frames f JOIN projects p ON p.id=f.project_id WHERE p.user_id=? AND p.id=? AND f.id=f.root_frame_id ORDER BY f.created_at,f.id LIMIT 1`, strings.TrimSpace(userID), strings.TrimSpace(projectID)).Scan(&rootID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return rootID, true, nil
}

func (s *Store) SetSessionComputeProvider(userID, rootID, name string, checked bool) error {
	ctx := context.Background()
	if err := s.ensureComputeWorkbenchSchema(ctx); err != nil {
		return err
	}
	userID = strings.TrimSpace(userID)
	rootID = strings.TrimSpace(rootID)
	name = strings.TrimSpace(name)
	if !validComputeSessionIdentifier(userID, 255) || !validComputeSessionIdentifier(rootID, 255) || !validComputeSessionIdentifier(name, 255) {
		return errors.New("bounded user id, session key, and provider name are required")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO compute_session_enabled(root_frame_id,owner_user_id,provider_name,checked,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(owner_user_id,root_frame_id,provider_name) DO UPDATE SET checked=excluded.checked,updated_at=excluded.updated_at`, rootID, userID, name, checked, s.now().UTC())
	return err
}

func (s *Store) ListSessionComputeProviders(userID, rootID string) ([]string, error) {
	providers, _, err := s.SessionComputeProviderSelection(userID, rootID)
	return providers, err
}

// SessionComputeProviderSelection distinguishes the untouched default from an
// explicit selection containing zero enabled providers.
func (s *Store) SessionComputeProviderSelection(userID, rootID string) ([]string, bool, error) {
	ctx := context.Background()
	if err := s.ensureComputeWorkbenchSchema(ctx); err != nil {
		return nil, false, err
	}
	userID = strings.TrimSpace(userID)
	rootID = strings.TrimSpace(rootID)
	if !validComputeSessionIdentifier(userID, 255) || !validComputeSessionIdentifier(rootID, 255) {
		return nil, false, errors.New("bounded user id and session key are required")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT provider_name,checked FROM compute_session_enabled WHERE owner_user_id=? AND root_frame_id=? ORDER BY provider_name`, userID, rootID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []string{}
	configured := false
	for rows.Next() {
		var name string
		var checked bool
		if err := rows.Scan(&name, &checked); err != nil {
			return nil, false, err
		}
		configured = true
		if checked {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, configured, rows.Err()
}

func (s *Store) MigrateSessionComputeProviders(userID, fromKey, toKey string) error {
	ctx := context.Background()
	if err := s.ensureComputeWorkbenchSchema(ctx); err != nil {
		return err
	}
	userID = strings.TrimSpace(userID)
	fromKey = strings.TrimSpace(fromKey)
	toKey = strings.TrimSpace(toKey)
	if !validComputeSessionIdentifier(userID, 255) || !validComputeSessionIdentifier(fromKey, 255) || !strings.HasPrefix(fromKey, "draft-") || !validComputeSessionIdentifier(toKey, 255) {
		return errors.New("user id, draft fromKey, and bounded toKey are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT provider_name,checked FROM compute_session_enabled WHERE owner_user_id=? AND root_frame_id=? ORDER BY provider_name`, userID, fromKey)
	if err != nil {
		return err
	}
	type selection struct {
		name    string
		checked bool
	}
	var providers []selection
	for rows.Next() {
		var name string
		var checked bool
		if err := rows.Scan(&name, &checked); err != nil {
			rows.Close()
			return err
		}
		providers = append(providers, selection{name: name, checked: checked})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(providers) == 0 {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM compute_session_enabled WHERE owner_user_id=? AND root_frame_id=?`, userID, toKey); err != nil {
		return err
	}
	for _, provider := range providers {
		if _, err := tx.ExecContext(ctx, `INSERT INTO compute_session_enabled(root_frame_id,owner_user_id,provider_name,checked,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(owner_user_id,root_frame_id,provider_name) DO UPDATE SET checked=excluded.checked,updated_at=excluded.updated_at`, toKey, userID, provider.name, provider.checked, s.now().UTC()); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM compute_session_enabled WHERE owner_user_id=? AND root_frame_id=?`, userID, fromKey); err != nil {
		return err
	}
	return tx.Commit()
}

func validComputeSessionIdentifier(value string, maxRunes int) bool {
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > maxRunes {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func nullString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}
