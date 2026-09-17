package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

const (
	dataLifecycleFirstSweepDelay = 5 * time.Minute
	dataLifecycleSweepInterval   = 24 * time.Hour
	dataLifecycleAuditMaxAge     = 7 * 24 * time.Hour
	dataLifecycleUsageMaxAge     = 30 * 24 * time.Hour
)

var dataLifecycleAuditNamespaces = []string{
	"adapter-live-smoke",
	"agent-runtime-hook-audit",
	"compute-byoc-lifecycle-audit",
	"tool-gateway-audit",
	"visual-reviews",
}

var dataLifecycleUsageNamespaces = []string{
	"kernel-host-model-audit",
	"mcp-invocations",
	"session-runner-model-audit",
	"skill-invocations",
}

type dataLifecyclePassReport struct {
	Workspace workspace.DataLifecycleReport
	Audits    int64
	Usage     int64
}

func (r dataLifecyclePassReport) Changed() bool {
	return r.Workspace.Changed() || r.Audits > 0 || r.Usage > 0
}

func (s *Server) startDataLifecycle() {
	if s == nil || s.workspaceStore == nil || s.runtimeStore == nil || s.dataLifecycleDone != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.dataLifecycleStop = cancel
	s.dataLifecycleDone = make(chan struct{})
	go func() {
		defer close(s.dataLifecycleDone)
		runDataLifecycleScheduler(ctx, dataLifecycleFirstSweepDelay, dataLifecycleSweepInterval, func(runCtx context.Context) {
			receipt, err := s.runDataLifecycleSweepWithReceipt(runCtx, time.Now().UTC())
			if err != nil {
				log.Printf("data lifecycle sweep failed: %v", err)
				return
			}
			log.Print(receipt)
		})
	}()
}

func runDataLifecycleScheduler(
	ctx context.Context,
	firstDelay, interval time.Duration,
	sweep func(context.Context),
) {
	if ctx == nil || firstDelay <= 0 || interval <= 0 || sweep == nil {
		return
	}
	timer := time.NewTimer(firstDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	for {
		sweep(ctx)
		timer.Reset(interval)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

func (s *Server) runDataLifecyclePass(ctx context.Context, now time.Time) (dataLifecyclePassReport, error) {
	if s == nil || s.workspaceStore == nil || s.runtimeStore == nil {
		return dataLifecyclePassReport{}, errors.New("data lifecycle stores are required")
	}
	report := dataLifecyclePassReport{}
	var err error
	report.Workspace, err = s.workspaceStore.SweepDataLifecycle(ctx, workspace.DefaultDataLifecyclePolicy())
	if err != nil {
		return dataLifecyclePassReport{}, err
	}
	report.Audits, err = s.runtimeStore.DeleteOlderThan(ctx, dataLifecycleAuditNamespaces, now.Add(-dataLifecycleAuditMaxAge))
	if err != nil {
		return report, fmt.Errorf("sweep runtime audit state: %w", err)
	}
	report.Usage, err = s.runtimeStore.DeleteOlderThan(ctx, dataLifecycleUsageNamespaces, now.Add(-dataLifecycleUsageMaxAge))
	if err != nil {
		return report, fmt.Errorf("sweep runtime usage state: %w", err)
	}
	return report, nil
}

func (s *Server) stopDataLifecycle(ctx context.Context) error {
	if s == nil || s.dataLifecycleStop == nil || s.dataLifecycleDone == nil {
		return nil
	}
	s.dataLifecycleStop()
	select {
	case <-s.dataLifecycleDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
