package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	ComputeJobPending    = "pending"
	ComputeJobStaging    = "staging"
	ComputeJobQueued     = "queued"
	ComputeJobRunning    = "running"
	ComputeJobHarvesting = "harvesting"
	ComputeJobDone       = "done"
	ComputeJobFailed     = "failed"
	ComputeJobTimedOut   = "timed_out"
	ComputeJobOrphaned   = "orphaned"
)

var ErrComputeJobNotFound = errors.New("compute job not found")
var ErrComputeJobTransition = errors.New("invalid compute job state transition")
var ErrComputeExternalIDConflict = errors.New("compute external id is already bound")
var ErrComputeProviderConcurrencyFull = errors.New("compute provider concurrency limit is full")

type ComputeReconcileLease struct {
	OwnerUserID string    `json:"ownerUserId"`
	Provider    string    `json:"provider"`
	Holder      string    `json:"holder"`
	ExpiresAt   time.Time `json:"expiresAt"`
	HeartbeatAt time.Time `json:"heartbeatAt"`
}

type OwnedComputeJob struct {
	OwnerUserID string     `json:"ownerUserId"`
	Job         ComputeJob `json:"job"`
}

func (s *Store) ListActiveBYOCJobs(limit int) ([]OwnedComputeJob, error) {
	return s.listActiveComputeJobs("byoc", limit)
}

func (s *Store) ListActiveSSHJobs(limit int) ([]OwnedComputeJob, error) {
	return s.listActiveComputeJobs("ssh", limit)
}

func (s *Store) CountActiveComputeJobs(userID, provider string) (int, error) {
	ctx := context.Background()
	if err := s.ensureComputeWorkbenchSchema(ctx); err != nil {
		return 0, err
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM compute_workbench_jobs
		WHERE owner_user_id=? AND provider=? AND state IN (?,?,?,?,?)`,
		strings.TrimSpace(userID), strings.TrimSpace(provider), ComputeJobPending, ComputeJobStaging,
		ComputeJobQueued, ComputeJobRunning, ComputeJobHarvesting).Scan(&count)
	return count, err
}

func (s *Store) listActiveComputeJobs(family string, limit int) ([]OwnedComputeJob, error) {
	ctx := context.Background()
	if err := s.ensureComputeWorkbenchSchema(ctx); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT owner_user_id,`+computeJobColumns+` FROM compute_workbench_jobs WHERE provider_family=? AND state IN (?,?,?,?) ORDER BY started_at,job_id LIMIT ?`, strings.TrimSpace(family), ComputeJobPending, ComputeJobStaging, ComputeJobRunning, ComputeJobHarvesting, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]OwnedComputeJob, 0)
	for rows.Next() {
		var owner string
		var job ComputeJob
		var ended sql.NullTime
		var intent, hardware, left string
		if err := rows.Scan(
			&owner, &job.JobID, &job.Environment, &job.TierType, &job.Provider, &job.FrameID, &job.ProjectID,
			&job.State, &job.StartedAt, &ended, &intent, &hardware, &job.OriginToolUseID, &job.RootFrameID,
			&job.ProviderFamily, &job.ProviderLabel, &job.ExternalID, &job.ExternalURL, &job.SupportsTail,
			&job.ErrorKind, &left, &job.SystemHint,
		); err != nil {
			return nil, err
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
		result = append(result, OwnedComputeJob{OwnerUserID: owner, Job: job})
		if len(result) > limit {
			return nil, errors.New("active compute job inventory exceeds the bounded supervisor limit")
		}
	}
	return result, rows.Err()
}

