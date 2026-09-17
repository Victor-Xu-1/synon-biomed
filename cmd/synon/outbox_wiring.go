package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/google/uuid"

	"synon-go/internal/outbox"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/server"
)

func newRealtimeOutboxDispatcher(store *workspace.Store, app *server.Server) (*outbox.Dispatcher, error) {
	return outbox.NewDispatcher(store, outbox.RealtimeDeliverer{Store: store, Fanout: app}, outbox.Options{
		WorkerID: fmt.Sprintf("synon-go-realtime-%d-%s", os.Getpid(), uuid.NewString()),
		Topics:   []string{workspace.RealtimeOutboxTopic}, BatchSize: 4,
		Lease:        30 * time.Second,
		ErrorBackoff: 250 * time.Millisecond, RetryBase: 100 * time.Millisecond,
		RetryMax: 30 * time.Second, DeliveryLimit: 10 * time.Second,
		OnError: func(err error) { log.Printf("realtime outbox: %v", err) },
	})
}

func newKernelSettlementOutboxDispatcher(store *workspace.Store, app *server.Server) (*outbox.Dispatcher, error) {
	deliverer := outbox.KernelSettlementDeliverer{Store: store}
	if app != nil {
		deliverer.OnDelivered = app.WakeFrameResumeDispatchAfterKernelSettlement
	}
	return outbox.NewDispatcher(store, deliverer, outbox.Options{
		WorkerID: fmt.Sprintf("synon-biomed-kernel-settlement-%d-%s", os.Getpid(), uuid.NewString()),
		Topics:   []string{workspace.KernelResultSettlementOutboxTopic}, BatchSize: 4,
		Lease:        30 * time.Second,
		ErrorBackoff: 250 * time.Millisecond, RetryBase: 100 * time.Millisecond,
		RetryMax: 10 * time.Minute, DeliveryLimit: 15 * time.Second,
		OnError: func(err error) { log.Printf("kernel result settlement outbox: %v", err) },
	})
}

// routineRealtimeRepository makes scheduler claims and completions use the
// same transactional outbox contract as HTTP routine mutations.
type routineRealtimeRepository struct{ store *workspace.Store }

func (r routineRealtimeRepository) ClaimNextDueRoutine(now time.Time, lockTTL time.Duration) (workspace.Routine, bool, error) {
	return r.store.ClaimNextDueRoutineRealtime(context.Background(), now, lockTTL, "", uuid.NewString())
}

func (r routineRealtimeRepository) CompleteRoutineTick(id string, at time.Time, successful bool, result string) error {
	routine, err := r.store.GetRoutine(id)
	if err != nil {
		return err
	}
	_, err = r.store.CompleteRoutineTickRealtime(context.Background(), routine, at, successful, result)
	return err
}

func (r routineRealtimeRepository) CompleteClaimedRoutineTick(claim workspace.Routine, at time.Time, successful bool, result string) error {
	_, err := r.store.CompleteRoutineTickRealtime(context.Background(), claim, at, successful, result)
	return err
}

func (r routineRealtimeRepository) RenewClaimedRoutineTick(ctx context.Context, claim workspace.Routine, at time.Time) error {
	return r.store.RenewClaimedRoutineTick(ctx, claim, at)
}

func (r routineRealtimeRepository) NextRoutineWakeAt(lockTTL time.Duration) (time.Time, bool, error) {
	return r.store.NextRoutineWakeAt(lockTTL)
}
