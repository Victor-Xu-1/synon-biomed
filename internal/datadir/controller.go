package datadir

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"

	runtimecontrol "synon-go/internal/runtimecontrol"
)

const (
	stateSchemaVersion             = 1
	sourceMarkerPrefix             = ".synon-data-move-source-"
	completionMarker               = ".synon-data-move-complete"
	migrationFreeSpaceReserveBytes = uint64(512 * 1024 * 1024)
)

var (
	ErrChangePending    = errors.New("a data directory change is already pending")
	ErrActivationActive = errors.New("cannot change the data directory while a prepared activation is active")
	ErrTargetNotEmpty   = errors.New("new location must be empty")
)

type StagePolicy struct {
	ProtectedDirectories []string
}

type InsufficientSpaceError struct {
	RequiredBytes  uint64
	AvailableBytes uint64
}

func (e *InsufficientSpaceError) Error() string {
	return fmt.Sprintf(
		"not enough free space for migration: need %d bytes including reserve, have %d bytes",
		e.RequiredBytes, e.AvailableBytes,
	)
}

type Move struct {
	ID                string     `json:"id"`
	Source            string     `json:"source"`
	Target            string     `json:"target"`
	Migrate           bool       `json:"migrate"`
	Copied            bool       `json:"copied"`
	Entries           []string   `json:"entries,omitempty"`
	InventoryCaptured bool       `json:"inventoryCaptured,omitempty"`
	StagedAt          time.Time  `json:"stagedAt"`
	CompletedAt       *time.Time `json:"completedAt,omitempty"`
}

type State struct {
	SchemaVersion int         `json:"schemaVersion"`
	Current       string      `json:"current,omitempty"`
	Pending       *Move       `json:"pending,omitempty"`
	LastMove      *Move       `json:"lastMove,omitempty"`
	Activation    *Activation `json:"activation,omitempty"`
}

// Activation records a pointer-only switch to an already prepared runtime
// directory. It is separate from Move because no source files are copied.
type Activation struct {
	ID              string    `json:"id"`
	ActivationKey   string    `json:"activationKey"`
	Target          string    `json:"target"`
	PreviousCurrent string    `json:"previousCurrent,omitempty"`
	ActivatedAt     time.Time `json:"activatedAt"`
}

type Controller struct {
	mu   sync.Mutex
	path string
	now  func() time.Time
}

func New(controlPath string) *Controller {
	return &Controller{path: filepath.Clean(controlPath), now: time.Now}
}

func (c *Controller) Path() string {
	return c.path
}

func (c *Controller) Load() (State, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state, err := c.loadLocked()
	if err != nil {
		return State{}, err
	}
	return cloneState(state), nil
}

// ActivateExisting atomically points future runtime starts at a prepared data
// directory while retaining enough state to restore the previous pointer.
func (c *Controller) ActivateExisting(target, activationKey string) (Activation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	target, err := canonicalExistingDirectory(target)
	if err != nil {
		return Activation{}, fmt.Errorf("activation target: %w", err)
	}
	activationKey = strings.TrimSpace(activationKey)
	if activationKey == "" {
		return Activation{}, errors.New("activation key is required")
	}
	state, err := c.loadLocked()
	if err != nil {
		return Activation{}, err
	}
	if state.Pending != nil {
		return Activation{}, errors.New("cannot activate a prepared directory while a data move is pending")
	}
	if state.Activation != nil {
		if state.Activation.Target == target && state.Activation.ActivationKey == activationKey && state.Current == target {
			return *state.Activation, nil
		}
		return Activation{}, errors.New("a prepared data directory activation is already active")
	}
	activation := Activation{
		ID: uuid.NewString(), ActivationKey: activationKey, Target: target,
		PreviousCurrent: state.Current, ActivatedAt: c.now().UTC(),
	}
	state.Current = target
	state.Activation = &activation
	if err := c.saveLocked(state); err != nil {
		return Activation{}, err
	}
	return activation, nil
}

