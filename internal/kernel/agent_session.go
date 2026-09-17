package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

type EnsuredSession struct {
	ID     string
	Worker *Worker
	Reused bool
}

func StableSessionID(spec SessionSpec) (string, error) {
	spec = normalizeSessionSpec(spec)
	if spec.OwnerID == "" || spec.ProjectID == "" || spec.FrameID == "" || spec.FrameIncarnationID == "" ||
		spec.RootFrameID == "" || spec.RootFrameIncarnationID == "" || spec.KernelKind == "" ||
		spec.Language == "" || spec.Environment == "" || spec.WorkspaceDir == "" {
		return "", errors.New("owner, project, frame, frame incarnation, root frame, root incarnation, kernel kind, language, environment, and workspace are required for stable kernel identity")
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		spec.OwnerID, spec.ProjectID, spec.RootFrameID, spec.RootFrameIncarnationID,
		spec.FrameID, spec.FrameIncarnationID,
		spec.AgentName, spec.DelegateName, spec.KernelKind, spec.Language, spec.Environment,
		spec.RuntimeGeneration, spec.WorkspaceDir,
	}, "\x00")))
	return "kernel-" + hex.EncodeToString(sum[:12]), nil
}

func (m *Manager) EnsureSession(spec SessionSpec) (EnsuredSession, error) {
	spec = normalizeSessionSpec(spec)
	if spec.KernelID == "" {
		id, err := StableSessionID(spec)
		if err != nil {
			return EnsuredSession{}, err
		}
		spec.KernelID = id
	}
	if existing := m.workerByID(spec.KernelID); existing != nil {
		state := lifecycleWorkerState(existing)
		if state != nil {
			state.mu.Lock()
			same := sameSessionSpec(state.spec, spec)
			sameAuthority := sameSessionSpecExceptMutableAuthority(state.spec, spec)
			state.mu.Unlock()
			if !same {
				if !sameAuthority {
					return EnsuredSession{}, errors.New("stable kernel identity is already bound to different session metadata")
				}
				timeout := m.config.ShutdownTimeout
				if timeout <= 0 {
					timeout = 5 * time.Second
				}
				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				err := existing.Close(ctx)
				cancel()
				if err != nil {
					return EnsuredSession{}, errors.New("restart kernel after mount authority change")
				}
				worker, err := m.StartSession(spec)
				if err != nil {
					return EnsuredSession{}, err
				}
				return EnsuredSession{ID: spec.KernelID, Worker: worker}, nil
			}
			return EnsuredSession{ID: spec.KernelID, Worker: existing, Reused: true}, nil
		}
	}
	worker, err := m.StartSession(spec)
	if err != nil {
		return EnsuredSession{}, err
	}
	return EnsuredSession{ID: spec.KernelID, Worker: worker}, nil
}

func sameSessionSpecExceptMutableAuthority(left, right SessionSpec) bool {
	left.Mounts, right.Mounts = nil, nil
	left.ProtectedPaths, right.ProtectedPaths = nil, nil
	left.EgressAllowedDomains, right.EgressAllowedDomains = nil, nil
	left.EgressDeniedDomains, right.EgressDeniedDomains = nil, nil
	left.CABundle, right.CABundle = "", ""
	left.UpstreamProxy, right.UpstreamProxy = "", ""
	return sameSessionSpec(left, right)
}

func (m *Manager) ExecuteSession(ctx context.Context, spec SessionSpec, input SubmitRequest) (ExecutionOutcome, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := m.EnsureSession(spec)
	if err != nil {
		return ExecutionOutcome{}, err
	}
	state := lifecycleWorkerState(session.Worker)
	if state == nil {
		return ExecutionOutcome{}, errors.New("kernel session identity disappeared")
	}
	state.mu.Lock()
	bound := state.spec
	state.mu.Unlock()
	input.FrameID = bound.FrameID
	input.OwnerID = bound.OwnerID
	input.ProjectID = bound.ProjectID
	input.FrameIncarnationID = bound.FrameIncarnationID
	input.RootFrameIncarnationID = bound.RootFrameIncarnationID
	input.KernelKind = bound.KernelKind
	input.Language = bound.Language
	input.Environment = bound.Environment
	handle, err := m.Submit(input)
	if err != nil {
		return ExecutionOutcome{}, err
	}
	select {
	case outcome, ok := <-handle.Done():
		if !ok {
			return ExecutionOutcome{}, errors.New("kernel execution closed without outcome")
		}
		return outcome, nil
	case <-ctx.Done():
		m.Interrupt(bound.FrameID, input.ExecID)
		return ExecutionOutcome{}, ctx.Err()
	}
}
