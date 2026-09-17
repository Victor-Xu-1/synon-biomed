package workspace

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestComputeWorkbenchLifecyclePersistsAndIsOwnerScoped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	credential := "NVIDIA_API_KEY"
	if err := store.UpsertManagedEndpoint(ManagedEndpoint{Name: "fixture-nim", RegisteredBy: "owner-a", URL: "http://127.0.0.1:9444/v1", Port: 9444, State: "live", SkillName: "using-model-endpoint", CredentialName: &credential, LivePath: "/v1/health/ready", StartScript: "fixture start", StopScript: "fixture stop", ApprovedScriptHash: "sha256:fixture", ServiceDir: "/tmp/fixture", StateChangedAt: &now}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateInferenceProvider(ComputeProviderInput{Name: "fixture-nim", UserID: "owner-a", Family: "infer", Endpoint: "http://127.0.0.1:9444/v1", SkillName: "using-model-endpoint"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-a", UserID: "owner-a", Name: "Compute fixture"}); err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateComputeJob("owner-a", ComputeJob{JobID: "compute-fixture-job-0001", Environment: "python", TierType: "gpu", Provider: "fixture-nim", ProjectID: "project-a", StartedAt: now, ProviderFamily: "infer", ProviderLabel: "fixture-nim", SupportsTail: true, LeftOnRemote: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BindComputeJobExternal("owner-a", job.JobID, "sandbox-1", "https://compute.example/jobs/sandbox-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendComputeJobLog("owner-a", job.JobID, "stdout", strings.Repeat("x", 70000)+"done\n"); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendComputeJobLog("owner-a", job.JobID, "stderr", "warning\n"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSessionComputeProvider("owner-a", "root-a", "infer:fixture-nim", true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetComputeGPUEnabled(true); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	gpu, err := store.GetComputeGPUSettings()
	if err != nil || !gpu.Enabled || gpu.Override == nil || !*gpu.Override {
		t.Fatalf("gpu=%#v err=%v", gpu, err)
	}
	endpoints, err := store.ListManagedEndpoints("owner-a", "", false)
	if err != nil || len(endpoints) != 1 || endpoints[0].ServiceDirBytes != nil {
		t.Fatalf("endpoints=%#v err=%v", endpoints, err)
	}
	foreign, err := store.ListManagedEndpoints("owner-b", "", true)
	if err != nil || len(foreign) != 0 {
		t.Fatalf("foreign=%#v err=%v", foreign, err)
	}
	jobs, err := store.ListComputeJobs("owner-a", "project-a")
	if err != nil || len(jobs) != 1 || jobs[0].RootFrameID != nil {
		t.Fatalf("jobs=%#v err=%v", jobs, err)
	}
	foreignJobs, err := store.ListComputeJobs("owner-b", "project-a")
	if err != nil || len(foreignJobs) != 0 {
		t.Fatalf("foreign jobs=%#v err=%v", foreignJobs, err)
	}
	log, found, err := store.GetComputeJobLog("owner-a", job.JobID, "stdout", 16)
	if err != nil || !found || !log.Truncated || log.Text != "xxxxxxxxxxxdone\n" {
		t.Fatalf("log=%#v found=%v err=%v", log, found, err)
	}
	_, found, err = store.GetComputeJobLog("owner-b", job.JobID, "stdout", 16)
	if err != nil || found {
		t.Fatalf("foreign log found=%v err=%v", found, err)
	}
	providers, err := store.ListSessionComputeProviders("owner-a", "root-a")
	if err != nil || len(providers) != 1 || providers[0] != "infer:fixture-nim" {
		t.Fatalf("providers=%#v err=%v", providers, err)
	}
	if _, _, err := store.ClaimManagedEndpointStop("fixture-nim", "owner-b"); err == nil {
		t.Fatal("foreign owner stopped endpoint")
	}
	endpoint, shouldRun, err := store.ClaimManagedEndpointStop("fixture-nim", "owner-a")
	if err != nil || !shouldRun {
		t.Fatalf("claim endpoint=%#v run=%v err=%v", endpoint, shouldRun, err)
	}
	state, err := store.FinishManagedEndpointStop(endpoint.ClaimHandle, "stopped through fixture", nil)
	if err != nil || state != "stopped" {
		t.Fatalf("stop state=%q err=%v", state, err)
	}
}

func TestManagedEndpointStartFailureStaysFailedUntilExplicitStop(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.UpsertManagedEndpoint(ManagedEndpoint{
		Name: "start-fixture", RegisteredBy: "owner-a", URL: "http://127.0.0.1:25001/v1",
		Port: 25001, State: "stopped", Location: "local", SkillName: "using-model-endpoint",
		LivePath: "/health", StartScript: "start", StopScript: "stop", ApprovedScriptHash: "fixture",
	}); err != nil {
		t.Fatal(err)
	}
	claimed, shouldRun, err := store.ClaimManagedEndpointStart("start-fixture", "owner-a")
	if err != nil || !shouldRun || claimed.ClaimHandle == "" || claimed.State != "starting" {
		t.Fatalf("start claim=%#v shouldRun=%t err=%v", claimed, shouldRun, err)
	}
	if state, err := store.FinishManagedEndpointStart(claimed.ClaimHandle, "failed transcript", errors.New("start failed")); err != nil || state != "failed" {
		t.Fatalf("failed start state=%q err=%v", state, err)
	}
	if _, _, err := store.ClaimManagedEndpointStart("start-fixture", "owner-a"); !errors.Is(err, ErrManagedEndpointFailed) {
		t.Fatalf("failed endpoint retry error=%v", err)
	}
	stop, shouldRun, err := store.ClaimManagedEndpointStop("start-fixture", "owner-a")
	if err != nil || !shouldRun {
		t.Fatalf("failed endpoint stop=%#v shouldRun=%t err=%v", stop, shouldRun, err)
	}
	if state, err := store.FinishManagedEndpointStop(stop.ClaimHandle, "reset", nil); err != nil || state != "stopped" {
		t.Fatalf("reset state=%q err=%v", state, err)
	}
	claimed, shouldRun, err = store.ClaimManagedEndpointStart("start-fixture", "owner-a")
	if err != nil || !shouldRun {
		t.Fatalf("restarted claim=%#v shouldRun=%t err=%v", claimed, shouldRun, err)
	}
	if state, err := store.FinishManagedEndpointStart(claimed.ClaimHandle, "ready", nil); err != nil || state != "live" {
		t.Fatalf("ready state=%q err=%v", state, err)
	}
}

func TestManagedEndpointClaimRejectsStaleFinisherAcrossStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	storeA, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer storeA.Close()
	storeB, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer storeB.Close()

	endpoint := ManagedEndpoint{
		Name: "shared-endpoint", RegisteredBy: "owner-a", State: "live",
		StopScript: "true", ApprovedScriptHash: "approved",
	}
	if err := storeA.UpsertManagedEndpoint(endpoint); err != nil {
		t.Fatal(err)
	}
	firstClaim, shouldRun, err := storeA.ClaimManagedEndpointStop(endpoint.Name, endpoint.RegisteredBy)
	if err != nil || !shouldRun {
		t.Fatalf("first claim=%#v shouldRun=%v err=%v", firstClaim, shouldRun, err)
	}

	endpoint.State = "live"
	if err := storeB.UpsertManagedEndpoint(endpoint); err != nil {
		t.Fatal(err)
	}
	secondClaim, shouldRun, err := storeB.ClaimManagedEndpointStop(endpoint.Name, endpoint.RegisteredBy)
	if err != nil || !shouldRun {
		t.Fatalf("second claim=%#v shouldRun=%v err=%v", secondClaim, shouldRun, err)
	}
	if firstClaim.Name != endpoint.Name || secondClaim.Name != endpoint.Name {
		t.Fatalf("claim changed endpoint names: first=%q second=%q", firstClaim.Name, secondClaim.Name)
	}
	if firstClaim.ApprovedScriptHash != endpoint.ApprovedScriptHash || secondClaim.ApprovedScriptHash != endpoint.ApprovedScriptHash {
		t.Fatalf("claim changed approval hashes: first=%q second=%q", firstClaim.ApprovedScriptHash, secondClaim.ApprovedScriptHash)
	}
	if firstClaim.ClaimHandle == "" || firstClaim.ClaimHandle == secondClaim.ClaimHandle {
		t.Fatalf("claim handles first=%q second=%q", firstClaim.ClaimHandle, secondClaim.ClaimHandle)
	}

	if _, err := storeA.FinishManagedEndpointStop(firstClaim.ClaimHandle, "stale", nil); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale finish err=%v", err)
	}
	state, err := storeB.FinishManagedEndpointStop(secondClaim.ClaimHandle, "current", nil)
	if err != nil || state != "stopped" {
		t.Fatalf("current finish state=%q err=%v", state, err)
	}
	endpoints, err := storeA.ListManagedEndpoints("owner-a", endpoint.Name, false)
	if err != nil || len(endpoints) != 1 || endpoints[0].State != "stopped" || endpoints[0].Transcript == nil || *endpoints[0].Transcript != "current" {
		t.Fatalf("endpoints=%#v err=%v", endpoints, err)
	}
}

