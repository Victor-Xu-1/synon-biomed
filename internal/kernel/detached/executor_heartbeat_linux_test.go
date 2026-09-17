//go:build linux

package detached

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestRunHeartbeatLoopKeepsActiveExecutorAliveAcrossSQLiteContention(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	sequences := make([]int64, 0, 4)
	attempts := 0
	done := make(chan error, 1)
	go func() {
		done <- runHeartbeatLoop(ctx, time.Millisecond, 7, func(sequence int64) error {
			mu.Lock()
			defer mu.Unlock()
			attempts++
			sequences = append(sequences, sequence)
			if attempts < 3 {
				return errors.New("database is locked (5) (SQLITE_BUSY)")
			}
			cancel()
			return nil
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("heartbeat loop returned after transient contention: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("heartbeat loop did not recover from transient contention")
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 3 {
		t.Fatalf("attempts=%d want 3", attempts)
	}
	for _, sequence := range sequences {
		if sequence != 8 {
			t.Fatalf("sequences=%v; a failed heartbeat must retry the same sequence", sequences)
		}
	}
}

func TestRunHeartbeatLoopStillFailsClosedForPermanentAuthorityErrors(t *testing.T) {
	want := errors.New("kernel execution backend authority is stale")
	err := runHeartbeatLoop(context.Background(), time.Millisecond, 3, func(int64) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("heartbeat error=%v want %v", err, want)
	}
}
