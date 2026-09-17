// Package outbox dispatches durable workspace events with at-least-once
// delivery. Receivers must deduplicate by OutboxEvent.ID.
package outbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"synon-go/internal/persistence/workspace"
)

type Repository interface {
	ClaimOutbox(context.Context, workspace.ClaimOutboxInput) ([]workspace.OutboxEvent, error)
	AckOutbox(context.Context, string, string) error
	RetryOutbox(context.Context, string, string, string, time.Duration) (bool, error)
}

type WakeRepository interface {
	OutboxWake() <-chan struct{}
	NextOutboxWakeAt(context.Context, []string) (time.Time, bool, error)
}

type Deliverer interface {
	Deliver(context.Context, workspace.OutboxEvent) error
}

// ErrDeliverySettled tells the dispatcher that a topic-specific deliverer has
// atomically consumed the live claim while recording a terminal domain result.
// The generic dispatcher must neither ack nor retry that event.
var ErrDeliverySettled = errors.New("outbox delivery was atomically settled")

type Options struct {
	WorkerID      string
	Topics        []string
	BatchSize     int
	Lease         time.Duration
	PollInterval  time.Duration
	ErrorBackoff  time.Duration
	RetryBase     time.Duration
	RetryMax      time.Duration
	DeliveryLimit time.Duration
	OnError       func(error)
}

type Dispatcher struct {
	repository    Repository
	deliverer     Deliverer
	workerID      string
	topics        []string
	batchSize     int
	lease         time.Duration
	pollInterval  time.Duration
	errorBackoff  time.Duration
	retryBase     time.Duration
	retryMax      time.Duration
	deliveryLimit time.Duration
	onError       func(error)
	reportMu      sync.Mutex
}

const (
	maxDispatcherLease   = 24 * time.Hour
	maxDispatcherBackoff = 24 * time.Hour
)

func NewDispatcher(repository Repository, deliverer Deliverer, options Options) (*Dispatcher, error) {
	if repository == nil {
		return nil, errors.New("outbox repository is required")
	}
	if deliverer == nil {
		return nil, errors.New("outbox deliverer is required")
	}
	options.WorkerID = strings.TrimSpace(options.WorkerID)
	if options.WorkerID == "" {
		return nil, errors.New("outbox worker id is required")
	}
	if options.BatchSize == 0 {
		options.BatchSize = 32
	}
	if options.Lease == 0 {
		options.Lease = 30 * time.Second
	}
	if options.PollInterval == 0 {
		options.PollInterval = 250 * time.Millisecond
	}
	if options.ErrorBackoff == 0 {
		options.ErrorBackoff = time.Second
	}
	if options.RetryBase == 0 {
		options.RetryBase = time.Second
	}
	if options.RetryMax == 0 {
		options.RetryMax = time.Minute
	}
	if options.DeliveryLimit == 0 {
		options.DeliveryLimit = 20 * time.Second
	}
	if options.BatchSize < 1 || options.BatchSize > 256 || options.Lease <= 0 ||
		options.PollInterval <= 0 || options.ErrorBackoff <= 0 || options.RetryBase <= 0 ||
		options.RetryMax < options.RetryBase || options.DeliveryLimit <= 0 {
		return nil, errors.New("invalid outbox dispatcher timing or batch options")
	}
	if options.Lease > maxDispatcherLease || options.PollInterval > maxDispatcherBackoff ||
		options.ErrorBackoff > maxDispatcherBackoff || options.RetryBase > maxDispatcherBackoff ||
		options.RetryMax > maxDispatcherBackoff || options.DeliveryLimit > maxDispatcherLease {
		return nil, errors.New("outbox dispatcher lease and retry backoff must not exceed 24 hours")
	}
	if options.DeliveryLimit >= options.Lease {
		return nil, errors.New("outbox delivery limit must be shorter than claim lease")
	}
	return &Dispatcher{
		repository: repository, deliverer: deliverer, workerID: options.WorkerID,
		topics: append([]string(nil), options.Topics...), batchSize: options.BatchSize,
		lease: options.Lease, pollInterval: options.PollInterval, errorBackoff: options.ErrorBackoff,
		retryBase: options.RetryBase, retryMax: options.RetryMax, deliveryLimit: options.DeliveryLimit,
		onError: options.OnError,
	}, nil
}

// Run uses one claim coordinator and BatchSize delivery workers. The
// coordinator claims no more events than there are immediately available
// workers, so events never spend their lease queued behind a network call.
// Centralizing the empty claim prevents every worker from polling the same
// SQLite index independently while preserving parallel delivery. A
// cancellation during delivery deliberately leaves the claim unacked for
// takeover after DB lease expiry.
func (d *Dispatcher) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("outbox dispatcher context is required")
	}
	jobs := make(chan workspace.OutboxEvent)
	completed := make(chan struct{}, d.batchSize)
	var workers sync.WaitGroup
	workers.Add(d.batchSize)
	for range d.batchSize {
		go func() {
			defer workers.Done()
			d.runDeliveryWorker(ctx, jobs, completed)
		}()
	}
	d.runClaimCoordinator(ctx, jobs, completed)
	close(jobs)
	workers.Wait()
	return nil
}