// RollbackActivation restores the pointer that was active before
// ActivateExisting. The prepared directory and its migrated data are retained.
func (c *Controller) RollbackActivation(id string) (Activation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state, err := c.loadLocked()
	if err != nil {
		return Activation{}, err
	}
	if state.Activation == nil {
		return Activation{}, errors.New("no prepared data directory activation is active")
	}
	activation := *state.Activation
	if strings.TrimSpace(id) == "" || activation.ID != strings.TrimSpace(id) {
		return Activation{}, errors.New("activation id does not match active pointer")
	}
	if state.Current != activation.Target {
		return Activation{}, errors.New("active data directory pointer changed after activation")
	}
	if activation.PreviousCurrent != "" {
		previous, err := canonicalExistingDirectory(activation.PreviousCurrent)
		if err != nil || previous != activation.PreviousCurrent {
			return Activation{}, errors.New("previous data directory is no longer available")
		}
	}
	state.Current = activation.PreviousCurrent
	state.Activation = nil
	if err := c.saveLocked(state); err != nil {
		return Activation{}, err
	}
	return activation, nil
}

func (c *Controller) Stage(source, target string, migrate bool) (Move, error) {
	return c.StageWithPolicy(source, target, migrate, StagePolicy{})
}

func (c *Controller) StageWithPolicy(source, target string, migrate bool, policy StagePolicy) (Move, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	source, err := canonicalExistingDirectory(source)
	if err != nil {
		return Move{}, fmt.Errorf("source data directory: %w", err)
	}
	target, targetCreated, err := prepareTargetDirectory(target)
	if err != nil {
		return Move{}, fmt.Errorf("target data directory: %w", err)
	}
	cleanupTarget := func() {
		if targetCreated {
			_ = os.Remove(target)
		}
	}
	if pathsOverlap(source, target) {
		cleanupTarget()
		return Move{}, errors.New("new location must not overlap the current data directory")
	}
	protectedDirectories := append([]string{filepath.Dir(c.path)}, policy.ProtectedDirectories...)
	for _, protected := range protectedDirectories {
		protected, err = canonicalPolicyDirectory(protected)
		if err != nil {
			cleanupTarget()
			return Move{}, fmt.Errorf("protected directory: %w", err)
		}
		if pathsOverlap(target, protected) {
			cleanupTarget()
			return Move{}, fmt.Errorf("new location must not overlap protected directory %s", protected)
		}
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		cleanupTarget()
		return Move{}, err
	}
	if len(entries) != 0 {
		cleanupTarget()
		return Move{}, ErrTargetNotEmpty
	}
	if err := writableProbe(target); err != nil {
		cleanupTarget()
		return Move{}, fmt.Errorf("new location is not writable: %w", err)
	}
	state, err := c.loadLocked()
	if err != nil {
		cleanupTarget()
		return Move{}, err
	}
	if state.Pending != nil {
		cleanupTarget()
		return Move{}, ErrChangePending
	}
	if state.Activation != nil {
		cleanupTarget()
		return Move{}, ErrActivationActive
	}
	if migrate {
		sourceBytes, _, err := runtimecontrol.PathUsage(source)
		if err != nil {
			cleanupTarget()
			return Move{}, fmt.Errorf("measure source data directory: %w", err)
		}
		_, available, err := runtimecontrol.PathUsage(target)
		if err != nil {
			cleanupTarget()
			return Move{}, fmt.Errorf("measure target data directory: %w", err)
		}
		if available == nil {
			cleanupTarget()
			return Move{}, errors.New("target free space is unavailable")
		}
		required := uint64(sourceBytes)
		if ^uint64(0)-required < migrationFreeSpaceReserveBytes {
			cleanupTarget()
			return Move{}, errors.New("source data directory size overflows migration capacity")
		}
		required += migrationFreeSpaceReserveBytes
		if *available < required {
			cleanupTarget()
			return Move{}, &InsufficientSpaceError{RequiredBytes: required, AvailableBytes: *available}
		}
	}
	move := Move{
		ID: uuid.NewString(), Source: source, Target: target, Migrate: migrate,
		StagedAt: c.now().UTC(),
	}
	marker := ""
	if migrate {
		move.Entries, err = migrationSourceEntries(source)
		if err != nil {
			cleanupTarget()
			return Move{}, fmt.Errorf("inventory source data directory: %w", err)
		}
		move.InventoryCaptured = true
		marker = sourceMarkerPath(source, move.ID)
		if err := writeExclusiveSynced(marker, []byte(move.ID), 0o600); err != nil {
			cleanupTarget()
			return Move{}, fmt.Errorf("write source migration marker: %w", err)
		}
	}
	state.Pending = &move
	if err := c.saveLocked(state); err != nil {
		if marker != "" {
			_ = os.Remove(marker)
		}
		cleanupTarget()
		return Move{}, err
	}
	return move, nil
}

