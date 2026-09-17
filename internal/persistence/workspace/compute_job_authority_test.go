package workspace

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	compute "synon-go/internal/compute"
)

func TestTerminalComputeTransitionPublishesNotificationAtomically(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SetBYOCEnabled("modal", "owner", true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(CreateFrameInput{
		ID: "frame", ProjectID: "project", AgentName: "GENERAL", Status: "processing", ConversationType: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	createRunning := func(jobID, externalID string) ComputeJob {
		t.Helper()
		frameID, rootFrameID := frame.ID, frame.RootFrameID
		job, createErr := store.CreateComputeJob("owner", ComputeJob{
			JobID: jobID, ProjectID: "project", Environment: "python", TierType: "remote",
			Provider: "byoc:modal", ProviderFamily: "byoc", ProviderLabel: "modal",
			FrameID: &frameID, RootFrameID: &rootFrameID,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		job, createErr = store.BindComputeJobExternal("owner", job.JobID, externalID, "")
		if createErr != nil {
			t.Fatal(createErr)
		}
		return job
	}
	job := createRunning("job-atomic", "sandbox-atomic")
	endedAt := time.Date(2026, 8, 22, 8, 0, 0, 0, time.UTC)
	updated, notification, _, err := store.TransitionComputeJobWithNotification(
		context.Background(), "owner", job.JobID, ComputeJobDone, "", endedAt,
		CreateNotificationInput{
			ID: "compute-done-atomic", SenderFrameID: frame.ID, RecipientFrameID: frame.ID,
			RootFrameID: frame.RootFrameID, OwnerUserID: "owner", NotificationType: "compute_done",
			Payload: map[string]any{"job_id": job.JobID, "state": ComputeJobDone},
		},
	)
	if err != nil || updated.State != ComputeJobDone || notification.ID != "compute-done-atomic" {
		t.Fatalf("terminal transition updated=%#v notification=%#v err=%v", updated, notification, err)
	}
	if unread, countErr := store.CountUnreadNotifications(context.Background(), frame.ID, frame.RootFrameID, "owner"); countErr != nil || unread != 1 {
		t.Fatalf("terminal notification unread=%d err=%v", unread, countErr)
	}

	rollback := createRunning("job-rollback", "sandbox-rollback")
	if _, _, _, err := store.TransitionComputeJobWithNotification(
		context.Background(), "owner", rollback.JobID, ComputeJobFailed, "fixture", endedAt,
		CreateNotificationInput{
			ID: "compute-done-invalid", SenderFrameID: frame.ID, RecipientFrameID: frame.ID,
			RootFrameID: "missing-root", OwnerUserID: "owner", NotificationType: "compute_done",
			Payload: map[string]any{"job_id": rollback.JobID, "state": ComputeJobFailed},
		},
	); err == nil {
		t.Fatal("terminal transition accepted an invalid notification authority")
	}
	retained, found, err := store.GetComputeJob("owner", rollback.JobID)
	if err != nil || !found || retained.State != ComputeJobRunning {
		t.Fatalf("failed notification did not roll back terminal transition: job=%#v found=%t err=%v", retained, found, err)
	}
}

func TestComputeJobAuthorityEnforcesProductionStateTransitions(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SetBYOCEnabled("modal", "owner-a", true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-a", UserID: "owner-a", Name: "Project A"}); err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateComputeJob("owner-a", ComputeJob{
		JobID: "job-authority-1", ProjectID: "project-a", Environment: "python",
		TierType: "gpu", Provider: "byoc:modal", State: ComputeJobPending,
		ProviderFamily: "byoc", ProviderLabel: "Modal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.State != ComputeJobPending || job.StartedAt.IsZero() {
		t.Fatalf("job=%#v", job)
	}
	if _, err := store.TransitionComputeJob("owner-a", job.JobID, ComputeJobDone, "", time.Time{}); !errors.Is(err, ErrComputeJobTransition) {
		t.Fatalf("pending -> done err=%v", err)
	}
	usage, found, err := store.GetComputeUsageByJobID(job.JobID)
	if err != nil || !found || usage.State != ComputeStatePending || usage.ProjectID != "project-a" {
		t.Fatalf("authoritative usage=%#v found=%v err=%v", usage, found, err)
	}
	externalURL := "https://modal.com/apps/test/sb-123"
	running, err := store.BindComputeJobExternal("owner-a", job.JobID, "sb-123", externalURL)
	if err != nil {
		t.Fatal(err)
	}
	if running.State != ComputeJobRunning || running.ExternalID == nil || *running.ExternalID != "sb-123" {
		t.Fatalf("running=%#v", running)
	}
	usage, found, err = store.GetComputeUsageByJobID(job.JobID)
	if err != nil || !found || usage.State != ComputeStateRunning || len(usage.RemoteHandle) == 0 {
		t.Fatalf("bound authoritative usage=%#v found=%v err=%v", usage, found, err)
	}
	active, err := store.ListActiveComputeJobs("owner-a", "byoc:modal")
	if err != nil || len(active) != 1 || active[0].JobID != job.JobID {
		t.Fatalf("active=%#v err=%v", active, err)
	}
	second, err := store.CreateComputeJob("owner-a", ComputeJob{
		JobID: "job-authority-2", ProjectID: "project-a", Environment: "python",
		TierType: "gpu", Provider: "byoc:modal", ProviderFamily: "byoc", ProviderLabel: "Modal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BindComputeJobExternal("owner-a", second.JobID, "sb-123", ""); !errors.Is(err, ErrComputeExternalIDConflict) {
		t.Fatalf("duplicate active external id err=%v", err)
	}
	failedAt := time.Date(2026, 7, 12, 2, 3, 4, 0, time.UTC)
	failed, err := store.TransitionComputeJob("owner-a", job.JobID, ComputeJobFailed, "remote_error", failedAt)
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != ComputeJobFailed || failed.EndedAtISO == nil || failed.ErrorKind == nil ||
		*failed.ErrorKind != "remote_error" {
		t.Fatalf("failed=%#v", failed)
	}
	usage, found, err = store.GetComputeUsageByJobID(job.JobID)
	if err != nil || !found || usage.State != ComputeStateFailed || usage.EndedAt == nil {
		t.Fatalf("terminal authoritative usage=%#v found=%v err=%v", usage, found, err)
	}
	if _, err := store.TransitionComputeJob("owner-a", job.JobID, ComputeJobRunning, "", time.Time{}); !errors.Is(err, ErrComputeJobTransition) {
		t.Fatalf("terminal -> running err=%v", err)
	}
	active, err = store.ListActiveComputeJobs("owner-a", "byoc:modal")
	if err != nil || len(active) != 0 {
		t.Fatalf("terminal active=%#v err=%v", active, err)
	}
	if _, err := store.BindComputeJobExternal("owner-b", job.JobID, "sb-foreign", ""); !errors.Is(err, ErrComputeJobNotFound) {
		t.Fatalf("foreign bind err=%v", err)
	}
	reused, err := store.BindComputeJobExternal("owner-a", second.JobID, "sb-123", "")
	if err != nil || reused.State != ComputeJobRunning {
		t.Fatalf("terminal sandbox reuse=%#v err=%v", reused, err)
	}
}

func TestComputeJobAllowsExplicitSharedLegacyProviderWithoutOwnershipTransfer(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	if _, err := store.db.Exec(`
		INSERT INTO compute_providers(
			name,owner_user_id,family,environments,memory_md,scratch_root,scheduler,updated_at
		) VALUES('legacy-shared','*','local','["python"]','','','local',?)`, now); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"owner-a", "owner-b"} {
		projectID := "project-" + owner
		if _, err := store.CreateProject(CreateProjectInput{
			ID: projectID, UserID: owner, Name: "Shared provider " + owner,
		}); err != nil {
			t.Fatal(err)
		}
		job, err := store.CreateComputeJob(owner, ComputeJob{
			JobID: "job-" + owner, ProjectID: projectID, Environment: "python",
			TierType: "cpu", Provider: "legacy-shared",
			ProviderFamily: "local", ProviderLabel: "Legacy shared",
		})
		if err != nil {
			t.Fatalf("%s create shared job: %v", owner, err)
		}
		if job.ProjectID != projectID || job.Provider != "legacy-shared" {
			t.Fatalf("%s job=%#v", owner, job)
		}
	}
	var providerOwner string
	if err := store.db.QueryRow(`SELECT owner_user_id FROM compute_providers WHERE name='legacy-shared'`).Scan(&providerOwner); err != nil {
		t.Fatal(err)
	}
	if providerOwner != "*" {
		t.Fatalf("shared provider owner changed to %q", providerOwner)
	}
}

func TestComputeJobCreationAtomicallyEnforcesProviderConcurrency(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	limit := 1
	if _, err := store.UpsertSSHProvider(ComputeProviderInput{
		Name: "ssh:bounded", UserID: "owner", Family: "ssh", MaxConcurrentJobs: &limit,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	first, err := store.CreateComputeJob("owner", ComputeJob{
		JobID: "job-capacity-1", ProjectID: "project", Environment: "remote", TierType: "remote",
		Provider: "ssh:bounded", ProviderFamily: "ssh", ProviderLabel: "bounded",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateComputeJob("owner", ComputeJob{
		JobID: "job-capacity-2", ProjectID: "project", Environment: "remote", TierType: "remote",
		Provider: "ssh:bounded", ProviderFamily: "ssh", ProviderLabel: "bounded",
	}); !errors.Is(err, ErrComputeProviderConcurrencyFull) {
		t.Fatalf("second active job error=%v", err)
	}
	if _, err := store.TransitionComputeJob("owner", first.JobID, ComputeJobFailed, "fixture", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateComputeJob("owner", ComputeJob{
		JobID: "job-capacity-3", ProjectID: "project", Environment: "remote", TierType: "remote",
		Provider: "ssh:bounded", ProviderFamily: "ssh", ProviderLabel: "bounded",
	}); err != nil {
		t.Fatalf("capacity was not released after terminal transition: %v", err)
	}
}

func TestComputeReconcileLeaseIsExclusiveAndSupportsExpiryTakeover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	base := time.Date(2026, 7, 12, 1, 0, 0, 0, time.UTC)
	first.now = func() time.Time { return base }
	second.now = func() time.Time { return base }

	lease, acquired, err := first.AcquireComputeReconcileLease("owner-a", "byoc:modal", "worker-a", 30*time.Second)
	if err != nil || !acquired || lease.Holder != "worker-a" {
		t.Fatalf("first lease=%#v acquired=%v err=%v", lease, acquired, err)
	}
	lease, acquired, err = second.AcquireComputeReconcileLease("owner-a", "byoc:modal", "worker-b", 30*time.Second)
	if err != nil || acquired || lease.Holder != "worker-a" {
		t.Fatalf("contended lease=%#v acquired=%v err=%v", lease, acquired, err)
	}
	base = base.Add(20 * time.Second)
	lease, renewed, err := first.HeartbeatComputeReconcileLease("owner-a", "byoc:modal", "worker-a", 30*time.Second)
	if err != nil || !renewed || !lease.ExpiresAt.Equal(base.Add(30*time.Second)) {
		t.Fatalf("heartbeat lease=%#v renewed=%v err=%v", lease, renewed, err)
	}
	base = base.Add(31 * time.Second)
	lease, acquired, err = second.AcquireComputeReconcileLease("owner-a", "byoc:modal", "worker-b", 30*time.Second)
	if err != nil || !acquired || lease.Holder != "worker-b" {
		t.Fatalf("takeover lease=%#v acquired=%v err=%v", lease, acquired, err)
	}
	if released, err := first.ReleaseComputeReconcileLease("owner-a", "byoc:modal", "worker-a"); err != nil || released {
		t.Fatalf("stale release=%v err=%v", released, err)
	}
	if released, err := second.ReleaseComputeReconcileLease("owner-a", "byoc:modal", "worker-b"); err != nil || !released {
		t.Fatalf("owner release=%v err=%v", released, err)
	}
}

func TestModalComputeIdentityTagsAreVersionedAndComplete(t *testing.T) {
	tags, err := compute.ModalComputeIdentityTags(compute.ModalComputeIdentity{
		InstallID: "install-a", JobID: "job-a", RootFrameID: "root-a", FrameID: "frame-a", ProjectID: "project-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	for key, expected := range map[string]string{
		"synonbiomed-org":              "install-a",
		"synonbiomed-session":          "root-a",
		"synonbiomed-frame":            "frame-a",
		"synonbiomed-job":              "job-a",
		"synonbiomed-project":          "project-a",
		"synonbiomed-identity-version": "1",
	} {
		if tags[key] != expected {
			t.Fatalf("tag %s=%q expected=%q tags=%#v", key, tags[key], expected, tags)
		}
	}
	if _, err := compute.ModalComputeIdentityTags(compute.ModalComputeIdentity{InstallID: "install-a"}); err == nil {
		t.Fatal("incomplete identity accepted")
	}
}
