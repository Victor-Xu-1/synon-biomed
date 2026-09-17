package kernel

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"synon-go/internal/toolprogress"
)

type managedEnvironmentOperation struct {
	done    chan struct{}
	cancel  context.CancelFunc
	context context.Context
	waiters int
	result  ManagedEnvironment
	err     error

	progressMu          sync.Mutex
	progressSubscribers map[int]toolprogress.Reporter
	nextProgressID      int
	latestProgress      *toolprogress.Update
}

func (o *managedEnvironmentOperation) publishProgress(update toolprogress.Update) {
	if o == nil {
		return
	}
	update = toolprogress.Normalize(update)
	o.progressMu.Lock()
	if o.latestProgress != nil {
		update = toolprogress.Merge(*o.latestProgress, update)
	}
	snapshot := toolprogress.Clone(update)
	o.latestProgress = &snapshot
	reporters := make([]toolprogress.Reporter, 0, len(o.progressSubscribers))
	for _, reporter := range o.progressSubscribers {
		reporters = append(reporters, reporter)
	}
	o.progressMu.Unlock()
	for _, reporter := range reporters {
		reporter(toolprogress.Clone(snapshot))
	}
}

func (o *managedEnvironmentOperation) subscribeProgress(reporter toolprogress.Reporter) (int, *toolprogress.Update) {
	if o == nil || reporter == nil {
		return 0, nil
	}
	o.progressMu.Lock()
	defer o.progressMu.Unlock()
	if o.progressSubscribers == nil {
		o.progressSubscribers = map[int]toolprogress.Reporter{}
	}
	o.nextProgressID++
	id := o.nextProgressID
	o.progressSubscribers[id] = reporter
	if o.latestProgress == nil {
		return id, nil
	}
	snapshot := toolprogress.Clone(*o.latestProgress)
	return id, &snapshot
}

func (o *managedEnvironmentOperation) unsubscribeProgress(id int) {
	if o == nil || id <= 0 {
		return
	}
	o.progressMu.Lock()
	delete(o.progressSubscribers, id)
	o.progressMu.Unlock()
}

var (
	managedEnvironmentSupervisorStartupTimeout = 15 * time.Second
	managedEnvironmentStagingRetention         = 15 * time.Minute
	managedInstallerCacheTempRetention         = 30 * time.Minute
	managedEnvironmentMaintenanceInterval      = 30 * time.Minute
	managedEnvironmentMaintenanceLockTimeout   = 250 * time.Millisecond
)

type ManagedEnvironmentMaintenanceReport struct {
	RemovedStaging     int
	RetainedStaging    int
	RemovedCacheTemps  int
	RetainedCacheTemps int
	RemovedCacheBytes  int64
}

func (m *Manager) ManagedEnvironmentSupervisorEnabled() bool {
	return m != nil && m.config.Micromamba != "" && m.config.CondaHome != "" && m.config.CondaEnvsPath != ""
}

// ManagedEnvironmentSupervisorReady reports whether package/environment
// mutations can be admitted now. Enabled configuration alone is insufficient:
// advertising installer tools before the service-owned supervisor is running
// makes an agent wait until its request is cancelled.
func (m *Manager) ManagedEnvironmentSupervisorReady() bool {
	if !m.ManagedEnvironmentSupervisorEnabled() {
		return false
	}
	m.managedEnvironmentMu.Lock()
	defer m.managedEnvironmentMu.Unlock()
	return m.managedEnvironmentSupervisor != nil && m.managedEnvironmentSupervisor.Err() == nil
}

