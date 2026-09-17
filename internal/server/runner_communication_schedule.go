package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

const runnerCommunicationScheduleNamespace = "session-runner-communication-schedule"

// This is cadence metadata, never generated prose or scientific state. Scope
// includes branch and input revision so unrelated turns cannot inherit silence.
type sessionRunnerCommunicationSchedule struct {
	mu             sync.Mutex
	LastPublished  time.Time `json:"last_published"`
	CompletedSteps int       `json:"completed_steps"`
	now            func() time.Time
	persist        func(*sessionRunnerCommunicationSchedule) error
}

func (schedule *sessionRunnerCommunicationSchedule) due() bool {
	schedule.mu.Lock()
	defer schedule.mu.Unlock()
	return schedule.LastPublished.IsZero() || schedule.CompletedSteps >= 4 && schedule.now().Sub(schedule.LastPublished) >= 90*time.Second
}

func (schedule *sessionRunnerCommunicationSchedule) allowed() bool {
	schedule.mu.Lock()
	defer schedule.mu.Unlock()
	return schedule.LastPublished.IsZero() || schedule.CompletedSteps > 0 && schedule.now().Sub(schedule.LastPublished) >= 30*time.Second
}

func (schedule *sessionRunnerCommunicationSchedule) published() error {
	schedule.mu.Lock()
	defer schedule.mu.Unlock()
	schedule.LastPublished = schedule.now().UTC()
	schedule.CompletedSteps = 0
	return schedule.save()
}

func (schedule *sessionRunnerCommunicationSchedule) settled() error {
	schedule.mu.Lock()
	defer schedule.mu.Unlock()
	if schedule.CompletedSteps < 4 {
		schedule.CompletedSteps++
	}
	return schedule.save()
}

func (schedule *sessionRunnerCommunicationSchedule) save() error {
	if schedule.persist != nil {
		return schedule.persist(schedule)
	}
	return nil
}

func (s *Server) loadSessionRunnerCommunicationSchedule(ctx context.Context, run *sessionRunnerChatRun) (*sessionRunnerCommunicationSchedule, error) {
	schedule := &sessionRunnerCommunicationSchedule{now: time.Now}
	if s.runtimeStore == nil || run == nil || run.Transcript == nil {
		return schedule, nil
	}
	stream := run.Transcript.Stream
	branch, err := s.transcriptStore.GetBranchState(ctx, stream.UID, stream.OwnerID)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("%s:%s:%d", stream.UID, branch.ActiveBranchID, run.Transcript.Claim.ClaimedInputRevision)
	entry, found, err := s.runtimeStore.Get(runnerCommunicationScheduleNamespace, key)
	if err != nil {
		return nil, err
	}
	if found {
		encoded, err := json.Marshal(entry.Value)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(encoded, schedule); err != nil {
			return nil, err
		}
	}
	schedule.persist = func(current *sessionRunnerCommunicationSchedule) error {
		_, err := s.runtimeStore.Set(runnerCommunicationScheduleNamespace, key, current)
		return err
	}
	return schedule, nil
}
