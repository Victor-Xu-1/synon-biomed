package workspace

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUpsertComputeProviderRejectsCrossOwnerTakeover(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	created, err := store.UpsertComputeProvider(ComputeProviderInput{
		Name: "shared-name", UserID: "owner-a", Family: "local",
		Environments: []string{"python"}, MemoryMD: "owner-a-memory",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.UserID != "owner-a" {
		t.Fatalf("created owner = %q", created.UserID)
	}

	_, err = store.UpsertComputeProvider(ComputeProviderInput{
		Name: "shared-name", UserID: "owner-b", Family: "byoc",
		Environments: []string{"r"}, MemoryMD: "owner-b-memory",
	})
	if err == nil || !strings.Contains(err.Error(), "belongs to another user") {
		t.Fatalf("cross-owner upsert error = %v", err)
	}

	provider, found, err := store.GetComputeProvider("shared-name")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if provider.UserID != "owner-a" || provider.Family != "local" || provider.MemoryMD != "owner-a-memory" {
		t.Fatalf("provider changed after rejected takeover: %#v", provider)
	}
}

func TestComputeProviderReadsAreOwnerScopedAndIncludeGlobal(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for _, input := range []ComputeProviderInput{
		{Name: "owner-a-private", UserID: "owner-a", Family: "local"},
		{Name: "owner-b-private", UserID: "owner-b", Family: "local"},
		{Name: "legacy-global", UserID: "*", Family: "local"},
	} {
		if _, err := store.UpsertComputeProvider(input); err != nil {
			t.Fatalf("upsert %q: %v", input.Name, err)
		}
	}

	providers, err := store.ListComputeProviders("owner-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 2 || providers[0].Name != "legacy-global" || providers[1].Name != "owner-a-private" {
		t.Fatalf("owner-a providers = %#v", providers)
	}

	if _, found, err := store.GetComputeProvider("owner-a-private", "owner-b"); err != nil || found {
		t.Fatalf("cross-owner get found=%v err=%v", found, err)
	}
	global, found, err := store.GetComputeProvider("legacy-global", "owner-b")
	if err != nil || !found {
		t.Fatalf("global get found=%v err=%v", found, err)
	}
	if global.UserID != "*" {
		t.Fatalf("global owner = %q", global.UserID)
	}
}

func TestUpsertSSHProviderPersistsExecutionAuthority(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	concurrency, timeout := 3, 7200
	provider, err := store.UpsertSSHProvider(ComputeProviderInput{
		Name: "ssh:slurm", UserID: "owner", Family: "ssh", Scheduler: "slurm",
		ScratchRoot: "/scratch/owner", MemoryMD: "32 GB", MaxConcurrentJobs: &concurrency,
		MaxTimeoutSec: &timeout, DetailsMD: "scheduler: slurm",
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider.Scheduler != "slurm" || provider.ScratchRoot != "/scratch/owner" || provider.MemoryMD != "32 GB" ||
		provider.MaxConcurrentJobs == nil || *provider.MaxConcurrentJobs != concurrency ||
		provider.MaxTimeoutSec == nil || *provider.MaxTimeoutSec != timeout {
		t.Fatalf("SSH provider authority=%#v", provider)
	}
	if _, err := store.UpsertSSHProvider(ComputeProviderInput{
		Name: "ssh:invalid", UserID: "owner", Family: "ssh", Scheduler: "pbs",
	}); err == nil {
		t.Fatal("unsupported SSH scheduler was persisted")
	}
}

func TestComputeProviderMutationsRejectDifferentOwnerAndGlobalTakeover(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	private, err := store.CreateInferenceProvider(ComputeProviderInput{
		Name: "infer:private", UserID: "owner-a", Family: "infer",
		Endpoint: "http://127.0.0.1:9000", SkillName: "using-model-endpoint",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateInferenceProvider(ComputeProviderInput{
		Name: private.Name, UserID: "owner-b", Family: "infer",
		Endpoint: "http://127.0.0.1:9001", SkillName: "using-model-endpoint",
	}); err == nil {
		t.Fatal("different owner replaced inference provider")
	}
	if _, err := store.UpdateComputeProviderSettings(private.Name, "owner-b", "changed", nil, nil); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-owner settings error = %v", err)
	}
	if _, err := store.SetComputeProviderDataRoots(private.Name, "owner-b", []string{"/stolen"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-owner data roots error = %v", err)
	}
	scratch := "/stolen"
	if _, err := store.SetComputeProviderScratchRoot(private.Name, "owner-b", &scratch); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-owner scratch root error = %v", err)
	}
	if _, err := store.RecordComputeProbe(private.Name, "owner-b", "changed", nil); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-owner probe error = %v", err)
	}
	if deleted, err := store.DeleteComputeProvider(private.Name, "owner-b"); err != nil || deleted {
		t.Fatalf("cross-owner delete deleted=%v err=%v", deleted, err)
	}

	global, err := store.UpsertComputeProvider(ComputeProviderInput{
		Name: "legacy-global", UserID: "*", Family: "local", MemoryMD: "global-memory",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertComputeProvider(ComputeProviderInput{
		Name: global.Name, UserID: "owner-a", Family: "byoc", MemoryMD: "stolen",
	}); err == nil {
		t.Fatal("ordinary user took over global provider")
	}
	if _, err := store.UpdateComputeProviderSettings(global.Name, "owner-a", "changed", nil, nil); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("global settings error = %v", err)
	}
	if _, err := store.SetComputeProviderDataRoots(global.Name, "owner-a", []string{"/stolen"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("global data roots error = %v", err)
	}
	if deleted, err := store.DeleteComputeProvider(global.Name, "owner-a"); err != nil || deleted {
		t.Fatalf("global delete deleted=%v err=%v", deleted, err)
	}
	unchanged, found, err := store.GetComputeProvider(global.Name, "owner-a")
	if err != nil || !found || unchanged.UserID != "*" || unchanged.MemoryMD != "global-memory" {
		t.Fatalf("global after rejected mutations found=%v err=%v provider=%#v", found, err, unchanged)
	}

	globalInference, err := store.CreateInferenceProvider(ComputeProviderInput{
		Name: "infer:legacy-global", UserID: "*", Family: "infer",
		Endpoint: "http://127.0.0.1:9100", SkillName: "using-model-endpoint",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateInferenceProvider(ComputeProviderInput{
		Name: globalInference.Name, UserID: "owner-a", Family: "infer",
		Endpoint: "http://127.0.0.1:9101", SkillName: "using-model-endpoint",
	}); err == nil {
		t.Fatal("ordinary user replaced global inference provider")
	}

	globalSSH, err := store.UpsertSSHProvider(ComputeProviderInput{
		Name: "ssh:legacy-global", UserID: "*", DataRoots: []string{"/shared"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertSSHProvider(ComputeProviderInput{
		Name: globalSSH.Name, UserID: "owner-a", DataRoots: []string{"/stolen"},
	}); err == nil {
		t.Fatal("ordinary user replaced global SSH provider")
	}
	if _, err := store.SetComputeProviderScratchRoot(globalSSH.Name, "owner-a", &scratch); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("global scratch root error = %v", err)
	}
}

func TestComputeProviderProbeRevisionIsAtomicAndOwnerScoped(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	created, err := store.CreateInferenceProvider(ComputeProviderInput{
		Name: "infer:atomic", UserID: "owner-a", Family: "infer",
		Endpoint: "http://127.0.0.1:9000", SkillName: "using-model-endpoint",
		Scheduler: "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.DetailsRev != 0 || created.UserID != "owner-a" {
		t.Fatalf("created = %#v", created)
	}
	if _, err := store.RecordComputeProbe(created.Name, "owner-b", "must not write", nil); err == nil {
		t.Fatal("different owner unexpectedly updated provider")
	}

	const updates = 24
	var wait sync.WaitGroup
	errors := make(chan error, updates)
	for range updates {
		wait.Add(1)
		go func() {
			defer wait.Done()
			now := time.Now()
			_, err := store.RecordComputeProbe(created.Name, "owner-a", "ok", &now)
			errors <- err
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	provider, found, err := store.GetComputeProvider(created.Name)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if provider.DetailsRev != updates || provider.ProbedAt == nil {
		t.Fatalf("provider = %#v", provider)
	}
}

func TestComputeProviderLegacySchemaMigratesWithoutDataLoss(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open(sqliteDriver, path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(context.Background(), `
		CREATE TABLE compute_providers (
			name TEXT PRIMARY KEY,
			family TEXT NOT NULL,
			environments TEXT NOT NULL DEFAULT '[]',
			memory_md TEXT NOT NULL DEFAULT '',
			scratch_root TEXT NOT NULL DEFAULT '',
			scheduler TEXT NOT NULL DEFAULT '',
			updated_at TIMESTAMP NOT NULL
		);
		INSERT INTO compute_providers
			(name, family, environments, memory_md, scratch_root, scheduler, updated_at)
		VALUES ('legacy-local', 'local', '["python"]', 'memory', '/tmp/scratch', 'none', ?)
	`, time.Now().UTC())
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider, found, err := store.GetComputeProvider("legacy-local")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if provider.UserID != "local" || provider.Family != "local" || provider.MemoryMD != "memory" || len(provider.Environments) != 1 {
		t.Fatalf("migrated provider = %#v", provider)
	}
	if provider.DetailsRev != 0 || provider.Endpoint != "" {
		t.Fatalf("new defaults = %#v", provider)
	}
}