// RunManagedEnvironmentSupervisor supplies the parent authority for managed
// installers. Individual operations are still reference-counted by their live
// task waiters: concurrent tasks can share one installation, while the last
// waiter leaving cancels the operation and its process tree instead of leaking
// task work into the service lifetime.
func (m *Manager) RunManagedEnvironmentSupervisor(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("managed environment supervisor requires a service context")
	}
	m.managedEnvironmentMu.Lock()
	if active := m.managedEnvironmentSupervisor; active != nil && active.Err() == nil {
		m.managedEnvironmentMu.Unlock()
		return errors.New("managed environment supervisor is already running")
	}
	m.managedEnvironmentSupervisor = ctx
	m.managedEnvironmentSupervisorOnce.Do(func() { close(m.managedEnvironmentSupervisorReady) })
	m.managedEnvironmentMu.Unlock()

	runMaintenance := func() {
		report, err := m.sweepManagedEnvironmentStaging(ctx, time.Now().UTC(), managedEnvironmentStagingRetention)
		cacheReport, cacheErr := m.sweepManagedInstallerCacheTemps(ctx, time.Now().UTC(), managedInstallerCacheTempRetention)
		report.RemovedCacheTemps = cacheReport.RemovedCacheTemps
		report.RetainedCacheTemps = cacheReport.RetainedCacheTemps
		report.RemovedCacheBytes = cacheReport.RemovedCacheBytes
		err = errors.Join(err, cacheErr)
		if err != nil && ctx.Err() == nil {
			log.Printf("managed environment maintenance staging_retained=%d staging_removed=%d cache_retained=%d cache_removed=%d cache_removed_bytes=%d: %v",
				report.RetainedStaging, report.RemovedStaging, report.RetainedCacheTemps,
				report.RemovedCacheTemps, report.RemovedCacheBytes, err)
		} else if report.RemovedStaging != 0 || report.RemovedCacheTemps != 0 {
			log.Printf("managed environment maintenance staging_removed=%d staging_retained=%d cache_removed=%d cache_retained=%d cache_removed_bytes=%d",
				report.RemovedStaging, report.RetainedStaging, report.RemovedCacheTemps,
				report.RetainedCacheTemps, report.RemovedCacheBytes)
		}
	}
	runMaintenance()
	ticker := time.NewTicker(managedEnvironmentMaintenanceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			goto stopped
		case <-ticker.C:
			runMaintenance()
		}
	}

stopped:
	m.managedEnvironmentMu.Lock()
	if m.managedEnvironmentSupervisor == ctx {
		m.managedEnvironmentSupervisor = nil
	}
	m.managedEnvironmentMu.Unlock()
	return nil
}

// sweepManagedInstallerCacheTemps removes only micromamba's crash/interruption
// scratch files. It shares the exact host-wide installer admission lock with
// provisioning, so maintenance can never race a live solve or download. The
// strict filename, regular-file, age, and containment checks deliberately
// exclude package metadata, archives, directories, and symlinks.
func (m *Manager) sweepManagedInstallerCacheTemps(
	ctx context.Context,
	now time.Time,
	minimumAge time.Duration,
) (ManagedEnvironmentMaintenanceReport, error) {
	report := ManagedEnvironmentMaintenanceReport{}
	if m == nil {
		return report, errors.New("kernel manager is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if minimumAge < 0 {
		return report, errors.New("managed installer cache retention is invalid")
	}
	environmentRoot, err := m.managedEnvironmentRoot()
	if err != nil {
		return report, err
	}
	condaHome := filepath.Clean(strings.TrimSpace(m.config.CondaHome))
	if !filepath.IsAbs(condaHome) {
		return report, errors.New("managed installer cache root is not configured")
	}
	cacheRoot := filepath.Join(condaHome, "pkgs", "cache")
	entries, err := os.ReadDir(cacheRoot)
	if errors.Is(err, os.ErrNotExist) {
		return report, nil
	}
	if err != nil {
		return report, errors.New("managed installer cache cannot be inspected")
	}
	candidates := 0
	for _, entry := range entries {
		if managedInstallerCacheTempName(entry.Name()) {
			candidates++
		}
	}
	lockCtx, cancelLock := context.WithTimeout(ctx, managedEnvironmentMaintenanceLockTimeout)
	release, err := lockKernelFile(lockCtx, filepath.Join(environmentRoot, ".installer.lock"))
	cancelLock()
	if err != nil {
		report.RetainedCacheTemps = candidates
		return report, nil
	}
	defer release()
	entries, err = os.ReadDir(cacheRoot)
	if err != nil {
		return report, errors.New("managed installer cache cannot be reinspected")
	}
	var maintenanceErr error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if !managedInstallerCacheTempName(entry.Name()) {
			continue
		}
		candidate := filepath.Join(cacheRoot, entry.Name())
		relative, relErr := filepath.Rel(cacheRoot, candidate)
		info, statErr := os.Lstat(candidate)
		if relErr != nil || relative != entry.Name() || statErr != nil ||
			!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
			now.Before(info.ModTime()) || now.Sub(info.ModTime()) < minimumAge {
			report.RetainedCacheTemps++
			continue
		}
		if removeErr := os.Remove(candidate); removeErr != nil {
			report.RetainedCacheTemps++
			maintenanceErr = errors.Join(maintenanceErr, errors.New("managed installer cache temporary file could not be removed"))
			continue
		}
		report.RemovedCacheTemps++
		report.RemovedCacheBytes += info.Size()
	}
	return report, maintenanceErr
}

