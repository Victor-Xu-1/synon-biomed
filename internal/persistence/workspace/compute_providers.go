package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const computeProviderColumns = `
	name, owner_user_id, family, endpoint, skill_name, credential_name, hosted,
	environments, data_roots, ssh_overrides, max_concurrent_jobs, max_timeout_sec,
	memory_md, scratch_root, scheduler, details_md, details_rev,
	probed_at, updated_at`

type computeProviderRowScanner interface {
	Scan(dest ...any) error
}

// CreateInferenceProvider registers an inference endpoint and resets stale probe
// metadata when the same owner intentionally replaces an existing definition.
func (s *Store) CreateInferenceProvider(input ComputeProviderInput) (ComputeProvider, error) {
	if s == nil || s.db == nil {
		return ComputeProvider{}, errors.New("workspace store is closed")
	}
	input.Name = strings.TrimSpace(input.Name)
	input.UserID = strings.TrimSpace(input.UserID)
	input.Family = strings.TrimSpace(input.Family)
	input.Endpoint = strings.TrimSpace(input.Endpoint)
	input.SkillName = strings.TrimSpace(input.SkillName)
	input.CredentialName = strings.TrimSpace(input.CredentialName)
	if input.Name == "" || input.UserID == "" || input.Family == "" || input.Endpoint == "" || input.SkillName == "" {
		return ComputeProvider{}, errors.New("compute provider name, user id, family, endpoint, and skill name are required")
	}
	environments, err := json.Marshal(input.Environments)
	if err != nil {
		return ComputeProvider{}, fmt.Errorf("marshal compute environments: %w", err)
	}
	now := s.now().UTC()
	dataRoots, err := json.Marshal(input.DataRoots)
	if err != nil {
		return ComputeProvider{}, fmt.Errorf("marshal compute data roots: %w", err)
	}
	sshOverrides, err := json.Marshal(input.SSHOverrides)
	if err != nil {
		return ComputeProvider{}, fmt.Errorf("marshal SSH overrides: %w", err)
	}
	row := s.db.QueryRowContext(context.Background(), `
		INSERT INTO compute_providers (
			name, owner_user_id, family, endpoint, skill_name, credential_name, hosted,
			environments, data_roots, ssh_overrides, max_concurrent_jobs, max_timeout_sec,
			memory_md, scratch_root, scheduler, details_md, details_rev,
			probed_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', 0, NULL, ?)
		ON CONFLICT(name) DO UPDATE SET
			family = excluded.family,
			endpoint = excluded.endpoint,
			skill_name = excluded.skill_name,
			credential_name = excluded.credential_name,
			hosted = excluded.hosted,
			environments = excluded.environments,
			memory_md = excluded.memory_md,
			scratch_root = excluded.scratch_root,
			data_roots = excluded.data_roots,
			ssh_overrides = excluded.ssh_overrides,
			max_concurrent_jobs = excluded.max_concurrent_jobs,
			max_timeout_sec = excluded.max_timeout_sec,
			scheduler = excluded.scheduler,
			details_md = '',
			details_rev = 0,
			probed_at = NULL,
			updated_at = excluded.updated_at
		WHERE compute_providers.owner_user_id = excluded.owner_user_id
		RETURNING `+computeProviderColumns,
		input.Name, input.UserID, input.Family, input.Endpoint, input.SkillName,
		input.CredentialName, input.Hosted, string(environments), string(dataRoots), string(sshOverrides),
		input.MaxConcurrentJobs, input.MaxTimeoutSec, input.MemoryMD,
		input.ScratchRoot, input.Scheduler, now)
	provider, err := scanComputeProvider(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ComputeProvider{}, fmt.Errorf("compute provider %q belongs to another user", input.Name)
	}
	if err != nil {
		return ComputeProvider{}, fmt.Errorf("create inference provider: %w", err)
	}
	return provider, nil
}

