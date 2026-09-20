package kernel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"

	"synon-go/internal/runtimecontrol"
)

const hostResultChunkBytes = 64 << 10

// hostResultSpool keeps the wire frame budget separate from the result size.
// Only overflow uses a private transient file. Durable result ownership stays
// with the host handler and the existing tool-evidence store.
type hostResultSpool struct {
	ctx    context.Context
	inline bytes.Buffer
	file   *os.File
	writer io.Writer
}

func (s *hostResultSpool) Write(data []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	if s.file == nil && len(data) <= maxHostResultBytes-s.inline.Len() {
		return s.inline.Write(data)
	}
	if s.file == nil {
		file, err := os.CreateTemp("", "synon-host-result-*")
		if err != nil {
			return 0, err
		}
		s.file = file
		s.writer = runtimecontrol.DiskCapacityWriter(s.ctx, file, file.Name())
		if _, err := s.writer.Write(s.inline.Bytes()); err != nil {
			return 0, err
		}
		s.inline.Reset()
	}
	return s.writer.Write(data)
}

func (s *hostResultSpool) close() {
	if s.file != nil {
		_ = s.file.Close()
		_ = os.Remove(s.file.Name())
	}
}

func (w *Worker) writeHostResultContext(ctx context.Context, call HostCall, result any, callErr error) error {
	wire := hostResultWire{Type: "host_result", ID: call.ID, CellID: call.CellID, OK: callErr == nil, Result: result}
	if callErr != nil {
		wire.Result, wire.Error = nil, hostCallFailure(callErr)
	}
	// Cancellation already observed by a host handler must still reach Python
	// as the normal error response. A cancelled successful result is never sent.
	if ctx.Err() != nil {
		wire.OK, wire.Result, wire.Error = false, nil, hostCallFailure(ctx.Err())
	}
	if !wire.OK {
		payload, err := json.Marshal(wire)
		if err != nil {
			return err
		}
		return w.writeProtocol(payload)
	}
	spool := &hostResultSpool{ctx: ctx}
	defer spool.close()
	encoder := json.NewEncoder(spool)
	if err := encoder.Encode(wire); err != nil {
		var unsupportedType *json.UnsupportedTypeError
		var unsupportedValue *json.UnsupportedValueError
		var marshalError *json.MarshalerError
		if errors.As(err, &unsupportedType) || errors.As(err, &unsupportedValue) || errors.As(err, &marshalError) {
			return w.writeHostResult(call, nil, NewHostCallError("invalid_result", "host result is not JSON serializable"))
		}
		return err
	}
	if spool.file == nil {
		return w.writeProtocol(bytes.TrimSuffix(spool.inline.Bytes(), []byte{'\n'}))
	}
	if _, err := spool.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	digest := sha256.New()
	size, err := io.Copy(digest, spool.file)
	if err != nil {
		return err
	}
	if _, err := spool.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return w.writeHostResultStream(ctx, call, spool.file, size, hex.EncodeToString(digest.Sum(nil)))
}

func (w *Worker) writeHostResultStream(ctx context.Context, call HostCall, reader io.Reader, size int64, digest string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// An interrupted frame cannot be reused by a later cell. Closing the pipe
	// wakes a blocked writer, and the existing worker owner reaps this process.
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = w.stdin.Close()
		if w.process != nil {
			_ = w.process.kill()
		}
		close(stopped)
	})
	defer func() {
		if !stop() {
			<-stopped
		}
	}()
	send := func(frame map[string]any) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		frame["id"], frame["cell_id"] = call.ID, call.CellID
		payload, err := json.Marshal(frame)
		if err != nil {
			return err
		}
		return w.writeProtocol(payload)
	}
	if err := send(map[string]any{"type": "host_result_start", "size_bytes": size, "sha256": digest}); err != nil {
		return err
	}
	buffer := make([]byte, hostResultChunkBytes)
	var offset int64
	for offset < size {
		n, err := io.ReadFull(reader, buffer[:min(int64(len(buffer)), size-offset)])
		if err != nil {
			return err
		}
		if err := send(map[string]any{"type": "host_result_chunk", "offset": offset, "data": buffer[:n]}); err != nil {
			return err
		}
		offset += int64(n)
	}
	return send(map[string]any{"type": "host_result_end", "size_bytes": size, "sha256": digest})
}
