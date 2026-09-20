package runtimecontrol

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
)

type diskTestWriter func([]byte) (int, error)

func (write diskTestWriter) Write(data []byte) (int, error) { return write(data) }

func TestGuardDiskWritesConcurrentWritersCannotSpendSameCapacity(t *testing.T) {
	remaining := int64(17)
	check := func(next int64) error {
		if next > remaining {
			return ErrInsufficientDiskSpace
		}
		return nil
	}
	sink := diskTestWriter(func(data []byte) (int, error) { remaining -= int64(len(data)); return len(data), nil })
	var wait sync.WaitGroup
	errorsFound := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := GuardDiskWrites(context.Background(), sink, check).Write([]byte("12345678"))
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(errorsFound)
	successes := 0
	for err := range errorsFound {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrInsufficientDiskSpace) {
			t.Fatal(err)
		}
	}
	if successes != 2 || remaining != 1 {
		t.Fatalf("oversubscribed capacity: successes=%d remaining=%d", successes, remaining)
	}
}

func TestGuardDiskWritesCancellationWhileAnotherWriterOwnsGate(t *testing.T) {
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := GuardDiskWrites(context.Background(), diskTestWriter(func(p []byte) (int, error) { close(entered); <-release; return len(p), nil }), func(int64) error { return nil }).Write([]byte("x"))
		finished <- err
	}()
	<-entered
	defer func() {
		close(release)
		if err := <-finished; err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := GuardDiskWrites(ctx, io.Discard, func(int64) error { t.Error("cancelled check ran"); return nil }).Write([]byte("x"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestGuardDiskWritesRechecksEveryBoundedChunkAndKeepsPrefix(t *testing.T) {
	var saved bytes.Buffer
	checks := 0
	writer := GuardDiskWrites(context.Background(), &saved, func(next int64) error {
		checks++
		if next > 1<<20 {
			t.Fatal("unbounded write")
		}
		if checks == 2 {
			return ErrInsufficientDiskSpace
		}
		return nil
	})
	n, err := writer.Write(bytes.Repeat([]byte{'x'}, 3<<20))
	if n != 1<<20 || saved.Len() != n || !errors.Is(err, ErrInsufficientDiskSpace) {
		t.Fatalf("partial write=%d/%d err=%v", n, saved.Len(), err)
	}
	writer = GuardDiskWrites(context.Background(), diskTestWriter(func(p []byte) (int, error) { return len(p) - 1, nil }), func(int64) error { return nil })
	if _, err := writer.Write([]byte("xy")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write=%v", err)
	}
}
