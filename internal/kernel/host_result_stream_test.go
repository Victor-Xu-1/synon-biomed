package kernel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type delayedHostResultEndWriter struct {
	io.WriteCloser
	delivered, release, closed chan struct{}
	once                       sync.Once
}

func (writer *delayedHostResultEndWriter) Write(value []byte) (int, error) {
	n, err := writer.WriteCloser.Write(value)
	if err == nil && bytes.Contains(value, []byte(`"type":"host_result_end"`)) {
		close(writer.delivered)
		select {
		case <-writer.release:
		case <-writer.closed:
		}
	}
	return n, err
}

func (writer *delayedHostResultEndWriter) Close() error {
	writer.once.Do(func() { close(writer.closed) })
	return writer.WriteCloser.Close()
}

func TestHostResultDeliveryDrainsBeforeCompletingCell(t *testing.T) {
	manager, worker := newHostCallTestSession(t, nil)
	writer := &delayedHostResultEndWriter{WriteCloser: worker.stdin, delivered: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	worker.stdin = writer
	defer writer.once.Do(func() { close(writer.closed) })
	policy := &HostCallPolicy{AllowedMethods: []string{"host.routine.status"}, Handler: func(context.Context, HostCall) (any, error) { return strings.Repeat("x", 5<<20), nil }}
	handle, err := manager.Submit(SubmitRequest{FrameID: "frame-host", KernelKind: "analysis", Language: "python", Environment: "python", ExecID: "host-drain", ToolUseID: "tool-host-drain", Code: "import host\nassert len(host.routine.status()) == 5*1024*1024\npreserved_value=42", Origin: "agent", Timeout: 5 * time.Second, InterruptGrace: time.Second, HostCalls: policy})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-writer.delivered:
	case <-time.After(3 * time.Second):
		t.Fatal("result terminal frame not delivered")
	}
	select {
	case outcome := <-handle.Done():
		t.Fatalf("cell completed before host writer settled: %#v", outcome)
	case <-time.After(500 * time.Millisecond):
	}
	close(writer.release)
	select {
	case outcome := <-handle.Done():
		if outcome.Err != nil || outcome.Response.Error != "" {
			t.Fatalf("drained result: %#v", outcome)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cell did not complete after writer drained")
	}
	outcome := executeHostCallCell(t, manager, "after-host-drain", "assert preserved_value == 42\nprint('preserved')", nil)
	if outcome.Err != nil || outcome.Response.Error != "" || strings.TrimSpace(outcome.Response.Stdout) != "preserved" {
		t.Fatalf("healthy worker state was lost: %#v", outcome)
	}
}

func TestHostResultStreamRejectsCorruptFramesAndCleansPythonSpool(t *testing.T) {
	for _, fault := range []string{"call", "cell", "offset", "short", "digest", "encoding", "envelope"} {
		t.Run(fault, func(t *testing.T) {
			manager, worker := newHostCallTestSession(t, nil)
			policy := &HostCallPolicy{AllowedMethods: []string{"host.routine.status"}, Handler: func(ctx context.Context, call HostCall) (any, error) {
				wire := hostResultWire{Type: "host_result", ID: call.ID, CellID: call.CellID, OK: true, Result: map[string]any{"value": "tail"}}
				if fault == "envelope" {
					wire.CellID = "different-cell"
				}
				raw, _ := json.Marshal(wire)
				hash := sha256.Sum256(raw)
				digest := hex.EncodeToString(hash[:])
				send := func(frame map[string]any) {
					if _, present := frame["id"]; !present {
						frame["id"] = call.ID
					}
					if _, present := frame["cell_id"]; !present {
						frame["cell_id"] = call.CellID
					}
					encoded, _ := json.Marshal(frame)
					if err := worker.writeProtocol(encoded); err != nil {
						t.Error(err)
					}
				}
				send(map[string]any{"type": "host_result_start", "size_bytes": len(raw), "sha256": digest})
				chunk := map[string]any{"type": "host_result_chunk", "offset": 0, "data": raw}
				switch fault {
				case "call":
					chunk["id"] = "hc-00000000000000000000000000000000"
				case "cell":
					chunk["cell_id"] = "different-cell"
				case "offset":
					chunk["offset"] = 1
				case "encoding":
					chunk["data"] = "!!!invalid-base64"
				case "short":
					chunk["data"] = raw[:len(raw)-1]
				}
				send(chunk)
				if fault == "short" || fault == "digest" || fault == "envelope" {
					if fault == "digest" {
						digest = strings.Repeat("0", 64)
					}
					send(map[string]any{"type": "host_result_end", "size_bytes": len(raw), "sha256": digest})
				}
				return nil, nil
			}}
			outcome := executeHostCallCell(t, manager, "corrupt-result", `
import host, os
before = len(os.listdir('/proc/self/fd'))
try:
    host.routine.status()
    raise AssertionError('corrupt result accepted')
except (RuntimeError, ValueError):
    pass
assert len(os.listdir('/proc/self/fd')) == before, 'temporary descriptor leaked'
print('rejected-and-cleaned')
`, policy)
			if outcome.Err != nil || outcome.Response.Error != "" || strings.TrimSpace(outcome.Response.Stdout) != "rejected-and-cleaned" {
				t.Fatalf("corrupt frame outcome: %#v", outcome)
			}
		})
	}
}

func TestHostResultStreamCancellationUnblocksPipeAndCleansGoSpool(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	worker := &Worker{stdin: writer}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- worker.writeHostResultContext(ctx, HostCall{ID: "hc-0123456789abcdef0123456789abcdef", CellID: "cancelled-cell"}, map[string]any{"body": strings.Repeat("x", 8<<20)}, nil)
	}()
	// Read enough to observe that transport really started, then stop consuming
	// so the next bounded frame blocks in the real OS pipe.
	var prefix [64]byte
	if _, err := io.ReadFull(reader, prefix[:]); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled stream succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled stream retained a blocked writer")
	}
	paths, err := filepath.Glob(filepath.Join(os.TempDir(), "synon-host-result-*"))
	if err != nil || len(paths) != 0 {
		t.Fatalf("host transport spool leaked: %v %v", paths, err)
	}
}

func TestHostResultStreamShortPipeIsNotASuccess(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	defer writer.Close()
	worker := &Worker{stdin: writer}
	if err := worker.writeHostResultContext(context.Background(), HostCall{ID: "hc-0123456789abcdef0123456789abcdef", CellID: "closed-pipe"}, map[string]any{"body": strings.Repeat("x", 5<<20)}, nil); err == nil {
		t.Fatal("closed result pipe succeeded")
	}
}

func TestHostResultDeliveryDoesNotRejectImmediateNextCall(t *testing.T) {
	manager, _ := newHostCallTestSession(t, nil)
	policy := &HostCallPolicy{AllowedMethods: []string{"host.routine.status"}, MaxCalls: 512,
		Handler: func(context.Context, HostCall) (any, error) { return map[string]any{"value": 42}, nil }}
	outcome := executeHostCallCell(t, manager, "back-to-back-host", "import host\nfor i in range(400):\n assert host.routine.status()['value'] == 42\nprint('all-delivered')", policy)
	if outcome.Err != nil || outcome.Response.Error != "" || strings.TrimSpace(outcome.Response.Stdout) != "all-delivered" {
		t.Fatalf("back-to-back host outcome: %#v", outcome)
	}
}
