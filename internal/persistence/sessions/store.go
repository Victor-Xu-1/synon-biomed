package sessions

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Session struct {
	ID                string         `json:"id"`
	Title             string         `json:"title"`
	WorkDir           string         `json:"workDir"`
	CreatedAt         time.Time      `json:"createdAt"`
	UpdatedAt         time.Time      `json:"updatedAt"`
	LastUserMessageAt time.Time      `json:"lastUserMessageAt,omitempty"`
	MessageCount      int            `json:"messageCount"`
	LastRole          string         `json:"lastRole,omitempty"`
	Runner            *Runner        `json:"runner,omitempty"`
	Project           *Project       `json:"project,omitempty"`
	Orchestration     map[string]any `json:"orchestration,omitempty"`
}

type Runner struct {
	RunnerID              string    `json:"runnerId"`
	Status                string    `json:"status"`
	Attempt               int       `json:"attempt,omitempty"`
	PreviousRunnerID      string    `json:"previousRunnerId,omitempty"`
	ReclaimedExpiredLease bool      `json:"reclaimedExpiredLease,omitempty"`
	ClaimedAt             time.Time `json:"claimedAt"`
	LastHeartbeatAt       time.Time `json:"lastHeartbeatAt"`
	ExpiresAt             time.Time `json:"expiresAt"`
	LastCheckpoint        string    `json:"lastCheckpoint,omitempty"`
	LastCheckpointAt      time.Time `json:"lastCheckpointAt,omitempty"`
	LastCheckpointEventID int64     `json:"lastCheckpointEventId,omitempty"`
}

var (
	ErrRunnerClaimStale       = errors.New("runner claim is stale")
	ErrRunnerFinalizeConflict = errors.New("runner finalization conflicts with durable state")
)

type FinalizeRunnerInput struct {
	SessionID                   string
	RunnerID                    string
	Attempt                     int
	Status                      string
	Checkpoint                  string
	CheckpointAt                time.Time
	EventID                     int64
	FinishedAt                  time.Time
	SupersedesCheckpointEventID int64
}

// RunnerMutationClaim is a fencing credential for one durable runner lease.
// ClaimToken is derived from persisted claim state; it is not authentication.
type RunnerMutationClaim struct {
	SessionID  string
	RunnerID   string
	Attempt    int
	ClaimToken string
}

type CheckpointRunnerInput struct {
	Claim      RunnerMutationClaim
	Status     string
	Checkpoint string
	EventID    int64
	RecordedAt time.Time
}

type Project struct {
	ID      string    `json:"id"`
	Name    string    `json:"name,omitempty"`
	Path    string    `json:"path"`
	BoundAt time.Time `json:"boundAt"`
}

type RunnerQueueSnapshot struct {
	TotalSessions           int      `json:"totalSessions"`
	Pending                 int      `json:"pending"`
	Running                 int      `json:"running"`
	Expired                 int      `json:"expired"`
	Terminal                int      `json:"terminal"`
	OldestPending           *Session `json:"oldestPending,omitempty"`
	OldestPendingAgeSeconds int64    `json:"oldestPendingAgeSeconds,omitempty"`
}

type RunnerBacklogOptions struct {
	Limit           int    `json:"limit,omitempty"`
	IncludeRunning  bool   `json:"includeRunning,omitempty"`
	IncludeTerminal bool   `json:"includeTerminal,omitempty"`
	ProjectID       string `json:"projectId,omitempty"`
	State           string `json:"state,omitempty"`
}

type RunnerBacklog struct {
	TotalSessions int                 `json:"totalSessions"`
	Pending       int                 `json:"pending"`
	Running       int                 `json:"running"`
	Expired       int                 `json:"expired"`
	Terminal      int                 `json:"terminal"`
	Returned      int                 `json:"returned"`
	Truncated     bool                `json:"truncated"`
	Items         []RunnerBacklogItem `json:"items"`
}