func (c *Controller) CancelPending(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	state, err := c.loadLocked()
	if err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if state.Pending == nil || id == "" || state.Pending.ID != id {
		return errors.New("pending data directory change does not match")
	}
	move := *state.Pending
	state.Pending = nil
	if err := c.saveLocked(state); err != nil {
		return err
	}
	if move.Migrate {
		marker := sourceMarkerPath(move.Source, move.ID)
		if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove cancelled migration marker: %w", err)
		}
	}
	return nil
}

func (c *Controller) Resolve(defaultDirectory string) (string, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state, err := c.loadLocked()
	if err != nil {
		return "", "", err
	}
	if state.Pending != nil {
		if err := c.applyPendingLocked(&state); err != nil {
			return "", "", err
		}
	}
	if strings.TrimSpace(state.Current) != "" {
		current, err := canonicalExistingDirectory(state.Current)
		if err != nil {
			return "", "", fmt.Errorf("configured data directory: %w", err)
		}
		return current, "pointer", nil
	}
	current, err := canonicalOrCreateDirectory(defaultDirectory)
	if err != nil {
		return "", "", err
	}
	return current, "default", nil
}

func (c *Controller) ClearLastMove(deleteSource bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	state, err := c.loadLocked()
	if err != nil {
		return err
	}
	if state.LastMove == nil {
		return nil
	}
	move := *state.LastMove
	target, err := canonicalExistingDirectory(state.Current)
	if err != nil {
		return fmt.Errorf("current data directory: %w", err)
	}
	if target != move.Target {
		return errors.New("last move belongs to a different current data directory")
	}
	source, err := canonicalExistingDirectory(move.Source)
	if err != nil {
		return fmt.Errorf("previous data directory: %w", err)
	}
	if pathsOverlap(source, target) {
		return errors.New("previous location overlaps the current data directory")
	}
	marker := sourceMarkerPath(source, move.ID)
	markerValue, err := readRegularFile(marker)
	if err != nil || string(markerValue) != move.ID {
		return errors.New("previous location does not contain the matching migration marker")
	}
	if deleteSource {
		if !move.InventoryCaptured {
			return errors.New("last move has no cleanup inventory; source deletion requires manual review")
		}
		for _, name := range move.Entries {
			if err := removeInventoriedEntry(source, name); err != nil {
				return err
			}
		}
	}
	if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_ = os.Remove(filepath.Join(target, completionMarker))
	state.LastMove = nil
	return c.saveLocked(state)
}