func (d *Dispatcher) runClaimCoordinator(
	ctx context.Context,
	jobs chan<- workspace.OutboxEvent,
	completed <-chan struct{},
) {
	available := d.batchSize
	for {
		for available < d.batchSize {
			select {
			case <-completed:
				available++
			default:
				goto claimedCapacityReady
			}
		}
	claimedCapacityReady:
		if ctx.Err() != nil {
			return
		}
		if available == 0 {
			select {
			case <-ctx.Done():
				return
			case <-completed:
				available++
			}
			continue
		}
		events, err := d.repository.ClaimOutbox(ctx, workspace.ClaimOutboxInput{
			WorkerID: d.workerID, Topics: d.topics, Limit: available, Lease: d.lease,
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			d.report(fmt.Errorf("claim outbox batch: %w", err))
			if !waitForDispatch(ctx, completed, &available, d.errorBackoff) {
				return
			}
			continue
		}
		if len(events) == 0 {
			wakeRepository, eventDriven := d.repository.(WakeRepository)
			if !eventDriven {
				if !waitForDispatch(ctx, completed, &available, d.pollInterval) {
					return
				}
				continue
			}
			// Acquire the broadcast generation before querying durable due
			// state. A commit between the query and wait closes this channel,
			// so no enqueue/retry/partition-unlock wake can be lost.
			wake := wakeRepository.OutboxWake()
			wakeAt, found, wakeErr := wakeRepository.NextOutboxWakeAt(ctx, d.topics)
			if wakeErr != nil {
				if ctx.Err() != nil {
					return
				}
				d.report(fmt.Errorf("find next outbox wake time: %w", wakeErr))
				if !waitForDispatch(ctx, completed, &available, d.errorBackoff) {
					return
				}
				continue
			}
			if !waitForOutboxWake(ctx, completed, &available, wake, wakeAt, found) {
				return
			}
			continue
		}
		for _, event := range events {
			select {
			case <-ctx.Done():
				return
			case jobs <- event:
				available--
			}
		}
	}
}

func waitForOutboxWake(
	ctx context.Context,
	completed <-chan struct{},
	available *int,
	wake <-chan struct{},
	wakeAt time.Time,
	found bool,
) bool {
	if !found {
		select {
		case <-ctx.Done():
			return false
		case <-wake:
			return true
		case <-completed:
			*available = *available + 1
			return true
		}
	}
	delay := time.Until(wakeAt)
	if delay < time.Millisecond {
		delay = time.Millisecond
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-wake:
		return true
	case <-completed:
		*available = *available + 1
		return true
	case <-timer.C:
		return true
	}
}

func (d *Dispatcher) runDeliveryWorker(
	ctx context.Context,
	jobs <-chan workspace.OutboxEvent,
	completed chan<- struct{},
) {
	for {
		var event workspace.OutboxEvent
		select {
		case <-ctx.Done():
			return
		case next, open := <-jobs:
			if !open {
				return
			}
			event = next
		}
		deliveryCtx, cancel := context.WithTimeout(ctx, d.deliveryLimit)
		err := d.deliverer.Deliver(deliveryCtx, event)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, ErrDeliverySettled) {
			if !notifyDispatchCompleted(ctx, completed) {
				return
			}
			continue
		}
		if err == nil {
			if ackErr := d.repository.AckOutbox(ctx, event.ID, event.ClaimToken); ackErr != nil {
				d.report(fmt.Errorf("ack outbox event %s: %w", event.ID, ackErr))
				if !errors.Is(ackErr, workspace.ErrOutboxClaimLost) && !waitContext(ctx, d.errorBackoff) {
					return
				}
			}
			if !notifyDispatchCompleted(ctx, completed) {
				return
			}
			continue
		}
		d.report(fmt.Errorf("deliver outbox event %s (%s): %w", event.ID, event.Topic, err))
		backoff := exponentialBackoff(d.retryBase, d.retryMax, event.AttemptCount)
		_, retryErr := d.repository.RetryOutbox(ctx, event.ID, event.ClaimToken, err.Error(), backoff)
		if retryErr != nil && !errors.Is(retryErr, workspace.ErrOutboxClaimLost) {
			d.report(fmt.Errorf("record outbox retry %s: %w", event.ID, retryErr))
			if !waitContext(ctx, d.errorBackoff) {
				return
			}
		}
		if !notifyDispatchCompleted(ctx, completed) {
			return
		}
	}
}

func notifyDispatchCompleted(ctx context.Context, completed chan<- struct{}) bool {
	select {
	case <-ctx.Done():
		return false
	case completed <- struct{}{}:
		return true
	}
}

func waitForDispatch(
	ctx context.Context,
	completed <-chan struct{},
	available *int,
	delay time.Duration,
) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-completed:
		*available = *available + 1
		return true
	case <-timer.C:
		return true
	}
}

func exponentialBackoff(base, maximum time.Duration, attempt int) time.Duration {
	delay := base
	for index := 1; index < attempt; index++ {
		if delay >= maximum || delay > maximum/2 {
			return maximum
		}
		delay *= 2
	}
	return delay
}

func waitContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (d *Dispatcher) report(err error) {
	if d.onError != nil {
		d.reportMu.Lock()
		defer d.reportMu.Unlock()
		d.onError(err)
	}
}