type RunnerBacklogItem struct {
	SessionID             string         `json:"sessionId"`
	Title                 string         `json:"title,omitempty"`
	State                 string         `json:"state"`
	Priority              int            `json:"priority"`
	NextAction            string         `json:"nextAction"`
	AgeSeconds            int64          `json:"ageSeconds,omitempty"`
	WaitingSince          time.Time      `json:"waitingSince,omitempty"`
	UpdatedAt             time.Time      `json:"updatedAt"`
	LastUserMessageAt     time.Time      `json:"lastUserMessageAt,omitempty"`
	MessageCount          int            `json:"messageCount"`
	WorkDir               string         `json:"workDir,omitempty"`
	Project               *Project       `json:"project,omitempty"`
	Orchestration         map[string]any `json:"orchestration,omitempty"`
	RunnerID              string         `json:"runnerId,omitempty"`
	RunnerStatus          string         `json:"runnerStatus,omitempty"`
	Attempt               int            `json:"attempt,omitempty"`
	ClaimedAt             time.Time      `json:"claimedAt,omitempty"`
	LastHeartbeatAt       time.Time      `json:"lastHeartbeatAt,omitempty"`
	ExpiresAt             time.Time      `json:"expiresAt,omitempty"`
	LastCheckpoint        string         `json:"lastCheckpoint,omitempty"`
	LastCheckpointAt      time.Time      `json:"lastCheckpointAt,omitempty"`
	LeaseAgeSeconds       int64          `json:"leaseAgeSeconds,omitempty"`
	LeaseExpiredSeconds   int64          `json:"leaseExpiredSeconds,omitempty"`
	LeaseRemainingSeconds int64          `json:"leaseRemainingSeconds,omitempty"`
}

type Store struct {
	root string
	path string
	mu   sync.Mutex
	now  func() time.Time
}

func NewStore(root string) *Store {
	return &Store{
		root: root,
		path: filepath.Join(root, "sessions", "index.json"),
		now:  time.Now,
	}
}