func (s *Store) UpsertSSHProvider(input ComputeProviderInput) (ComputeProvider, error) {
	if s == nil || s.db == nil {
		return ComputeProvider{}, errors.New("workspace store is closed")
	}
	input.Name = strings.TrimSpace(input.Name)
	input.UserID = strings.TrimSpace(input.UserID)
	input.Scheduler = strings.ToLower(strings.TrimSpace(input.Scheduler))
	if input.Name == "" || input.UserID == "" || !strings.HasPrefix(input.Name, "ssh:") {
		return ComputeProvider{}, errors.New("SSH provider name and user id are required")
	}
	if input.Scheduler != "" && input.Scheduler != "none" && input.Scheduler != "slurm" {
		return ComputeProvider{}, errors.New("SSH provider scheduler must be none or slurm")
	}
	dataRoots, err := json.Marshal(input.DataRoots)
	if err != nil {
		return ComputeProvider{}, fmt.Errorf("marshal compute data roots: %w", err)
	}
	sshOverrides, err := json.Marshal(input.SSHOverrides)
	if err != nil {
		return ComputeProvider{}, fmt.Errorf("marshal SSH overrides: %w", err)
	}
	now := s.now().UTC()
	provider, err := scanComputeProvider(s.db.QueryRowContext(context.Background(), `
		INSERT INTO compute_providers (
			name, owner_user_id, family, endpoint, skill_name, credential_name, hosted,
			environments, data_roots, ssh_overrides, max_concurrent_jobs, max_timeout_sec,
			memory_md, scratch_root, scheduler, details_md, details_rev, probed_at, updated_at
		) VALUES (?, ?, 'ssh', '', '', '', 0, '[]', ?, ?, ?, ?, ?, ?, ?, ?, 0, NULL, ?)
		ON CONFLICT(name) DO UPDATE SET
			data_roots=CASE WHEN excluded.data_roots='[]' THEN compute_providers.data_roots ELSE excluded.data_roots END,
			ssh_overrides=CASE WHEN excluded.ssh_overrides='{}' THEN compute_providers.ssh_overrides ELSE excluded.ssh_overrides END,
			max_concurrent_jobs=COALESCE(excluded.max_concurrent_jobs, compute_providers.max_concurrent_jobs),
			max_timeout_sec=COALESCE(excluded.max_timeout_sec, compute_providers.max_timeout_sec),
			memory_md=CASE WHEN excluded.memory_md='' THEN compute_providers.memory_md ELSE excluded.memory_md END,
			scratch_root=CASE WHEN excluded.scratch_root='' THEN compute_providers.scratch_root ELSE excluded.scratch_root END,
			scheduler=CASE WHEN excluded.scheduler='' THEN compute_providers.scheduler ELSE excluded.scheduler END,
			details_md=CASE WHEN excluded.details_md='' THEN compute_providers.details_md
				WHEN compute_providers.details_md='' THEN excluded.details_md
				ELSE compute_providers.details_md || char(10) || char(10) || excluded.details_md END,
			updated_at=excluded.updated_at
		WHERE compute_providers.owner_user_id=excluded.owner_user_id AND compute_providers.family='ssh'
		RETURNING `+computeProviderColumns,
		input.Name, input.UserID, string(dataRoots), string(sshOverrides), input.MaxConcurrentJobs, input.MaxTimeoutSec,
		input.MemoryMD, input.ScratchRoot, input.Scheduler, input.DetailsMD, now))
	if errors.Is(err, sql.ErrNoRows) {
		return ComputeProvider{}, fmt.Errorf("compute provider %q belongs to another user or family", input.Name)
	}
	if err != nil {
		return ComputeProvider{}, fmt.Errorf("upsert SSH provider: %w", err)
	}
	return provider, nil
}

