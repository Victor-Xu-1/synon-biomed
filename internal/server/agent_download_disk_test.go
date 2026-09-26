package server

import (
	"bytes"
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

func TestDownloadCapacityAllowsLargeFilesButPreservesPhysicalReserve(t *testing.T) {
	free := uint64(64 << 30)
	guard := agentDownloadDiskGuard{paths: []string{"workspace", "artifacts"}, available: func(string) *uint64 { return &free }}
	for _, size := range []int64{5 << 30, 10 << 30, 20 << 30} {
		if (agentPublicScientificFileRequest{}).maximumBytes() < size {
			t.Fatalf("artificial size cap still rejects %d bytes", size)
		}
		if err := guard.check(0, size); err != nil {
			t.Fatalf("large transfer rejected with sufficient capacity: %v", err)
		}
	}
	free = agentPublicScientificDiskReserve + 3*(10<<30) - 1
	if !errors.Is(guard.check(0, 10<<30), errAgentPublicScientificFileDiskSpace) {
		t.Fatal("publication copies were not included in disk budget")
	}
	free = math.MaxUint64
	if err := guard.check(0, math.MaxInt64); err == nil {
		t.Fatal("malicious size overflow bypassed disk accounting")
	}
	guard.available = func(string) *uint64 { return nil }
	if err := guard.check(0, 1); err == nil {
		t.Fatal("unknown capacity was treated as infinite disk")
	}
}

func TestDownloadDiskWriterRechecksDecliningCapacityAndKeepsPrefix(t *testing.T) {
	var data bytes.Buffer
	free := uint64(64 << 30)
	w := &agentDownloadDiskWriter{writer: &data, guard: agentDownloadDiskGuard{
		paths: []string{"workspace"}, available: func(string) *uint64 { return &free },
	}}
	if _, err := w.Write([]byte("verified prefix")); err != nil {
		t.Fatal(err)
	}
	free = agentPublicScientificDiskReserve
	if n, err := w.Write([]byte("unsafe suffix")); n != 0 || !errors.Is(err, errAgentPublicScientificFileDiskSpace) {
		t.Fatalf("write=%d error=%v", n, err)
	}
	if data.String() != "verified prefix" {
		t.Fatal("low disk destroyed prior partial bytes")
	}
	if err := ensureAgentPublicScientificDiskSpace(t.TempDir(), "", -1, agentPublicScientificFileLimit); err != nil {
		t.Fatalf("unknown content length required imaginary maximum capacity: %v", err)
	}
}

func TestDownloadStageLockCancellationDoesNotCompeteWithActiveTransfer(t *testing.T) {
	key := t.Name()
	release, err := acquireAgentPublicScientificStageLock(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		unlock, err := acquireAgentPublicScientificStageLock(ctx, key)
		if unlock != nil {
			unlock()
		}
		result <- err
	}()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel waits behind active download")
	}
	release()
	agentPublicScientificStageLocks.mu.Lock()
	_, leaked := agentPublicScientificStageLocks.locks[key]
	agentPublicScientificStageLocks.mu.Unlock()
	if leaked {
		t.Fatal("stage lock leaked")
	}
}