func (s *Store) Upsert(session Session) error {
	if session.ID == "" {
		return errors.New("session id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	sessions, err := s.readLocked()
	if err != nil {
		return err
	}
	now := s.now().UTC()
	existing := sessions[session.ID]
	if session.CreatedAt.IsZero() {
		if !existing.CreatedAt.IsZero() {
			session.CreatedAt = existing.CreatedAt
		} else {
			session.CreatedAt = now
		}
	}
	if session.UpdatedAt.IsZero() {
		session.UpdatedAt = now
	}
	if session.LastUserMessageAt.IsZero() {
		if !existing.LastUserMessageAt.IsZero() {
			session.LastUserMessageAt = existing.LastUserMessageAt
		} else if session.LastRole == "user" {
			session.LastUserMessageAt = session.UpdatedAt
		}
	}
	if session.Title == "" {
		session.Title = existing.Title
	}
	if session.WorkDir == "" {
		session.WorkDir = existing.WorkDir
	}
	if session.MessageCount == 0 {
		session.MessageCount = existing.MessageCount
	}
	if session.LastRole == "" {
		session.LastRole = existing.LastRole
	}
	if session.Runner == nil {
		session.Runner = existing.Runner
	}
	if session.Project == nil {
		session.Project = existing.Project
	}
	if len(session.Orchestration) == 0 {
		session.Orchestration = existing.Orchestration
	}
	sessions[session.ID] = session
	return s.writeLocked(sessions)
}

func (s *Store) Save(session Session) error {
	if session.ID == "" {
		return errors.New("session id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	sessions, err := s.readLocked()
	if err != nil {
		return err
	}
	now := s.now().UTC()
	existing := sessions[session.ID]
	if session.CreatedAt.IsZero() {
		if !existing.CreatedAt.IsZero() {
			session.CreatedAt = existing.CreatedAt
		} else {
			session.CreatedAt = now
		}
	}
	if session.UpdatedAt.IsZero() {
		session.UpdatedAt = now
	}
	sessions[session.ID] = session
	return s.writeLocked(sessions)
}

// SaveProjectionPreservingRunner updates compatibility session metadata while
// atomically retaining the latest durable runner authority.
func (s *Store) SaveProjectionPreservingRunner(session Session) (Session, error) {
	if session.ID == "" {
		return Session{}, errors.New("session id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sessions, err := s.readLocked()
	if err != nil {
		return Session{}, err
	}
	now := s.now().UTC()
	existing := sessions[session.ID]
	if session.CreatedAt.IsZero() {
		if !existing.CreatedAt.IsZero() {
			session.CreatedAt = existing.CreatedAt
		} else {
			session.CreatedAt = now
		}
	}
	if session.UpdatedAt.IsZero() {
		session.UpdatedAt = now
	}
	session.Runner = existing.Runner
	sessions[session.ID] = session
	if err := s.writeLocked(sessions); err != nil {
		return Session{}, err
	}
	return session, nil
}

// FinalizeRunner advances only the exact durable runner attempt that produced EventID.
func (s *Store) FinalizeRunner(input FinalizeRunnerInput) (Session, bool, error) {
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.RunnerID = strings.TrimSpace(input.RunnerID)
	input.Status = strings.TrimSpace(input.Status)
	if input.SessionID == "" || input.RunnerID == "" {
		return Session{}, false, errors.New("session id and runner id are required")
	}
	if input.Attempt <= 0 || input.EventID <= 0 {
		return Session{}, false, errors.New("runner attempt and event id must be positive")
	}
	if !isTerminalRunnerStatus(input.Status) {
		return Session{}, false, errors.New("runner final status must be terminal")
	}
	now := s.now().UTC()
	finishedAt := input.FinishedAt.UTC()
	if finishedAt.IsZero() {
		finishedAt = now
	} else if finishedAt.After(now) {
		return Session{}, false, errors.New("runner finished time cannot be in the future")
	}
	checkpointAt := input.CheckpointAt.UTC()
	if checkpointAt.IsZero() {
		checkpointAt = finishedAt
	} else if checkpointAt.After(finishedAt) {
		return Session{}, false, errors.New("runner checkpoint time cannot be after finished time")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	sessions, err := s.readLocked()
	if err != nil {
		return Session{}, false, err
	}
	session, found := sessions[input.SessionID]
	if !found {
		return Session{}, false, errors.New("session not found")
	}
	if session.Runner == nil || session.Runner.RunnerID != input.RunnerID || session.Runner.Attempt != input.Attempt {
		return session, false, ErrRunnerClaimStale
	}
	runner := session.Runner
	if finishedAt.Before(runner.ClaimedAt) || finishedAt.Before(runner.LastHeartbeatAt) ||
		checkpointAt.Before(runner.ClaimedAt) || checkpointAt.Before(runner.LastHeartbeatAt) {
		return session, false, errors.New("runner finalization predates the active claim")
	}
	if isTerminalRunnerStatus(runner.Status) {
		if runner.Status == input.Status && runner.LastCheckpointEventID == input.EventID && runner.LastCheckpoint == input.Checkpoint {
			return session, false, nil
		}
		if input.SupersedesCheckpointEventID <= 0 || runner.LastCheckpointEventID != input.SupersedesCheckpointEventID {
			return session, false, ErrRunnerFinalizeConflict
		}
	}
	if !runner.ExpiresAt.After(finishedAt) {
		return session, false, ErrRunnerClaimStale
	}
	if !isTerminalRunnerStatus(runner.Status) && runner.Status != "running" && runner.Status != "waiting" && runner.Status != "blocked" {
		return session, false, ErrRunnerFinalizeConflict
	}
	runner.Status = input.Status
	runner.LastCheckpoint = input.Checkpoint
	runner.LastCheckpointAt = checkpointAt
	runner.LastCheckpointEventID = input.EventID
	runner.ExpiresAt = finishedAt
	session.UpdatedAt = finishedAt
	sessions[input.SessionID] = session
	if err := s.writeLocked(sessions); err != nil {
		return Session{}, false, err
	}
	return session, true, nil
}

func (s *Store) Get(id string) (Session, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sessions, err := s.readLocked()
	if err != nil {
		return Session{}, false, err
	}
	session, ok := sessions[id]
	return session, ok, nil
}

func (s *Store) Delete(id string) (bool, error) {
	if id == "" {
		return false, errors.New("session id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	sessions, err := s.readLocked()
	if err != nil {
		return false, err
	}
	if _, found := sessions[id]; !found {
		return false, nil
	}
	delete(sessions, id)
	if err := s.writeLocked(sessions); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) List() ([]Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sessions, err := s.readLocked()
	if err != nil {
		return nil, err
	}
	list := make([]Session, 0, len(sessions))
	for _, session := range sessions {
		list = append(list, session)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].UpdatedAt.After(list[j].UpdatedAt)
	})
	return list, nil
}

func (s *Store) AppendMessage(sessionID string, role string) error {
	if sessionID == "" {
		return errors.New("session id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	sessions, err := s.readLocked()
	if err != nil {
		return err
	}
	session, ok := sessions[sessionID]
	if !ok {
		return errors.New("session not found")
	}
	now := s.now().UTC()
	if role == "user" && session.Runner != nil && isTerminalRunnerStatus(session.Runner.Status) && !session.Runner.ExpiresAt.After(now) {
		session.Runner = nil
	}
	session.MessageCount++
	session.LastRole = role
	if role == "user" {
		session.LastUserMessageAt = now
	}
	if now.After(session.UpdatedAt) {
		session.UpdatedAt = now
	}
	sessions[sessionID] = session
	return s.writeLocked(sessions)
}

// ReconcileMessageStats advances the legacy Session read model to a complete
// journal snapshot. A newer concurrent projection always wins.
func (s *Store) ReconcileMessageStats(sessionID string, messageCount int, lastRole string, lastUserMessageAt time.Time) error {
	if sessionID == "" {
		return errors.New("session id is required")
	}
	if messageCount < 0 {
		return errors.New("message count must be non-negative")
	}
	lastRole = strings.TrimSpace(lastRole)
	s.mu.Lock()
	defer s.mu.Unlock()

	sessions, err := s.readLocked()
	if err != nil {
		return err
	}
	session, ok := sessions[sessionID]
	if !ok {
		return errors.New("session not found")
	}
	if session.MessageCount > messageCount {
		return nil
	}
	now := s.now().UTC()
	if messageCount > session.MessageCount && lastRole == "user" && session.Runner != nil &&
		isTerminalRunnerStatus(session.Runner.Status) && !session.Runner.ExpiresAt.After(now) {
		session.Runner = nil
	}
	session.MessageCount = messageCount
	session.LastRole = lastRole
	if lastUserMessageAt.After(session.LastUserMessageAt) || messageCount == 0 {
		session.LastUserMessageAt = lastUserMessageAt.UTC()
	}
	if now.After(session.UpdatedAt) {
		session.UpdatedAt = now
	}
	sessions[sessionID] = session
	return s.writeLocked(sessions)
}

func (s *Store) ClaimRunner(sessionID string, runnerID string, ttl time.Duration) (Session, bool, error) {
	if sessionID == "" {
		return Session{}, false, errors.New("session id is required")
	}
	if runnerID == "" {
		return Session{}, false, errors.New("runner id is required")
	}
	if ttl <= 0 {
		return Session{}, false, errors.New("runner ttl must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	sessions, err := s.readLocked()
	if err != nil {
		return Session{}, false, err
	}
	session, ok := sessions[sessionID]
	if !ok {
		return Session{}, false, errors.New("session not found")
	}
	now := s.now().UTC()
	if isLiveRunner(session.Runner, now) {
		return session, false, nil
	}
	session = claimSessionRunner(session, runnerID, ttl, now)
	sessions[sessionID] = session
	if err := s.writeLocked(sessions); err != nil {
		return Session{}, false, err
	}
	return session, true, nil
}

func (s *Store) HeartbeatRunner(claim RunnerMutationClaim, ttl time.Duration) (Session, bool, error) {
	claim = normalizeRunnerMutationClaim(claim)
	if err := validateRunnerMutationClaimInput(claim); err != nil {
		return Session{}, false, err
	}
	if ttl <= 0 {
		return Session{}, false, errors.New("runner ttl must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	sessions, err := s.readLocked()
	if err != nil {
		return Session{}, false, err
	}
	session, ok := sessions[claim.SessionID]
	if !ok {
		return Session{}, false, errors.New("session not found")
	}
	now := s.now().UTC()
	// The fencing identity, rather than the wall-clock deadline alone, decides
	// whether this worker still owns the session. A delayed heartbeat may renew
	// an expired lease when nobody has reclaimed it; once another worker claims
	// the session the attempt/token changes and the stale owner is rejected.
	if err := validateRunnerMutationClaim(session, claim, now, false); err != nil {
		return session, false, err
	}
	if isTerminalRunnerStatus(session.Runner.Status) {
		return session, false, ErrRunnerClaimStale
	}
	if session.Runner.Attempt == 0 {
		session.Runner.Attempt = 1
	}
	session.Runner.Status = "running"
	session.Runner.LastHeartbeatAt = now
	session.Runner.ExpiresAt = now.Add(ttl)
	session.UpdatedAt = now
	sessions[claim.SessionID] = session
	if err := s.writeLocked(sessions); err != nil {
		return Session{}, false, err
	}
	return session, true, nil
}

func (s *Store) ReleaseRunner(claim RunnerMutationClaim) (Session, bool, error) {
	claim = normalizeRunnerMutationClaim(claim)
	if err := validateRunnerMutationClaimInput(claim); err != nil {
		return Session{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	sessions, err := s.readLocked()
	if err != nil {
		return Session{}, false, err
	}
	session, ok := sessions[claim.SessionID]
	if !ok {
		return Session{}, false, errors.New("session not found")
	}
	if err := validateRunnerMutationClaim(session, claim, s.now().UTC(), true); err != nil {
		return session, false, err
	}
	session.Runner = nil
	session.UpdatedAt = s.now().UTC()
	sessions[claim.SessionID] = session
	if err := s.writeLocked(sessions); err != nil {
		return Session{}, false, err
	}
	return session, true, nil
}

// ValidateRunnerClaim verifies an explicit caller credential against durable
// runner state without exposing the current attempt or token on failure.
func (s *Store) ValidateRunnerClaim(claim RunnerMutationClaim, requireLive bool) (Session, error) {
	claim = normalizeRunnerMutationClaim(claim)
	if err := validateRunnerMutationClaimInput(claim); err != nil {
		return Session{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sessions, err := s.readLocked()
	if err != nil {
		return Session{}, err
	}
	session, ok := sessions[claim.SessionID]
	if !ok {
		return Session{}, errors.New("session not found")
	}
	if err := validateRunnerMutationClaim(session, claim, s.now().UTC(), requireLive); err != nil {
		return session, err
	}
	return session, nil
}

// CheckpointRunner advances checkpoint metadata only for the exact live claim.
func (s *Store) CheckpointRunner(input CheckpointRunnerInput) (Session, error) {
	input.Claim = normalizeRunnerMutationClaim(input.Claim)
	input.Status = strings.TrimSpace(input.Status)
	if err := validateRunnerMutationClaimInput(input.Claim); err != nil {
		return Session{}, err
	}
	if input.EventID <= 0 {
		return Session{}, errors.New("runner checkpoint event id must be positive")
	}
	now := s.now().UTC()
	recordedAt := input.RecordedAt.UTC()
	if recordedAt.IsZero() {
		recordedAt = now
	} else if recordedAt.After(now) {
		return Session{}, errors.New("runner checkpoint time cannot be in the future")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sessions, err := s.readLocked()
	if err != nil {
		return Session{}, err
	}
	session, ok := sessions[input.Claim.SessionID]
	if !ok {
		return Session{}, errors.New("session not found")
	}
	if err := validateRunnerMutationClaim(session, input.Claim, now, true); err != nil {
		return session, err
	}
	if isTerminalRunnerStatus(input.Status) {
		session.Runner.Status = "running"
	} else {
		session.Runner.Status = input.Status
	}
	session.Runner.LastCheckpoint = input.Checkpoint
	session.Runner.LastCheckpointAt = recordedAt
	session.Runner.LastCheckpointEventID = input.EventID
	session.UpdatedAt = recordedAt
	sessions[input.Claim.SessionID] = session
	if err := s.writeLocked(sessions); err != nil {
		return Session{}, err
	}
	return session, nil
}

func (s *Store) ClaimNextRunner(runnerID string, ttl time.Duration) (Session, bool, error) {
	if runnerID == "" {
		return Session{}, false, errors.New("runner id is required")
	}
	if ttl <= 0 {
		return Session{}, false, errors.New("runner ttl must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	sessions, err := s.readLocked()
	if err != nil {
		return Session{}, false, err
	}
	now := s.now().UTC()
	if session, ok := s.findLiveRunnerSessionLocked(sessions, runnerID, now); ok {
		return session, false, nil
	}

	candidates := make([]Session, 0)
	for _, session := range sessions {
		if isPendingRunnerSession(session, now) {
			candidates = append(candidates, session)
		}
	}
	if len(candidates) == 0 {
		return Session{}, false, nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		left := candidates[i]
		right := candidates[j]
		leftPendingAt := pendingUserTime(left)
		rightPendingAt := pendingUserTime(right)
		if leftPendingAt.Equal(rightPendingAt) {
			return left.ID < right.ID
		}
		if leftPendingAt.IsZero() {
			return false
		}
		if rightPendingAt.IsZero() {
			return true
		}
		return leftPendingAt.Before(rightPendingAt)
	})
	claimed := claimSessionRunner(candidates[0], runnerID, ttl, now)
	sessions[claimed.ID] = claimed
	if err := s.writeLocked(sessions); err != nil {
		return Session{}, false, err
	}
	return claimed, true, nil
}

func (s *Store) RunnerQueueSnapshot() (RunnerQueueSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sessions, err := s.readLocked()
	if err != nil {
		return RunnerQueueSnapshot{}, err
	}
	now := s.now().UTC()
	snapshot := RunnerQueueSnapshot{TotalSessions: len(sessions)}
	var oldest *Session
	var oldestAt time.Time
	for _, session := range sessions {
		if session.Runner != nil {
			switch {
			case isTerminalRunnerStatus(session.Runner.Status):
				snapshot.Terminal++
			case isLiveRunner(session.Runner, now):
				snapshot.Running++
			default:
				snapshot.Expired++
			}
		}
		if isPendingRunnerSession(session, now) {
			snapshot.Pending++
			pendingAt := pendingUserTime(session)
			if oldest == nil || pendingAt.Before(oldestAt) || (pendingAt.Equal(oldestAt) && session.ID < oldest.ID) {
				candidate := session
				oldest = &candidate
				oldestAt = pendingAt
			}
		}
	}
	if oldest != nil {
		snapshot.OldestPending = oldest
		if !oldestAt.IsZero() && now.After(oldestAt) {
			snapshot.OldestPendingAgeSeconds = int64(now.Sub(oldestAt).Seconds())
		}
	}
	return snapshot, nil
}

func (s *Store) RunnerBacklog(options RunnerBacklogOptions) (RunnerBacklog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sessions, err := s.readLocked()
	if err != nil {
		return RunnerBacklog{}, err
	}
	now := s.now().UTC()
	backlog := RunnerBacklog{TotalSessions: len(sessions)}
	items := make([]RunnerBacklogItem, 0, len(sessions))
	for _, session := range sessions {
		item := runnerBacklogItem(session, now)
		switch item.State {
		case "pending":
			backlog.Pending++
			if runnerBacklogItemMatches(item, options) {
				items = append(items, item)
			}
		case "expired":
			backlog.Pending++
			backlog.Expired++
			if runnerBacklogItemMatches(item, options) {
				items = append(items, item)
			}
		case "running":
			backlog.Running++
			if options.IncludeRunning && runnerBacklogItemMatches(item, options) {
				items = append(items, item)
			}
		case "terminal":
			backlog.Terminal++
			if options.IncludeTerminal && runnerBacklogItemMatches(item, options) {
				items = append(items, item)
			}
		}
	}
	sort.Slice(items, func(i, j int) bool {
		left := items[i]
		right := items[j]
		if weight := runnerBacklogStateWeight(left.State) - runnerBacklogStateWeight(right.State); weight != 0 {
			return weight < 0
		}
		if !left.WaitingSince.Equal(right.WaitingSince) {
			if left.WaitingSince.IsZero() {
				return false
			}
			if right.WaitingSince.IsZero() {
				return true
			}
			return left.WaitingSince.Before(right.WaitingSince)
		}
		return left.SessionID < right.SessionID
	})
	for index := range items {
		items[index].Priority = index + 1
	}
	if options.Limit > 0 && len(items) > options.Limit {
		backlog.Truncated = true
		items = items[:options.Limit]
	}
	backlog.Returned = len(items)
	backlog.Items = items
	return backlog, nil
}

func runnerBacklogItemMatches(item RunnerBacklogItem, options RunnerBacklogOptions) bool {
	if projectID := strings.TrimSpace(options.ProjectID); projectID != "" {
		if item.Project == nil || item.Project.ID != projectID {
			return false
		}
	}
	if state := strings.TrimSpace(options.State); state != "" && item.State != state {
		return false
	}
	return true
}

func runnerBacklogItem(session Session, now time.Time) RunnerBacklogItem {
	state := runnerBacklogState(session, now)
	waitingSince := pendingUserTime(session)
	item := RunnerBacklogItem{
		SessionID:         session.ID,
		Title:             session.Title,
		State:             state,
		NextAction:        runnerBacklogNextAction(state),
		WaitingSince:      waitingSince,
		UpdatedAt:         session.UpdatedAt,
		LastUserMessageAt: session.LastUserMessageAt,
		MessageCount:      session.MessageCount,
		WorkDir:           session.WorkDir,
		Project:           session.Project,
		Orchestration:     copyMap(session.Orchestration),
	}
	if !waitingSince.IsZero() && now.After(waitingSince) {
		item.AgeSeconds = int64(now.Sub(waitingSince).Seconds())
	}
	if session.Runner != nil {
		item.RunnerID = session.Runner.RunnerID
		item.RunnerStatus = session.Runner.Status
		item.Attempt = session.Runner.Attempt
		item.ClaimedAt = session.Runner.ClaimedAt
		item.LastHeartbeatAt = session.Runner.LastHeartbeatAt
		item.ExpiresAt = session.Runner.ExpiresAt
		item.LastCheckpoint = session.Runner.LastCheckpoint
		item.LastCheckpointAt = session.Runner.LastCheckpointAt
		if !session.Runner.ClaimedAt.IsZero() && now.After(session.Runner.ClaimedAt) {
			item.LeaseAgeSeconds = int64(now.Sub(session.Runner.ClaimedAt).Seconds())
		}
		if isLiveRunner(session.Runner, now) {
			item.LeaseRemainingSeconds = int64(session.Runner.ExpiresAt.Sub(now).Seconds())
		} else if !isTerminalRunnerStatus(session.Runner.Status) && !session.Runner.ExpiresAt.IsZero() {
			item.LeaseExpiredSeconds = int64(now.Sub(session.Runner.ExpiresAt).Seconds())
		}
	}
	return item
}

func runnerBacklogState(session Session, now time.Time) string {
	if session.Runner != nil {
		switch {
		case isTerminalRunnerStatus(session.Runner.Status):
			return "terminal"
		case isLiveRunner(session.Runner, now):
			return "running"
		default:
			return "expired"
		}
	}
	if session.LastRole == "user" {
		return "pending"
	}
	return "idle"
}

func runnerBacklogNextAction(state string) string {
	switch state {
	case "pending":
		return "claim"
	case "expired":
		return "reclaim"
	case "running":
		return "wait"
	case "terminal":
		return "none"
	default:
		return "none"
	}
}

func copyMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func runnerBacklogStateWeight(state string) int {
	switch state {
	case "pending", "expired":
		return 0
	case "running":
		return 2
	case "terminal":
		return 3
	default:
		return 4
	}
}

func (s *Store) findLiveRunnerSessionLocked(sessions map[string]Session, runnerID string, now time.Time) (Session, bool) {
	candidates := make([]Session, 0)
	for _, session := range sessions {
		if isLiveRunner(session.Runner, now) && session.Runner.RunnerID == runnerID {
			candidates = append(candidates, session)
		}
	}
	if len(candidates) == 0 {
		return Session{}, false
	}
	sort.Slice(candidates, func(i, j int) bool {
		left := candidates[i]
		right := candidates[j]
		if left.Runner.ClaimedAt.Equal(right.Runner.ClaimedAt) {
			return left.ID < right.ID
		}
		return left.Runner.ClaimedAt.Before(right.Runner.ClaimedAt)
	})
	return candidates[0], true
}

func isPendingRunnerSession(session Session, now time.Time) bool {
	if session.LastRole != "user" {
		return false
	}
	if session.Runner == nil {
		return true
	}
	if isLiveRunner(session.Runner, now) {
		return false
	}
	return !isTerminalRunnerStatus(session.Runner.Status)
}

func pendingUserTime(session Session) time.Time {
	if !session.LastUserMessageAt.IsZero() {
		return session.LastUserMessageAt
	}
	return session.UpdatedAt
}

func claimSessionRunner(session Session, runnerID string, ttl time.Duration, now time.Time) Session {
	claimedAt := now
	attempt := 1
	previousRunnerID := ""
	reclaimedExpiredLease := false
	if session.Runner != nil {
		attempt = session.Runner.Attempt + 1
		if attempt < 2 {
			attempt = 2
		}
		reclaimedExpiredLease = !isLiveRunner(session.Runner, now) && !isTerminalRunnerStatus(session.Runner.Status)
		if reclaimedExpiredLease {
			previousRunnerID = session.Runner.RunnerID
		}
	}
	session.Runner = &Runner{
		RunnerID:              runnerID,
		Status:                "running",
		Attempt:               attempt,
		PreviousRunnerID:      previousRunnerID,
		ReclaimedExpiredLease: reclaimedExpiredLease,
		ClaimedAt:             claimedAt,
		LastHeartbeatAt:       now,
		ExpiresAt:             now.Add(ttl),
	}
	session.UpdatedAt = now
	return session
}

func isTerminalRunnerStatus(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled"
}

func isLiveRunner(runner *Runner, now time.Time) bool {
	return runner != nil && !isTerminalRunnerStatus(runner.Status) && runner.ExpiresAt.After(now)
}

// RunnerClaimToken returns the opaque fencing token for persisted claim state.
// Including SessionID prevents a claim from being replayed across sessions.
func RunnerClaimToken(sessionID string, runner *Runner) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || runner == nil || strings.TrimSpace(runner.RunnerID) == "" || runner.Attempt <= 0 || runner.ClaimedAt.IsZero() {
		return ""
	}
	payload := strings.Join([]string{
		"synon.runner-claim.v1",
		sessionID,
		strings.TrimSpace(runner.RunnerID),
		strconv.Itoa(runner.Attempt),
		runner.ClaimedAt.UTC().Format(time.RFC3339Nano),
	}, "\x00")
	digest := sha256.Sum256([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func RunnerClaimFromSession(session Session) RunnerMutationClaim {
	claim := RunnerMutationClaim{SessionID: strings.TrimSpace(session.ID)}
	if session.Runner == nil {
		return claim
	}
	claim.RunnerID = strings.TrimSpace(session.Runner.RunnerID)
	claim.Attempt = session.Runner.Attempt
	claim.ClaimToken = RunnerClaimToken(session.ID, session.Runner)
	return claim
}

func normalizeRunnerMutationClaim(claim RunnerMutationClaim) RunnerMutationClaim {
	claim.SessionID = strings.TrimSpace(claim.SessionID)
	claim.RunnerID = strings.TrimSpace(claim.RunnerID)
	claim.ClaimToken = strings.TrimSpace(claim.ClaimToken)
	return claim
}

func validateRunnerMutationClaimInput(claim RunnerMutationClaim) error {
	if claim.SessionID == "" || claim.RunnerID == "" {
		return errors.New("session id and runner id are required")
	}
	if claim.Attempt <= 0 || claim.ClaimToken == "" {
		return errors.New("runner attempt and claim token are required")
	}
	return nil
}

func validateRunnerMutationClaim(session Session, claim RunnerMutationClaim, now time.Time, requireLive bool) error {
	runner := session.Runner
	if runner == nil || runner.RunnerID != claim.RunnerID || runner.Attempt != claim.Attempt ||
		RunnerClaimToken(session.ID, runner) != claim.ClaimToken {
		return ErrRunnerClaimStale
	}
	if requireLive && !isLiveRunner(runner, now) {
		return ErrRunnerClaimStale
	}
	return nil
}

func (s *Store) readLocked() (map[string]Session, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]Session{}, nil
		}
		return nil, err
	}
	var sessions map[string]Session
	if err := json.Unmarshal(data, &sessions); err != nil {
		return nil, err
	}
	if sessions == nil {
		sessions = map[string]Session{}
	}
	return sessions, nil
}

func (s *Store) writeLocked(sessions map[string]Session) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(sessions, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
