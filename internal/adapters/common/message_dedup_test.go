package common

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestMessageDedupRecordsDistinctMessages(t *testing.T) {
	dedup := NewMessageDedup(time.Second, 100)

	if !dedup.TryRecord("msg-1") {
		t.Fatal("first msg-1 should be new")
	}
	if !dedup.TryRecord("msg-2") {
		t.Fatal("first msg-2 should be new")
	}
	if dedup.TryRecord("msg-1") {
		t.Fatal("second msg-1 should be duplicate")
	}
}

func TestMessageDedupFollowersWaitForDurableCompletion(t *testing.T) {
	dedup := NewMessageDedup(time.Minute, 10)
	leader, duplicate, err := dedup.Acquire(context.Background(), "shared")
	if err != nil || duplicate || leader == nil {
		t.Fatalf("leader=%#v duplicate=%t err=%v", leader, duplicate, err)
	}
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		_, duplicate, err := dedup.Acquire(context.Background(), "shared")
		if err == nil && !duplicate {
			err = errors.New("follower was not marked duplicate")
		}
		result <- err
	}()
	<-started
	select {
	case err := <-result:
		t.Fatalf("follower returned before leader completion: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	leader.Complete(errors.New("durable projection failed"))
	if err := <-result; err == nil || err.Error() != "durable projection failed" {
		t.Fatalf("follower error=%v", err)
	}
	retry, duplicate, err := dedup.Acquire(context.Background(), "shared")
	if err != nil || duplicate || retry == nil {
		t.Fatalf("retry=%#v duplicate=%t err=%v", retry, duplicate, err)
	}
	retry.Complete(nil)
	if _, duplicate, err := dedup.Acquire(context.Background(), "shared"); err != nil || !duplicate {
		t.Fatalf("completed duplicate=%t err=%v", duplicate, err)
	}
}

func TestMessageDedupConcurrentFollowersObserveOneSuccess(t *testing.T) {
	dedup := NewMessageDedup(time.Minute, 100)
	leader, _, err := dedup.Acquire(context.Background(), "success")
	if err != nil {
		t.Fatal(err)
	}
	const followers = 32
	var wg sync.WaitGroup
	errorsCh := make(chan error, followers)
	for range followers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, duplicate, err := dedup.Acquire(context.Background(), "success")
			if err != nil {
				errorsCh <- err
			} else if !duplicate {
				errorsCh <- errors.New("follower became leader")
			}
		}()
	}
	leader.Complete(nil)
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
}

func TestMessageDedupRejectsConflictingFingerprints(t *testing.T) {
	dedup := NewMessageDedup(time.Minute, 10)
	leader, duplicate, err := dedup.Acquire(context.Background(), "same-id", "payload-a")
	if err != nil || duplicate || leader == nil {
		t.Fatalf("leader=%#v duplicate=%t err=%v", leader, duplicate, err)
	}
	if _, _, err := dedup.Acquire(context.Background(), "same-id", "payload-b"); !errors.Is(err, ErrMessageDedupConflict) {
		t.Fatalf("in-flight conflict error=%v", err)
	}
	leader.Complete(nil)
	if _, _, err := dedup.Acquire(context.Background(), "same-id", "payload-b"); !errors.Is(err, ErrMessageDedupConflict) {
		t.Fatalf("completed conflict error=%v", err)
	}
	if _, duplicate, err := dedup.Acquire(context.Background(), "same-id", "payload-a"); err != nil || !duplicate {
		t.Fatalf("matching duplicate=%t err=%v", duplicate, err)
	}
}