func (s *Store) UpdateComputeProviderSettings(name, userID, detailsMD string, maxConcurrentJobs, maxTimeoutSec *int) (ComputeProvider, error) {
	if s == nil || s.db == nil {
		return ComputeProvider{}, errors.New("workspace store is closed")
	}
	provider, err := scanComputeProvider(s.db.QueryRowContext(context.Background(), `
		UPDATE compute_providers SET details_md=?, max_concurrent_jobs=?, max_timeout_sec=?, details_rev=details_rev+1, updated_at=?
		WHERE name=? AND owner_user_id=? RETURNING `+computeProviderColumns,
		detailsMD, maxConcurrentJobs, maxTimeoutSec, s.now().UTC(), strings.TrimSpace(name), strings.TrimSpace(userID)))
	if errors.Is(err, sql.ErrNoRows) {
		return ComputeProvider{}, sql.ErrNoRows
	}
	if err != nil {
		return ComputeProvider{}, fmt.Errorf("update compute provider settings: %w", err)
	}
	return provider, nil
}

func (s *Store) SetComputeProviderDataRoots(name, userID string, roots []string) (ComputeProvider, error) {
	raw, err := json.Marshal(roots)
	if err != nil {
		return ComputeProvider{}, fmt.Errorf("marshal compute data roots: %w", err)
	}
	provider, err := scanComputeProvider(s.db.QueryRowContext(context.Background(), `
		UPDATE compute_providers SET data_roots=?, updated_at=?
		WHERE name=? AND owner_user_id=? RETURNING `+computeProviderColumns,
		string(raw), s.now().UTC(), strings.TrimSpace(name), strings.TrimSpace(userID)))
	if errors.Is(err, sql.ErrNoRows) {
		return ComputeProvider{}, sql.ErrNoRows
	}
	if err != nil {
		return ComputeProvider{}, fmt.Errorf("set compute provider data roots: %w", err)
	}
	return provider, nil
}

func (s *Store) SetComputeProviderScratchRoot(name, userID string, scratchRoot *string) (ComputeProvider, error) {
	var value any
	if scratchRoot != nil {
		value = strings.TrimSpace(*scratchRoot)
	}
	provider, err := scanComputeProvider(s.db.QueryRowContext(context.Background(), `
		UPDATE compute_providers SET scratch_root=COALESCE(?, ''), updated_at=?
		WHERE name=? AND owner_user_id=? AND family='ssh' RETURNING `+computeProviderColumns,
		value, s.now().UTC(), strings.TrimSpace(name), strings.TrimSpace(userID)))
	if errors.Is(err, sql.ErrNoRows) {
		return ComputeProvider{}, sql.ErrNoRows
	}
	if err != nil {
		return ComputeProvider{}, fmt.Errorf("set compute provider scratch root: %w", err)
	}
	return provider, nil
}