func TestManagedEndpointMutationsCannotCrossOwners(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	ownerStore, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ownerStore.Close()
	foreignStore, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer foreignStore.Close()

	owned := ManagedEndpoint{Name: "shared-name", RegisteredBy: "owner-a", URL: "http://owner-a", State: "live"}
	if err := ownerStore.UpsertManagedEndpoint(owned); err != nil {
		t.Fatal(err)
	}
	foreign := owned
	foreign.RegisteredBy = "owner-b"
	foreign.URL = "http://owner-b"
	if err := foreignStore.UpsertManagedEndpoint(foreign); err == nil {
		t.Fatalf("foreign upsert err=%v", err)
	}
	if deleted, err := foreignStore.DeleteManagedEndpoint(owned.Name, foreign.RegisteredBy); err != nil || deleted {
		t.Fatalf("foreign delete deleted=%v err=%v", deleted, err)
	}
	endpoints, err := ownerStore.ListManagedEndpoints(owned.RegisteredBy, owned.Name, false)
	if err != nil || len(endpoints) != 1 || endpoints[0].URL != owned.URL {
		t.Fatalf("owned endpoints=%#v err=%v", endpoints, err)
	}
	foreignEndpoints, err := foreignStore.ListManagedEndpoints(foreign.RegisteredBy, foreign.Name, false)
	if err != nil || len(foreignEndpoints) != 0 {
		t.Fatalf("foreign endpoints=%#v err=%v", foreignEndpoints, err)
	}
}