func managedInstallerCacheTempName(name string) bool {
	if len(name) != len("mambaf")+10 || !strings.HasPrefix(name, "mambaf") {
		return false
	}
	for _, value := range name[len("mambaf"):] {
		if (value < 'a' || value > 'z') && (value < '0' || value > '9') {
			return false
		}
	}
	return true
}

// sweepManagedEnvironmentStaging removes only old, incomplete installer
// prefixes. Complete immutable generations and active pointers are outside the
// admitted name shape and are never touched. The per-environment install lock
// makes maintenance mutually exclusive with provisioning, including across
// service processes that share one managed environment root.
func (m *Manager) sweepManagedEnvironmentStaging(
	ctx context.Context,
	now time.Time,
	minimumAge time.Duration,
) (ManagedEnvironmentMaintenanceReport, error) {
	report := ManagedEnvironmentMaintenanceReport{}
	if m == nil {
		return report, errors.New("kernel manager is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if minimumAge < 0 {
		return report, errors.New("managed environment staging retention is invalid")
	}
	root, err := m.managedEnvironmentRoot()
	if err != nil {
		return report, err
	}
	generationsRoot := filepath.Join(root, ".generations")
	environments, err := os.ReadDir(generationsRoot)
	if errors.Is(err, os.ErrNotExist) {
		return report, nil
	}
	if err != nil {
		return report, errors.New("managed environment generation root cannot be inspected")
	}
	var maintenanceErr error
	for _, environmentEntry := range environments {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		name := environmentEntry.Name()
		if !ValidEnvironmentName(name) {
			continue
		}
		generationRoot := filepath.Join(generationsRoot, name)
		rootInfo, err := os.Lstat(generationRoot)
		if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
			continue
		}
		lockCtx, cancelLock := context.WithTimeout(ctx, managedEnvironmentMaintenanceLockTimeout)
		release, err := lockKernelFile(lockCtx, filepath.Join(generationRoot, ".install.lock"))
		cancelLock()
		if err != nil {
			report.RetainedStaging++
			continue
		}
		entries, readErr := os.ReadDir(generationRoot)
		if readErr != nil {
			release()
			maintenanceErr = errors.Join(maintenanceErr, errors.New("managed environment staging root cannot be inspected"))
			continue
		}
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), ".staging-") || len(entry.Name()) <= len(".staging-") {
				continue
			}
			candidate := filepath.Join(generationRoot, entry.Name())
			relative, relErr := filepath.Rel(generationRoot, candidate)
			info, statErr := os.Lstat(candidate)
			if relErr != nil || relative != entry.Name() || statErr != nil ||
				!info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				report.RetainedStaging++
				continue
			}
			if now.Before(info.ModTime()) || now.Sub(info.ModTime()) < minimumAge {
				report.RetainedStaging++
				continue
			}
			if _, markerErr := os.Lstat(filepath.Join(candidate, managedEnvironmentMarkerName)); markerErr == nil ||
				!errors.Is(markerErr, os.ErrNotExist) {
				report.RetainedStaging++
				continue
			}
			if removeErr := os.RemoveAll(candidate); removeErr != nil {
				report.RetainedStaging++
				maintenanceErr = errors.Join(maintenanceErr, errors.New("managed environment staging directory could not be removed"))
				continue
			}
			report.RemovedStaging++
		}
		release()
	}
	if report.RemovedStaging != 0 {
		m.notifyRuntimeChange()
	}
	return report, maintenanceErr
}