func (s *Store) CreateComputeJob(userID string, job ComputeJob) (ComputeJob, error) {
	ctx := context.Background()
	if err := s.ensureComputeWorkbenchSchema(ctx); err != nil {
		return ComputeJob{}, err
	}
	userID = strings.TrimSpace(userID)
	job.JobID = strings.TrimSpace(job.JobID)
	job.ProjectID = strings.TrimSpace(job.ProjectID)
	job.Provider = strings.TrimSpace(job.Provider)
	job.Environment = strings.TrimSpace(job.Environment)
	job.TierType = strings.TrimSpace(job.TierType)
	if userID == "" || job.JobID == "" || job.ProjectID == "" || job.Provider == "" ||
		job.Environment == "" || job.TierType == "" {
		return ComputeJob{}, errors.New("compute job identity, owner, project, provider, environment, and tier are required")
	}
	if job.State == "" {
		job.State = ComputeJobPending
	}
	if job.State != ComputeJobPending {
		return ComputeJob{}, ErrComputeJobTransition
	}
	if job.StartedAt.IsZero() {
		job.StartedAt = s.now().UTC()
	} else {
		job.StartedAt = job.StartedAt.UTC()
	}
	intent, err := json.Marshal(job.Intent)
	if err != nil {
		return ComputeJob{}, err
	}
	hardware, err := json.Marshal(job.HardwareDetails)
	if err != nil {
		return ComputeJob{}, err
	}
	left, err := json.Marshal(job.LeftOnRemote)
	if err != nil {
		return ComputeJob{}, err
	}
	if job.ProviderFamily == "" {
		job.ProviderFamily = strings.SplitN(job.Provider, ":", 2)[0]
	}
	if job.ProviderLabel == "" {
		job.ProviderLabel = job.Provider
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ComputeJob{}, fmt.Errorf("begin create compute job: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO compute_workbench_jobs(
			job_id,owner_user_id,project_id,frame_id,environment,tier_type,provider,state,
			started_at,intent_json,hardware_json,origin_tool_use_id,root_frame_id,
			provider_family,provider_label,supports_tail,left_on_remote_json,stdout_text,stderr_text
		)
		SELECT ?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'',''
		FROM projects project JOIN compute_providers provider
			ON provider.name=? AND provider.owner_user_id IN (?,'*')
		WHERE project.id=? AND project.user_id=?
			AND (provider.max_concurrent_jobs IS NULL OR (
				SELECT COUNT(*) FROM compute_workbench_jobs active
				WHERE active.owner_user_id=? AND active.provider=provider.name
					AND active.state IN (?,?,?,?,?)
			) < provider.max_concurrent_jobs)`,
		job.JobID, userID, job.ProjectID, job.FrameID, job.Environment, job.TierType,
		job.Provider, job.State, job.StartedAt, string(intent), string(hardware),
		job.OriginToolUseID, job.RootFrameID, job.ProviderFamily, job.ProviderLabel,
		job.SupportsTail, string(left), job.Provider, userID, job.ProjectID, userID,
		userID, ComputeJobPending, ComputeJobStaging, ComputeJobQueued, ComputeJobRunning, ComputeJobHarvesting)
	if err != nil {
		return ComputeJob{}, fmt.Errorf("create compute job: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		var limit sql.NullInt64
		var active int
		if capErr := tx.QueryRowContext(ctx, `SELECT provider.max_concurrent_jobs,
			(SELECT COUNT(*) FROM compute_workbench_jobs active WHERE active.owner_user_id=? AND active.provider=provider.name AND active.state IN (?,?,?,?,?))
			FROM compute_providers provider WHERE provider.name=? AND provider.owner_user_id IN (?,'*')`,
			userID, ComputeJobPending, ComputeJobStaging, ComputeJobQueued, ComputeJobRunning, ComputeJobHarvesting,
			job.Provider, userID).Scan(&limit, &active); capErr == nil && limit.Valid && active >= int(limit.Int64) {
			return ComputeJob{}, ErrComputeProviderConcurrencyFull
		}
		return ComputeJob{}, errors.New("compute job project or provider is not owned by user")
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO compute_usage(
			id,job_id,environment,tier_type,provider,frame_id,project_id,state,started_at,
			intent,hardware_details,root_frame_id,origin_tool_use_id
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		uuid.NewString(), job.JobID, job.Environment, job.TierType, job.Provider,
		job.FrameID, job.ProjectID, ComputeStatePending, job.StartedAt, string(intent),
		string(hardware), job.RootFrameID, job.OriginToolUseID); err != nil {
		return ComputeJob{}, fmt.Errorf("create authoritative compute usage: %w", err)
	}
	created, err := scanComputeJob(tx.QueryRowContext(ctx, `SELECT `+computeJobColumns+` FROM compute_workbench_jobs WHERE owner_user_id=? AND job_id=?`, userID, job.JobID))
	if err != nil {
		return ComputeJob{}, err
	}
	if err := s.enqueueComputeJobUpdateTx(ctx, tx, userID, created); err != nil {
		return ComputeJob{}, fmt.Errorf("enqueue compute job creation event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ComputeJob{}, fmt.Errorf("commit create compute job: %w", err)
	}
	return created, nil
}

func (s *Store) BindComputeJobExternal(userID, jobID, externalID, externalURL string) (ComputeJob, error) {
	return s.bindComputeJobExternal(userID, jobID, externalID, externalURL, ComputeJobRunning)
}

func (s *Store) BindComputeJobExternalForStaging(userID, jobID, externalID, externalURL string) (ComputeJob, error) {
	return s.bindComputeJobExternal(userID, jobID, externalID, externalURL, ComputeJobStaging)
}

func (s *Store) bindComputeJobExternal(userID, jobID, externalID, externalURL, nextState string) (ComputeJob, error) {
	ctx := context.Background()
	if err := s.ensureComputeWorkbenchSchema(ctx); err != nil {
		return ComputeJob{}, err
	}
	userID = strings.TrimSpace(userID)
	jobID = strings.TrimSpace(jobID)
	externalID = strings.TrimSpace(externalID)
	if userID == "" || jobID == "" || externalID == "" || (nextState != ComputeJobRunning && nextState != ComputeJobStaging) {
		return ComputeJob{}, errors.New("compute job owner, id, and external id are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ComputeJob{}, fmt.Errorf("begin bind compute job: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		UPDATE compute_workbench_jobs SET state=?,external_id=?,external_url=?
		WHERE owner_user_id=? AND job_id=? AND state=? AND external_id IS NULL`,
		nextState, externalID, nullableComputeString(externalURL), userID, jobID, ComputeJobPending)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
			return ComputeJob{}, ErrComputeExternalIDConflict
		}
		return ComputeJob{}, err
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		var exists int
		getErr := tx.QueryRowContext(ctx,
			`SELECT 1 FROM compute_workbench_jobs WHERE owner_user_id=? AND job_id=?`,
			userID, jobID).Scan(&exists)
		if errors.Is(getErr, sql.ErrNoRows) {
			return ComputeJob{}, ErrComputeJobNotFound
		}
		if getErr != nil {
			return ComputeJob{}, getErr
		}
		return ComputeJob{}, ErrComputeJobTransition
	}
	remoteHandle, err := json.Marshal(map[string]string{
		"sandboxId":   externalID,
		"externalUrl": strings.TrimSpace(externalURL),
	})
	if err != nil {
		return ComputeJob{}, err
	}
	usageResult, err := tx.ExecContext(ctx, `
		UPDATE compute_usage SET state=?,remote_handle=? WHERE job_id=? AND state=?`,
		nextState, string(remoteHandle), jobID, ComputeStatePending)
	if err != nil {
		return ComputeJob{}, fmt.Errorf("bind authoritative compute usage: %w", err)
	}
	if usageRows, _ := usageResult.RowsAffected(); usageRows != 1 {
		return ComputeJob{}, errors.New("authoritative compute usage is missing or changed")
	}
	job, err := scanComputeJob(tx.QueryRowContext(ctx, `SELECT `+computeJobColumns+` FROM compute_workbench_jobs WHERE owner_user_id=? AND job_id=?`, userID, jobID))
	if err != nil {
		return ComputeJob{}, err
	}
	if err := s.enqueueComputeJobUpdateTx(ctx, tx, userID, job); err != nil {
		return ComputeJob{}, fmt.Errorf("enqueue compute job binding event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ComputeJob{}, fmt.Errorf("commit bind compute job: %w", err)
	}
	return job, nil
}