func TestSessionComputeProvidersAreIndependentAcrossOwnersAndMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	storeA, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer storeA.Close()
	storeB, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer storeB.Close()

	for _, storeAndOwner := range []struct {
		store *Store
		owner string
	}{{storeA, "owner-a"}, {storeB, "owner-b"}} {
		if err := storeAndOwner.store.SetSessionComputeProvider(storeAndOwner.owner, "root-shared", "provider-existing", true); err != nil {
			t.Fatal(err)
		}
	}
	if err := storeA.SetSessionComputeProvider("owner-a", "draft-shared", "provider-shared", false); err != nil {
		t.Fatal(err)
	}
	if err := storeB.SetSessionComputeProvider("owner-b", "draft-shared", "provider-shared", true); err != nil {
		t.Fatal(err)
	}
	if err := storeA.MigrateSessionComputeProviders("owner-a", "draft-shared", "root-shared"); err != nil {
		t.Fatal(err)
	}

	ownerA, configured, err := storeA.SessionComputeProviderSelection("owner-a", "root-shared")
	if err != nil || len(ownerA) != 0 || !configured {
		t.Fatalf("owner A providers=%v configured=%v err=%v", ownerA, configured, err)
	}
	ownerB, err := storeB.ListSessionComputeProviders("owner-b", "root-shared")
	if err != nil || len(ownerB) != 1 || ownerB[0] != "provider-existing" {
		t.Fatalf("owner B target providers=%v err=%v", ownerB, err)
	}
	ownerBDraft, err := storeB.ListSessionComputeProviders("owner-b", "draft-shared")
	if err != nil || len(ownerBDraft) != 1 || ownerBDraft[0] != "provider-shared" {
		t.Fatalf("owner B draft providers=%v err=%v", ownerBDraft, err)
	}
	ownerADraft, configured, err := storeA.SessionComputeProviderSelection("owner-a", "draft-shared")
	if err != nil || len(ownerADraft) != 0 || configured {
		t.Fatalf("owner A source providers=%v configured=%v err=%v", ownerADraft, configured, err)
	}
	if err := storeA.SetSessionComputeProvider("owner-a", "draft-bad\nkey", "provider", true); err == nil {
		t.Fatal("control character in session key was accepted")
	}
}