func (m *Manager) runManagedEnvironmentOperation(
	waitCtx context.Context,
	key string,
	run func(context.Context) (ManagedEnvironment, error),
) (ManagedEnvironment, error) {
	if m == nil {
		return ManagedEnvironment{}, errors.New("kernel manager is not configured")
	}
	if waitCtx == nil {
		waitCtx = context.Background()
	}
	if !m.ManagedEnvironmentSupervisorEnabled() {
		return ManagedEnvironment{}, errors.New("managed environment supervisor is disabled")
	}
	startupCtx, cancelStartup := context.WithTimeout(waitCtx, managedEnvironmentSupervisorStartupTimeout)
	defer cancelStartup()
	select {
	case <-startupCtx.Done():
		if errors.Is(startupCtx.Err(), context.DeadlineExceeded) {
			return ManagedEnvironment{}, errors.New("managed environment supervisor did not become ready")
		}
		return ManagedEnvironment{}, startupCtx.Err()
	case <-m.managedEnvironmentSupervisorReady:
	}

	m.managedEnvironmentMu.Lock()
	// A cancelled owner retains the slot until its side effects and cleanup
	// have finished. New waiters must not join an already-cancelled operation.
	for {
		previous := m.managedEnvironmentOperations[key]
		if previous == nil || previous.context.Err() == nil {
			break
		}
		m.managedEnvironmentMu.Unlock()
		select {
		case <-waitCtx.Done():
			return ManagedEnvironment{}, waitCtx.Err()
		case <-previous.done:
		}
		m.managedEnvironmentMu.Lock()
	}
	supervisor := m.managedEnvironmentSupervisor
	if supervisor == nil || supervisor.Err() != nil {
		m.managedEnvironmentMu.Unlock()
		return ManagedEnvironment{}, errors.New("managed environment supervisor is unavailable")
	}
	operation := m.managedEnvironmentOperations[key]
	if operation == nil {
		var operationContext context.Context
		var cancelOperation context.CancelFunc
		// The first waiter establishes the operation's hard deadline. The
		// operation may be shared by later waiters, but a later waiter must not
		// extend an installer past the earliest admitted task budget. Without
		// this deadline the shared micromamba process can outlive the caller
		// after its wait context expires, leaving a lock and a visible tool step
		// running until service restart.
		if deadline, ok := waitCtx.Deadline(); ok {
			operationContext, cancelOperation = context.WithDeadline(supervisor, deadline)
		} else {
			operationContext, cancelOperation = context.WithCancel(supervisor)
		}
		operation = &managedEnvironmentOperation{
			done: make(chan struct{}), cancel: cancelOperation, context: operationContext,
			progressSubscribers: map[int]toolprogress.Reporter{},
		}
		operationContext = toolprogress.WithReporter(operationContext, operation.publishProgress)
		m.managedEnvironmentOperations[key] = operation
		go func(active *managedEnvironmentOperation, operationContext context.Context) {
			defer active.cancel()
			result, err := run(operationContext)
			m.managedEnvironmentMu.Lock()
			active.result = result
			active.err = err
			close(active.done)
			delete(m.managedEnvironmentOperations, key)
			m.managedEnvironmentMu.Unlock()
			m.notifyRuntimeChange()
		}(operation, operationContext)
	}
	operation.waiters++
	progressReporter := toolprogress.ReporterFrom(waitCtx)
	progressSubscription, latestProgress := operation.subscribeProgress(progressReporter)
	m.managedEnvironmentMu.Unlock()
	if latestProgress != nil && progressReporter != nil {
		progressReporter(*latestProgress)
	}

	select {
	case <-waitCtx.Done():
		operation.unsubscribeProgress(progressSubscription)
		if m.releaseManagedEnvironmentOperationWaiter(key, operation, true) {
			// The last owner cannot advertise terminal cancellation while the
			// installer is still draining or holds the publication lock.
			<-operation.done
		}
		return ManagedEnvironment{}, waitCtx.Err()
	case <-operation.done:
		operation.unsubscribeProgress(progressSubscription)
		m.releaseManagedEnvironmentOperationWaiter(key, operation, false)
		m.managedEnvironmentMu.Lock()
		result, err := operation.result, operation.err
		m.managedEnvironmentMu.Unlock()
		return result, err
	}
}

func (m *Manager) releaseManagedEnvironmentOperationWaiter(
	key string,
	operation *managedEnvironmentOperation,
	cancelled bool,
) bool {
	if m == nil || operation == nil {
		return false
	}
	m.managedEnvironmentMu.Lock()
	active := m.managedEnvironmentOperations[key]
	if active != operation {
		m.managedEnvironmentMu.Unlock()
		return false
	}
	if operation.waiters > 0 {
		operation.waiters--
	}
	shouldCancel := cancelled && operation.waiters == 0 && operation.cancel != nil
	if shouldCancel {
		operation.cancel()
	}
	m.managedEnvironmentMu.Unlock()
	return shouldCancel
}