// GetComputeProvider returns a provider by name. Supplying userID applies the
// caller-visible scope: a provider must be owned by that user or be global.
// The unscoped form is retained for internal migration and inspection callers.
func (s *Store) GetComputeProvider(name string, userID ...string) (ComputeProvider, bool, error) {
	if s == nil || s.db == nil {
		return ComputeProvider{}, false, errors.New("workspace store is closed")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ComputeProvider{}, false, errors.New("compute provider name is required")
	}
	if len(userID) > 1 {
		return ComputeProvider{}, false, errors.New("at most one compute provider user id may be supplied")
	}
	query := `SELECT ` + computeProviderColumns + ` FROM compute_providers WHERE name = ?`
	args := []any{name}
	if len(userID) == 1 {
		owner := strings.TrimSpace(userID[0])
		if owner == "" {
			return ComputeProvider{}, false, errors.New("compute provider user id is required")
		}
		query += ` AND owner_user_id IN (?, '*')`
		args = append(args, owner)
	}
	provider, err := scanComputeProvider(s.db.QueryRowContext(context.Background(), query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return ComputeProvider{}, false, nil
	}
	if err != nil {
		return ComputeProvider{}, false, fmt.Errorf("get compute provider: %w", err)
	}
	return provider, true, nil
}

func (s *Store) ListComputeProviders(userID string) ([]ComputeProvider, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, errors.New("compute provider user id is required")
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT `+computeProviderColumns+` FROM compute_providers
		WHERE owner_user_id IN (?, '*') ORDER BY name`, userID)
	if err != nil {
		return nil, fmt.Errorf("list compute providers: %w", err)
	}
	defer rows.Close()
	providers := make([]ComputeProvider, 0)
	for rows.Next() {
		provider, err := scanComputeProvider(rows)
		if err != nil {
			return nil, fmt.Errorf("scan compute provider: %w", err)
		}
		providers = append(providers, provider)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate compute providers: %w", err)
	}
	return providers, nil
}

// RecordComputeProbe advances the revision in the same SQL statement that
// stores probe details. Concurrent probes cannot observe or write a stale rev.
func (s *Store) RecordComputeProbe(name, userID, details string, probedAt *time.Time) (ComputeProvider, error) {
	if s == nil || s.db == nil {
		return ComputeProvider{}, errors.New("workspace store is closed")
	}
	name = strings.TrimSpace(name)
	userID = strings.TrimSpace(userID)
	if name == "" || userID == "" {
		return ComputeProvider{}, errors.New("compute provider name and user id are required")
	}
	var normalizedProbedAt *time.Time
	if probedAt != nil {
		value := probedAt.UTC()
		normalizedProbedAt = &value
	}
	provider, err := scanComputeProvider(s.db.QueryRowContext(context.Background(), `
		UPDATE compute_providers SET
			details_md = ?,
			details_rev = details_rev + 1,
			probed_at = COALESCE(?, probed_at),
			updated_at = ?
		WHERE name = ? AND owner_user_id = ?
		RETURNING `+computeProviderColumns,
		details, normalizedProbedAt, s.now().UTC(), name, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return ComputeProvider{}, fmt.Errorf("compute provider %q not found: %w", name, sql.ErrNoRows)
	}
	if err != nil {
		return ComputeProvider{}, fmt.Errorf("record compute probe: %w", err)
	}
	return provider, nil
}

func (s *Store) DeleteComputeProvider(name, userID string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("workspace store is closed")
	}
	result, err := s.db.ExecContext(context.Background(), `
		DELETE FROM compute_providers WHERE name = ? AND owner_user_id = ?`, strings.TrimSpace(name), strings.TrimSpace(userID))
	if err != nil {
		return false, fmt.Errorf("delete compute provider: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("inspect deleted compute provider: %w", err)
	}
	return count > 0, nil
}

func scanComputeProvider(scanner computeProviderRowScanner) (ComputeProvider, error) {
	var provider ComputeProvider
	var environments, dataRoots, sshOverrides string
	var maxConcurrentJobs, maxTimeoutSec sql.NullInt64
	if err := scanner.Scan(
		&provider.Name, &provider.UserID, &provider.Family, &provider.Endpoint,
		&provider.SkillName, &provider.CredentialName, &provider.Hosted,
		&environments, &dataRoots, &sshOverrides, &maxConcurrentJobs, &maxTimeoutSec,
		&provider.MemoryMD, &provider.ScratchRoot, &provider.Scheduler,
		&provider.DetailsMD, &provider.DetailsRev, &provider.ProbedAt, &provider.UpdatedAt,
	); err != nil {
		return ComputeProvider{}, err
	}
	if err := json.Unmarshal([]byte(environments), &provider.Environments); err != nil {
		return ComputeProvider{}, fmt.Errorf("decode compute environments: %w", err)
	}
	if provider.Environments == nil {
		provider.Environments = []string{}
	}
	if err := json.Unmarshal([]byte(dataRoots), &provider.DataRoots); err != nil {
		return ComputeProvider{}, fmt.Errorf("decode compute data roots: %w", err)
	}
	if provider.DataRoots == nil {
		provider.DataRoots = []string{}
	}
	if err := json.Unmarshal([]byte(sshOverrides), &provider.SSHOverrides); err != nil {
		return ComputeProvider{}, fmt.Errorf("decode SSH overrides: %w", err)
	}
	if maxConcurrentJobs.Valid {
		value := int(maxConcurrentJobs.Int64)
		provider.MaxConcurrentJobs = &value
	}
	if maxTimeoutSec.Valid {
		value := int(maxTimeoutSec.Int64)
		provider.MaxTimeoutSec = &value
	}
	return provider, nil
}
