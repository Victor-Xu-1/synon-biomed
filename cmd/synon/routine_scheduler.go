package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"time"

	"synon-go/internal/config"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/routinescheduler"
	"synon-go/internal/server"
)

func newRoutineScheduler(cfg config.Config, app *server.Server, store *workspace.Store) (*routinescheduler.Scheduler, error) {
	runner := normalizeRunnerConfig(cfg.Runner)
	configuredLease, err := checkedRoutineConfigDuration("runner lease ttl", runner.LeaseTTLSeconds, time.Second)
	if err != nil {
		return nil, err
	}
	if _, err := checkedRoutineConfigDuration("runner chat timeout", runner.ChatTimeoutSeconds, time.Second); err != nil {
		return nil, err
	}
	if _, err := checkedRoutineConfigDuration("runner poll interval", runner.PollIntervalMS, time.Millisecond); err != nil {
		return nil, err
	}
	chat := routineSessionRunnerChatOptions(cfg)
	executor, err := app.NewRoutineExecutor(server.RoutineExecutorOptions{
		Chat: chat, RunnerIDPrefix: "synon-go-routine",
	})
	if err != nil {
		return nil, err
	}
	tickTimeout, lockTTL, err := routineSchedulerDurations(chat, configuredLease)
	if err != nil {
		return nil, err
	}
	return routinescheduler.New(routinescheduler.Options{
		Repository: routineRealtimeRepository{store: store}, Executor: executor,
		LockTTL: lockTTL, TickTimeout: tickTimeout, UnboundedTicks: tickTimeout == 0,
		ErrorBackoff: time.Second, MinimumDelay: 10 * time.Millisecond,
		OnError: func(runtimeErr error) {
			log.Printf("routine scheduler: %v", runtimeErr)
			if auditErr := app.RecordRoutineSchedulerError(runtimeErr); auditErr != nil {
				log.Printf("routine scheduler audit: %v", auditErr)
			}
		},
	})
}

func routineSchedulerDurations(chat server.SessionRunnerChatOptions, configuredLease time.Duration) (time.Duration, time.Duration, error) {
	tickTimeout, err := server.RoutineExecutionBudget(chat)
	if err != nil {
		return 0, 0, err
	}
	lockTTL, err := server.RoutineLockTTL(configuredLease, tickTimeout)
	if err != nil {
		return 0, 0, err
	}
	return tickTimeout, lockTTL, nil
}

func checkedRoutineConfigDuration(label string, value int, unit time.Duration) (time.Duration, error) {
	if value <= 0 || unit <= 0 {
		return 0, fmt.Errorf("%s must be positive", label)
	}
	if int64(value) > math.MaxInt64/int64(unit) {
		return 0, fmt.Errorf("%s exceeds the maximum representable duration", label)
	}
	return time.Duration(value) * unit, nil
}

// routineSessionRunnerChatOptions permits configured real HTTP fallback while
// deliberately excluding the deterministic builtin. Saved Provider authority
// is resolved inside RunSessionRunnerChatOnce and remains authoritative.
func routineSessionRunnerChatOptions(cfg config.Config) server.SessionRunnerChatOptions {
	runner := normalizeRunnerConfig(cfg.Runner)
	options := server.SessionRunnerChatOptions{
		RunnerID: runner.ID + "-routine", SystemPrompt: runner.ChatSystemPrompt,
		AllowedTools:   append([]string(nil), runner.ChatTools...),
		RequestTimeout: time.Duration(runner.ChatTimeoutSeconds) * time.Second,
		MaxToolRounds:  runner.ChatToolRoundLimit, MaxToolCallsPerRound: runner.ChatToolCallBatchLimit,
		MaxAttempts:  runner.ChatMaxAttempts,
		LeaseTTL:     time.Duration(runner.LeaseTTLSeconds) * time.Second,
		PollInterval: time.Duration(runner.PollIntervalMS) * time.Millisecond,
		ReplayLimit:  runner.ReplayLimit, OutputLimitBytes: runner.OutputLimitBytes,
		RequireSavedModel: true,
	}
	if sessionRunnerProvider(runner) == "openai_chat" {
		options.Endpoint = strings.TrimSpace(runner.ChatEndpoint)
		options.APIKey = strings.TrimSpace(runner.ChatAPIKey)
		options.Model = strings.TrimSpace(runner.ChatModel)
		options.RequireSavedModel = false
	}
	return options
}

func startRoutineScheduler(ctx context.Context, scheduler *routinescheduler.Scheduler) error {
	if err := scheduler.Start(ctx); err != nil {
		return err
	}
	return nil
}

func stopRoutineScheduler(app *server.Server, scheduler *routinescheduler.Scheduler) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := scheduler.Stop(ctx); err != nil {
		log.Printf("stop routine scheduler: %v", err)
		if auditErr := app.RecordRoutineSchedulerError(err); auditErr != nil {
			log.Printf("routine scheduler stop audit: %v", auditErr)
		}
	}
}

func withRoutineSchedulerWake(next http.Handler, wake func()) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		if wake != nil && routineMutationRequest(r) {
			wake()
		}
	})
}

func routineMutationRequest(r *http.Request) bool {
	if r == nil || (r.URL.Path != "/api/go/routines" && !strings.HasPrefix(r.URL.Path, "/api/go/routines/")) {
		return false
	}
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}