func (c *Controller) applyPendingLocked(state *State) error {
	move := state.Pending
	if move == nil {
		return nil
	}
	if !move.Migrate {
		target, err := canonicalExistingDirectory(move.Target)
		if err != nil || target != move.Target {
			return errors.New("pending data directory target changed")
		}
		state.Current = target
		state.Pending = nil
		state.LastMove = nil
		return c.saveLocked(*state)
	}
	source, err := canonicalExistingDirectory(move.Source)
	if err != nil {
		return fmt.Errorf("pending migration source: %w", err)
	}
	if source != move.Source {
		return errors.New("pending migration source changed")
	}
	if pathsOverlap(move.Source, move.Target) {
		return errors.New("pending migration paths overlap")
	}
	markerValue, err := readRegularFile(sourceMarkerPath(source, move.ID))
	if err != nil || string(markerValue) != move.ID {
		return errors.New("pending migration source marker is missing or invalid")
	}
	if completedMigration(move.Target, move.ID) {
		return c.finalizeMoveLocked(state, move)
	}
	targetExists, err := emptyDirectoryOrMissing(move.Target)
	if err != nil {
		return err
	}
	staging := move.Target + ".synon-migrate-" + move.ID
	if _, err := os.Lstat(staging); err == nil {
		return errors.New("migration staging directory already exists; manual inspection is required")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(move.Target), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(staging, 0o700); err != nil {
		return fmt.Errorf("create migration staging directory: %w", err)
	}
	stagingComplete := false
	defer func() {
		if !stagingComplete {
			_ = os.RemoveAll(staging)
		}
	}()
	if err := copyTree(source, staging, sourceMarkerPath(source, move.ID)); err != nil {
		return err
	}
	if err := writeExclusiveSynced(filepath.Join(staging, completionMarker), []byte(move.ID), 0o600); err != nil {
		return err
	}
	if err := syncDirectory(staging); err != nil {
		return err
	}
	if targetExists {
		if err := os.Remove(move.Target); err != nil {
			return fmt.Errorf("remove empty target directory: %w", err)
		}
	}
	if err := os.Rename(staging, move.Target); err != nil {
		return fmt.Errorf("commit migrated data directory: %w", err)
	}
	stagingComplete = true
	if err := syncDirectory(filepath.Dir(move.Target)); err != nil {
		return err
	}
	return c.finalizeMoveLocked(state, move)
}

func (c *Controller) finalizeMoveLocked(state *State, move *Move) error {
	completed := c.now().UTC()
	lastMove := *move
	lastMove.Copied = true
	lastMove.CompletedAt = &completed
	state.Current = move.Target
	state.Pending = nil
	state.LastMove = &lastMove
	return c.saveLocked(*state)
}

func (c *Controller) loadLocked() (State, error) {
	if strings.TrimSpace(c.path) == "" || !filepath.IsAbs(c.path) {
		return State{}, errors.New("data directory control path must be absolute")
	}
	raw, err := readRegularFile(c.path)
	if errors.Is(err, os.ErrNotExist) {
		return State{SchemaVersion: stateSchemaVersion}, nil
	}
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return State{}, fmt.Errorf("decode data directory state: %w", err)
	}
	if state.SchemaVersion != stateSchemaVersion {
		return State{}, fmt.Errorf("unsupported data directory state schema %d", state.SchemaVersion)
	}
	return state, nil
}

func (c *Controller) saveLocked(state State) error {
	state.SchemaVersion = stateSchemaVersion
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	_ = os.Chmod(filepath.Dir(c.path), 0o700)
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + "." + uuid.NewString() + ".tmp"
	if err := writeExclusiveSynced(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.Chmod(c.path, 0o600)
	return syncDirectory(filepath.Dir(c.path))
}

func canonicalExistingDirectory(path string) (string, error) {
	absolute, err := cleanAbsolutePath(path)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("path is not a real directory")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	if resolved != absolute {
		return "", errors.New("directory path contains symbolic links")
	}
	return absolute, nil
}

func canonicalOrCreateDirectory(path string) (string, error) {
	absolute, err := cleanAbsolutePath(path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return "", err
	}
	return canonicalExistingDirectory(absolute)
}

func prepareTargetDirectory(path string) (string, bool, error) {
	absolute, err := cleanAbsolutePath(path)
	if err != nil {
		return "", false, err
	}
	if info, err := os.Lstat(absolute); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", false, errors.New("target is not a real directory")
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil || resolved != absolute {
			return "", false, errors.New("target path contains symbolic links")
		}
		return absolute, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	parent, err := canonicalOrCreateDirectory(filepath.Dir(absolute))
	if err != nil {
		return "", false, fmt.Errorf("target parent: %w", err)
	}
	if parent != filepath.Dir(absolute) {
		return "", false, errors.New("target parent changed during validation")
	}
	if err := os.Mkdir(absolute, 0o700); err != nil {
		return "", false, err
	}
	return absolute, true, nil
}

func cleanAbsolutePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return "", errors.New("path must be absolute")
	}
	for _, character := range path {
		if unicode.IsControl(character) {
			return "", errors.New("path contains control characters")
		}
	}
	return filepath.Clean(path), nil
}