func TestMessageDedupActiveReservationDoesNotExpireOrEvict(t *testing.T) {
	dedup := NewMessageDedup(10*time.Millisecond, 1)
	leader, _, err := dedup.Acquire(context.Background(), "slow", "payload")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	followerDone := make(chan error, 1)
	go func() {
		_, duplicate, err := dedup.Acquire(context.Background(), "slow", "payload")
		if err == nil && !duplicate {
			err = errors.New("follower became a second leader")
		}
		followerDone <- err
	}()
	select {
	case err := <-followerDone:
		t.Fatalf("active reservation expired: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if _, _, err := dedup.Acquire(context.Background(), "other", "payload"); !errors.Is(err, ErrMessageDedupCapacity) {
		t.Fatalf("capacity error=%v", err)
	}
	leader.Complete(nil)
	if err := <-followerDone; err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if next, duplicate, err := dedup.Acquire(context.Background(), "other", "payload"); err != nil || duplicate || next == nil {
		t.Fatalf("next=%#v duplicate=%t err=%v", next, duplicate, err)
	}
}

func TestMessageDedupTryRecordCannotReplaceActiveReservation(t *testing.T) {
	dedup := NewMessageDedup(time.Minute, 10)
	leader, duplicate, err := dedup.Acquire(context.Background(), "mixed", "payload")
	if err != nil || duplicate || leader == nil {
		t.Fatalf("leader=%#v duplicate=%t err=%v", leader, duplicate, err)
	}
	dedup.mu.Lock()
	leader.entry.seenAt = time.Now().Add(-2 * time.Minute)
	dedup.mu.Unlock()

	followerDone := make(chan error, 1)
	go func() {
		_, duplicate, err := dedup.Acquire(context.Background(), "mixed", "payload")
		if err == nil && !duplicate {
			err = errors.New("follower became leader")
		}
		followerDone <- err
	}()
	if dedup.TryRecord("mixed") {
		t.Fatal("legacy TryRecord replaced an active reservation")
	}
	leader.Complete(nil)
	if err := <-followerDone; err != nil {
		t.Fatal(err)
	}
}

func TestCompleteMessageDedupReservationFailsThenRepanics(t *testing.T) {
	dedup := NewMessageDedup(time.Minute, 10)
	leader, _, err := dedup.Acquire(context.Background(), "panic", "payload")
	if err != nil {
		t.Fatal(err)
	}
	followerDone := make(chan error, 1)
	followerStarted := make(chan struct{})
	go func() {
		close(followerStarted)
		_, _, err := dedup.Acquire(context.Background(), "panic", "payload")
		followerDone <- err
	}()
	<-followerStarted
	select {
	case err := <-followerDone:
		t.Fatalf("follower returned before leader panic: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	func() {
		var processingErr error
		defer func() {
			if recovered := recover(); recovered != "boom" {
				t.Fatalf("recovered=%#v", recovered)
			}
		}()
		defer CompleteMessageDedupReservation(leader, &processingErr)
		panic("boom")
	}()
	if err := <-followerDone; !errors.Is(err, ErrMessageDedupPanic) {
		t.Fatalf("follower error=%v", err)
	}
	retry, duplicate, err := dedup.Acquire(context.Background(), "panic", "payload")
	if err != nil || duplicate || retry == nil {
		t.Fatalf("retry=%#v duplicate=%t err=%v", retry, duplicate, err)
	}
}

func TestMessageDedupAllowsSameIDAfterTTL(t *testing.T) {
	dedup := NewMessageDedup(50*time.Millisecond, 100)

	if !dedup.TryRecord("msg-1") {
		t.Fatal("first msg-1 should be new")
	}
	if dedup.TryRecord("msg-1") {
		t.Fatal("second msg-1 should be duplicate")
	}
	time.Sleep(70 * time.Millisecond)
	if !dedup.TryRecord("msg-1") {
		t.Fatal("msg-1 should be new after TTL")
	}
}

func TestMessageDedupEvictsOldestAtCapacity(t *testing.T) {
	dedup := NewMessageDedup(time.Minute, 3)

	for _, id := range []string{"a", "b", "c"} {
		if !dedup.TryRecord(id) {
			t.Fatalf("%s should be new", id)
		}
	}
	if !dedup.TryRecord("d") {
		t.Fatal("d should be new")
	}
	if !dedup.TryRecord("a") {
		t.Fatal("a should be new after oldest eviction")
	}
	if dedup.TryRecord("c") {
		t.Fatal("c should still be deduplicated")
	}
}

func TestMessageDedupForgetAllowsDurabilityRepair(t *testing.T) {
	dedup := NewMessageDedup(time.Minute, 10)
	if !dedup.TryRecord("event-repair") {
		t.Fatal("initial reservation was rejected")
	}
	dedup.Forget("event-repair")
	if !dedup.TryRecord("event-repair") {
		t.Fatal("failed durable processing remained deduplicated")
	}
}
