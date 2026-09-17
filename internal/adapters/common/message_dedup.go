package common

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

type messageDedupEntry struct {
	seenAt      time.Time
	fingerprint string
	done        chan struct{}
	completed   bool
	err         error
}

var (
	ErrMessageDedupConflict = errors.New("message dedup payload conflict")
	ErrMessageDedupCapacity = errors.New("message dedup capacity exhausted")
	ErrMessageDedupPanic    = errors.New("message processing panicked")
)

type MessageDedup struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxEntries int
	entries    map[string]*messageDedupEntry
	order      []string
}

type MessageDedupReservation struct {
	dedup *MessageDedup
	id    string
	entry *messageDedupEntry
	once  sync.Once
}

func NewMessageDedup(ttl time.Duration, maxEntries int) *MessageDedup {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if maxEntries <= 0 {
		maxEntries = 5000
	}
	return &MessageDedup{ttl: ttl, maxEntries: maxEntries, entries: make(map[string]*messageDedupEntry)}
}

// Acquire creates one completion-aware reservation. Followers wait for the
// durable leader rather than acknowledging a duplicate that may still fail.
func (d *MessageDedup) Acquire(ctx context.Context, id string, fingerprints ...string) (*MessageDedupReservation, bool, error) {
	if d == nil {
		return nil, false, nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, false, errors.New("message dedup id is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	fingerprint := ""
	if len(fingerprints) > 0 {
		fingerprint = strings.TrimSpace(fingerprints[0])
	}
	for {
		d.mu.Lock()
		now := time.Now()
		d.sweep(now)
		if entry, ok := d.entries[id]; ok {
			if entry.fingerprint != fingerprint {
				d.mu.Unlock()
				return nil, false, ErrMessageDedupConflict
			}
			if entry.completed {
				d.mu.Unlock()
				return nil, true, nil
			}
			done := entry.done
			d.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, false, context.Cause(ctx)
			case <-done:
				if entry.err != nil {
					return nil, false, entry.err
				}
				return nil, true, nil
			}
		}
		if len(d.entries) >= d.maxEntries {
			d.evictOldestCompleted()
		}
		if len(d.entries) >= d.maxEntries {
			d.mu.Unlock()
			return nil, false, ErrMessageDedupCapacity
		}
		entry := &messageDedupEntry{seenAt: now, fingerprint: fingerprint, done: make(chan struct{})}
		d.entries[id] = entry
		d.order = append(d.order, id)
		d.mu.Unlock()
		return &MessageDedupReservation{dedup: d, id: id, entry: entry}, false, nil
	}
}

func (r *MessageDedupReservation) Complete(err error) {
	if r == nil || r.dedup == nil || r.entry == nil {
		return
	}
	r.once.Do(func() {
		d := r.dedup
		d.mu.Lock()
		defer d.mu.Unlock()
		if current := d.entries[r.id]; current != r.entry || r.entry.completed {
			return
		}
		r.entry.completed = true
		r.entry.err = err
		r.entry.seenAt = time.Now()
		close(r.entry.done)
		if err != nil {
			d.remove(r.id)
		}
	})
}

// CompleteMessageDedupReservation must be deferred by reservation owners. It
// releases followers with a deterministic failure before preserving a panic.
func CompleteMessageDedupReservation(reservation *MessageDedupReservation, err *error) {
	if recovered := recover(); recovered != nil {
		reservation.Complete(ErrMessageDedupPanic)
		panic(recovered)
	}
	if err == nil {
		reservation.Complete(nil)
		return
	}
	reservation.Complete(*err)
}

func (d *MessageDedup) TryRecord(id string) bool {
	if d == nil {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	if entry, ok := d.entries[id]; ok {
		if !entry.completed {
			return false
		}
		if now.Sub(entry.seenAt) < d.ttl {
			return false
		}
		d.remove(id)
	}
	d.sweep(now)
	if len(d.entries) >= d.maxEntries {
		d.evictOldestCompleted()
	}
	if len(d.entries) >= d.maxEntries {
		return false
	}
	entry := &messageDedupEntry{seenAt: now, done: make(chan struct{}), completed: true}
	close(entry.done)
	d.entries[id] = entry
	d.order = append(d.order, id)
	return true
}

// Forget releases a legacy reservation whose durable processing did not complete.
func (d *MessageDedup) Forget(id string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if entry := d.entries[id]; entry != nil && !entry.completed {
		entry.completed = true
		entry.err = errors.New("message processing did not complete")
		close(entry.done)
	}
	d.remove(id)
}

func (d *MessageDedup) sweep(now time.Time) {
	for _, id := range append([]string(nil), d.order...) {
		entry, ok := d.entries[id]
		if !ok {
			continue
		}
		if entry.completed && now.Sub(entry.seenAt) >= d.ttl {
			d.remove(id)
		}
	}
}

func (d *MessageDedup) remove(id string) {
	delete(d.entries, id)
	for index, existing := range d.order {
		if existing == id {
			d.order = append(d.order[:index], d.order[index+1:]...)
			return
		}
	}
}

func (d *MessageDedup) evictOldestCompleted() {
	for _, id := range append([]string(nil), d.order...) {
		if entry := d.entries[id]; entry != nil && entry.completed {
			d.remove(id)
			return
		}
	}
}