func pathsOverlap(first, second string) bool {
	return pathContains(first, second) || pathContains(second, first)
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative))
}

func writableProbe(directory string) error {
	file, err := os.CreateTemp(directory, ".synon-write-probe-")
	if err != nil {
		return err
	}
	name := file.Name()
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Remove(name)
}

func emptyDirectoryOrMissing(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		parent := filepath.Dir(path)
		resolvedParent, parentErr := canonicalExistingDirectory(parent)
		if parentErr != nil || resolvedParent != parent {
			return false, errors.New("migration target parent changed after staging")
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("migration target is not a real directory")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return false, errors.New("migration target path contains symbolic links")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	if len(entries) != 0 {
		return false, errors.New("migration target is not empty")
	}
	return true, nil
}

func canonicalPolicyDirectory(path string) (string, error) {
	absolute, err := cleanAbsolutePath(path)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(absolute); err == nil {
		if !info.IsDir() {
			return "", errors.New("path is not a real directory")
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return "", err
		}
		return resolved, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return resolveProspectivePath(absolute)
}

func resolveProspectivePath(path string) (string, error) {
	current := path
	missing := []string{}
	for {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return filepath.Clean(resolved), nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return path, nil
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func completedMigration(target, id string) bool {
	raw, err := readRegularFile(filepath.Join(target, completionMarker))
	return err == nil && string(raw) == id
}

func copyTree(source, target, excludedPath string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == excludedPath {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("migration refuses symbolic link %s", relative)
		}
		destination := filepath.Join(target, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Mkdir(destination, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("migration refuses non-regular entry %s", relative)
		}
		return copyRegularFile(path, destination, info)
	})
}

func copyRegularFile(source, target string, sourceInfo fs.FileInfo) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	openedInfo, err := input.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(sourceInfo, openedInfo) || !openedInfo.Mode().IsRegular() {
		return errors.New("source file changed during migration")
	}
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, sourceInfo.Mode().Perm())
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = output.Close()
		if !ok {
			_ = os.Remove(target)
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	if err := os.Chtimes(target, sourceInfo.ModTime(), sourceInfo.ModTime()); err != nil {
		return err
	}
	ok = true
	return nil
}

func sourceMarkerPath(source, id string) string {
	return filepath.Join(source, sourceMarkerPrefix+id)
}

func migrationSourceEntries(source string) ([]string, error) {
	entries, err := os.ReadDir(source)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), sourceMarkerPrefix) {
			return nil, fmt.Errorf("stale migration marker %s requires manual review", entry.Name())
		}
		names = append(names, entry.Name())
	}
	return names, nil
}

func removeInventoriedEntry(source, name string) error {
	if name == "" || filepath.Base(name) != name || name == "." || name == ".." || strings.ContainsAny(name, `/\\`) {
		return fmt.Errorf("invalid cleanup inventory entry %q", name)
	}
	path := filepath.Join(source, name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect previous data entry %s: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("previous data entry %s was replaced by a symbolic link", name)
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove previous data entry %s: %w", name, err)
	}
	return nil
}

func writeExclusiveSynced(path string, value []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(value); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func readRegularFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	return os.ReadFile(path)
}

func cloneState(state State) State {
	if state.Pending != nil {
		pending := *state.Pending
		pending.Entries = append([]string(nil), state.Pending.Entries...)
		state.Pending = &pending
	}
	if state.LastMove != nil {
		lastMove := *state.LastMove
		lastMove.Entries = append([]string(nil), state.LastMove.Entries...)
		state.LastMove = &lastMove
	}
	if state.Activation != nil {
		activation := *state.Activation
		state.Activation = &activation
	}
	return state
}