func TestSessionComputeProviderSchemaUpgradeAddsOwnerToPrimaryKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	legacy, err := sql.Open(sqliteDriver, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE compute_session_enabled (root_frame_id TEXT NOT NULL, owner_user_id TEXT NOT NULL, provider_name TEXT NOT NULL, checked INTEGER NOT NULL, updated_at TIMESTAMP NOT NULL, PRIMARY KEY(root_frame_id, provider_name))`,
		`INSERT INTO compute_session_enabled(root_frame_id,owner_user_id,provider_name,checked,updated_at) VALUES('root-shared','owner-a','provider-shared',1,'2026-07-11T08:00:00Z')`,
	} {
		if _, err := legacy.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SetSessionComputeProvider("owner-b", "root-shared", "provider-shared", true); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"owner-a", "owner-b"} {
		providers, err := store.ListSessionComputeProviders(owner, "root-shared")
		if err != nil || len(providers) != 1 || providers[0] != "provider-shared" {
			t.Fatalf("owner=%s providers=%v err=%v", owner, providers, err)
		}
	}
}

func TestComputeJobMutationsAreOwnerScoped(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateInferenceProvider(ComputeProviderInput{Name: "infer:owner-scoped", UserID: "owner-a", Family: "infer", Endpoint: "http://127.0.0.1:9444/v1", SkillName: "using-model-endpoint"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-owner-scoped", UserID: "owner-a", Name: "Owner scoped compute"}); err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateComputeJob("owner-a", ComputeJob{JobID: "job-owner-scoped", ProjectID: "project-owner-scoped", Environment: "python", TierType: "gpu", Provider: "infer:owner-scoped"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.BindComputeJobExternal("owner-b", job.JobID, "foreign-external", ""); !errors.Is(err, ErrComputeJobNotFound) {
		t.Fatalf("foreign bind err=%v", err)
	}
	if _, err := store.TransitionComputeJob("owner-b", job.JobID, ComputeJobFailed, "foreign", time.Time{}); !errors.Is(err, ErrComputeJobNotFound) {
		t.Fatalf("foreign transition err=%v", err)
	}
	if err := store.AppendComputeJobLog("owner-b", job.JobID, "stdout", "foreign stdout"); !errors.Is(err, ErrComputeJobNotFound) {
		t.Fatalf("foreign stdout append err=%v", err)
	}
	if err := store.AppendComputeJobLog("owner-b", job.JobID, "stderr", "foreign stderr"); !errors.Is(err, ErrComputeJobNotFound) {
		t.Fatalf("foreign stderr append err=%v", err)
	}

	unchanged, found, err := store.GetComputeJob("owner-a", job.JobID)
	if err != nil || !found || unchanged.State != ComputeJobPending || unchanged.ExternalID != nil || unchanged.ErrorKind != nil {
		t.Fatalf("job=%#v found=%v err=%v", unchanged, found, err)
	}
	for _, stream := range []string{"stdout", "stderr"} {
		log, found, err := store.GetComputeJobLog("owner-a", job.JobID, stream, 1024)
		if err != nil || !found || log.Size != 0 || log.Text != "" {
			t.Fatalf("%s log=%#v found=%v err=%v", stream, log, found, err)
		}
	}
}