func (s *Store) TransitionComputeJob(userID, jobID, nextState, errorKind string, at time.Time) (ComputeJob, error) {
	updated, _, _, err := s.transitionComputeJob(context.Background(), userID, jobID, nextState, errorKind, at, nil)
	return updated, err
}

// TransitionComputeJobWithNotification commits a terminal compute state, its
// realtime projection, and the durable completion notification together. This
// prevents observers from seeing terminal work before wait_for_notification can
// consume its result.
func (s *Store) TransitionComputeJobWithNotification(
	ctx context.Context,
	userID, jobID, nextState, errorKind string,
	at time.Time,
	input CreateNotificationInput,
) (ComputeJob, Notification, FrameEvent, error) {
	if !isTerminalComputeJobState(strings.TrimSpace(nextState)) {
		return ComputeJob{}, Notification{}, FrameEvent{}, ErrComputeJobTransition
	}
	return s.transitionComputeJob(ctx, userID, jobID, nextState, errorKind, at, &input)
}

func (s *Store) transitionComputeJob(
	ctx context.Context,
	userID, jobID, nextState, errorKind string,
	at time.Time,
	notificationInput *CreateNotificationInput,
) (ComputeJob, Notification, FrameEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.ensureComputeWorkbenchSchema(ctx); err != nil {
		return ComputeJob{}, Notification{}, FrameEvent{}, err
	}
	userID = strings.TrimSpace(userID)
	jobID = strings.TrimSpace(jobID)
	nextState = strings.TrimSpace(nextState)
	errorKind = strings.TrimSpace(errorKind)
	if at.IsZero() {
		at = s.now().UTC()
	} else {
		at = at.UTC()
	}
	var updated ComputeJob
	var notification Notification
	var event FrameEvent
	err := transcriptstore.NewRepository(s.db).RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		job, err := scanComputeJob(tx.QueryRowContext(ctx, `SELECT `+computeJobColumns+` FROM compute_workbench_jobs WHERE owner_user_id=? AND job_id=?`, userID, jobID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrComputeJobNotFound
		}
		if err != nil {
			return err
		}
		if !canTransitionComputeJob(job.State, nextState) {
			return ErrComputeJobTransition
		}
		var endedAt any
		if isTerminalComputeJobState(nextState) {
			endedAt = at
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE compute_workbench_jobs SET state=?,ended_at=?,error_kind=?
			WHERE owner_user_id=? AND job_id=? AND state=?`,
			nextState, endedAt, nullableComputeString(errorKind), userID, jobID, job.State)
		if err != nil {
			return err
		}
		if rows, _ := result.RowsAffected(); rows != 1 {
			return ErrComputeJobTransition
		}
		var resultJSON any
		if errorKind != "" {
			encoded, err := json.Marshal(map[string]string{"error_kind": errorKind})
			if err != nil {
				return err
			}
			resultJSON = string(encoded)
		}
		usageResult, err := tx.ExecContext(ctx, `
			UPDATE compute_usage SET state=?,ended_at=?,result=COALESCE(?,result)
			WHERE job_id=? AND state=?`,
			nextState, endedAt, resultJSON, jobID, job.State)
		if err != nil {
			return fmt.Errorf("transition authoritative compute usage: %w", err)
		}
		if usageRows, _ := usageResult.RowsAffected(); usageRows != 1 {
			return errors.New("authoritative compute usage is missing or changed")
		}
		updated, err = scanComputeJob(tx.QueryRowContext(ctx, `SELECT `+computeJobColumns+` FROM compute_workbench_jobs WHERE owner_user_id=? AND job_id=?`, userID, jobID))
		if err != nil {
			return err
		}
		if err := s.enqueueComputeJobUpdateTx(ctx, tx, userID, updated); err != nil {
			return fmt.Errorf("enqueue compute job transition event: %w", err)
		}
		if notificationInput != nil {
			notification, event, err = createNotificationTx(ctx, tx, *notificationInput, at)
			if err != nil {
				return fmt.Errorf("create compute completion notification: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return ComputeJob{}, Notification{}, FrameEvent{}, err
	}
	return updated, notification, event, nil
}

func (s *Store) SetComputeJobResult(userID, jobID string, result map[string]any) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is unavailable")
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > 1<<20 {
		return errors.New("compute job result exceeds the bounded contract")
	}
	update, err := s.db.Exec(`UPDATE compute_usage SET result=? WHERE job_id=? AND EXISTS(SELECT 1 FROM compute_workbench_jobs WHERE owner_user_id=? AND job_id=?)`, string(encoded), strings.TrimSpace(jobID), strings.TrimSpace(userID), strings.TrimSpace(jobID))
	if err != nil {
		return err
	}
	if rows, _ := update.RowsAffected(); rows != 1 {
		return ErrComputeJobNotFound
	}
	return nil
}

func (s *Store) GetComputeJobResult(userID, jobID string) (map[string]any, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, errors.New("workspace store is unavailable")
	}
	var encoded sql.NullString
	err := s.db.QueryRow(`SELECT usage.result FROM compute_usage usage JOIN compute_workbench_jobs job ON job.job_id=usage.job_id WHERE job.owner_user_id=? AND job.job_id=?`, strings.TrimSpace(userID), strings.TrimSpace(jobID)).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !encoded.Valid {
		return map[string]any{}, err == nil, nil
	}
	if err != nil {
		return nil, false, err
	}
	result := map[string]any{}
	if err := json.Unmarshal([]byte(encoded.String), &result); err != nil {
		return nil, true, errors.New("compute job result is invalid")
	}
	return result, true, nil
}

func (s *Store) ListActiveComputeJobs(userID, provider string) ([]ComputeJob, error) {
	ctx := context.Background()
	if err := s.ensureComputeWorkbenchSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+computeJobColumns+` FROM compute_workbench_jobs
		WHERE owner_user_id=? AND provider=? AND state IN (?,?,?,?) AND external_id IS NOT NULL
		ORDER BY started_at,job_id`,
		strings.TrimSpace(userID), strings.TrimSpace(provider), ComputeJobStaging, ComputeJobQueued, ComputeJobRunning, ComputeJobHarvesting)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []ComputeJob{}
	for rows.Next() {
		job, err := scanComputeJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *Store) AcquireComputeReconcileLease(userID, provider, holder string, ttl time.Duration) (ComputeReconcileLease, bool, error) {
	if err := validateComputeLeaseInput(userID, provider, holder, ttl); err != nil {
		return ComputeReconcileLease{}, false, err
	}
	ctx := context.Background()
	if err := s.ensureComputeWorkbenchSchema(ctx); err != nil {
		return ComputeReconcileLease{}, false, err
	}
	now := s.now().UTC()
	expiresAt := now.Add(ttl)
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO compute_reconcile_leases(owner_user_id,provider,holder,expires_at,heartbeat_at)
		VALUES(?,?,?,?,?)
		ON CONFLICT(owner_user_id,provider) DO UPDATE SET
			holder=excluded.holder,expires_at=excluded.expires_at,heartbeat_at=excluded.heartbeat_at
		WHERE compute_reconcile_leases.expires_at<=? OR compute_reconcile_leases.holder=excluded.holder`,
		strings.TrimSpace(userID), strings.TrimSpace(provider), strings.TrimSpace(holder), expiresAt, now, now)
	if err != nil {
		return ComputeReconcileLease{}, false, err
	}
	rows, _ := result.RowsAffected()
	lease, err := s.getComputeReconcileLease(ctx, userID, provider)
	return lease, rows == 1 && lease.Holder == strings.TrimSpace(holder), err
}

func (s *Store) HeartbeatComputeReconcileLease(userID, provider, holder string, ttl time.Duration) (ComputeReconcileLease, bool, error) {
	if err := validateComputeLeaseInput(userID, provider, holder, ttl); err != nil {
		return ComputeReconcileLease{}, false, err
	}
	ctx := context.Background()
	if err := s.ensureComputeWorkbenchSchema(ctx); err != nil {
		return ComputeReconcileLease{}, false, err
	}
	now := s.now().UTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE compute_reconcile_leases SET expires_at=?,heartbeat_at=?
		WHERE owner_user_id=? AND provider=? AND holder=? AND expires_at>?`,
		now.Add(ttl), now, strings.TrimSpace(userID), strings.TrimSpace(provider), strings.TrimSpace(holder), now)
	if err != nil {
		return ComputeReconcileLease{}, false, err
	}
	rows, _ := result.RowsAffected()
	lease, getErr := s.getComputeReconcileLease(ctx, userID, provider)
	return lease, rows == 1, getErr
}

func (s *Store) ReleaseComputeReconcileLease(userID, provider, holder string) (bool, error) {
	ctx := context.Background()
	if err := s.ensureComputeWorkbenchSchema(ctx); err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM compute_reconcile_leases WHERE owner_user_id=? AND provider=? AND holder=?`,
		strings.TrimSpace(userID), strings.TrimSpace(provider), strings.TrimSpace(holder))
	if err != nil {
		return false, err
	}
	rows, _ := result.RowsAffected()
	return rows == 1, nil
}

func (s *Store) getComputeReconcileLease(ctx context.Context, userID, provider string) (ComputeReconcileLease, error) {
	var lease ComputeReconcileLease
	err := s.db.QueryRowContext(ctx, `
		SELECT owner_user_id,provider,holder,expires_at,heartbeat_at
		FROM compute_reconcile_leases WHERE owner_user_id=? AND provider=?`,
		strings.TrimSpace(userID), strings.TrimSpace(provider)).Scan(
		&lease.OwnerUserID, &lease.Provider, &lease.Holder, &lease.ExpiresAt, &lease.HeartbeatAt)
	return lease, err
}

func validateComputeLeaseInput(userID, provider, holder string, ttl time.Duration) error {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(provider) == "" || strings.TrimSpace(holder) == "" {
		return errors.New("compute reconcile lease owner, provider, and holder are required")
	}
	if ttl < time.Second || ttl > 10*time.Minute {
		return errors.New("compute reconcile lease ttl must be between one second and ten minutes")
	}
	return nil
}

func nullableComputeString(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func canTransitionComputeJob(current, next string) bool {
	failureTerminal := next == ComputeJobFailed ||
		next == ComputeJobTimedOut || next == ComputeJobOrphaned
	switch current {
	case ComputeJobPending:
		return next == ComputeJobStaging || next == ComputeJobQueued ||
			next == ComputeJobRunning || failureTerminal
	case ComputeJobStaging:
		return next == ComputeJobQueued || next == ComputeJobRunning || failureTerminal
	case ComputeJobQueued:
		return next == ComputeJobRunning || failureTerminal
	case ComputeJobRunning:
		return next == ComputeJobHarvesting || next == ComputeJobDone || failureTerminal
	case ComputeJobHarvesting:
		return next == ComputeJobDone || failureTerminal
	default:
		return false
	}
}

func isTerminalComputeJobState(state string) bool {
	return state == ComputeJobDone || state == ComputeJobFailed ||
		state == ComputeJobTimedOut || state == ComputeJobOrphaned
}
