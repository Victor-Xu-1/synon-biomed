package runtimecontrol

import (
	"context"
	"errors"
	"io"
	"syscall"
)

// DiskFreeReserve preserves space for database journals and recovery while
// artifacts are streamed. It is a free-space floor, not an artifact-size cap.
const DiskFreeReserve = uint64(1 << 30)

var ErrInsufficientDiskSpace = errors.New("insufficient local disk space for artifact write")

// All governed artifact writes share this short check/write critical section.
// Checking then writing independently allows simultaneous writers to consume
// the same measured headroom. External writers are observed again every chunk;
// an OS write error is still authoritative and must not be suppressed.
var diskWriteSlot = make(chan struct{}, 1)

type guardedDiskWriter struct {
	ctx    context.Context
	writer io.Writer
	check  func(int64) error
}

// GuardDiskWrites applies a caller's capacity accounting to bounded chunks.
// The check executes immediately before the corresponding write, under the
// process-wide artifact write gate. It must not itself write through this API.
func GuardDiskWrites(ctx context.Context, writer io.Writer, check func(int64) error) io.Writer {
	if ctx == nil {
		ctx = context.Background()
	}
	return &guardedDiskWriter{ctx: ctx, writer: writer, check: check}
}

// DiskCapacityWriter measures the filesystem containing the actual destination
// on every write. Staging on another volume is therefore checked independently.
// A failed copy can be cleaned up without publishing metadata or claiming a
// complete artifact; capacity for future copies is never promised from a stale
// initial measurement.
func DiskCapacityWriter(ctx context.Context, writer io.Writer, path string) io.Writer {
	return GuardDiskWrites(ctx, writer, func(next int64) error {
		return CheckDiskCapacity(path, next)
	})
}

// CheckDiskCapacity is a current measurement, not a reservation. Database or
// parser scratch writers use it between bounded operations and still retain
// their real write errors if another process consumes capacity afterwards.
func CheckDiskCapacity(path string, next int64) error {
	available := AvailableBytes(path)
	if next < 0 || available == nil || *available < DiskFreeReserve || uint64(next) > *available-DiskFreeReserve {
		return ErrInsufficientDiskSpace
	}
	return nil
}

func (w *guardedDiskWriter) Write(data []byte) (int, error) {
	const chunkBytes = 1 << 20
	total := 0
	for len(data) > 0 {
		if err := w.ctx.Err(); err != nil {
			return total, err
		}
		chunk := data[:min(len(data), chunkBytes)]
		n, err := w.guardedChunk(chunk)
		total += n
		if err != nil {
			return total, err
		}
		data = data[n:]
	}
	return total, nil
}

func (w *guardedDiskWriter) guardedChunk(data []byte) (int, error) {
	select {
	case diskWriteSlot <- struct{}{}:
	case <-w.ctx.Done():
		return 0, w.ctx.Err()
	}
	defer func() { <-diskWriteSlot }()
	return w.writeChunk(data)
}

func (w *guardedDiskWriter) writeChunk(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if err := w.check(int64(len(data))); err != nil {
		return 0, err
	}
	n, err := w.writer.Write(data)
	if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) {
		err = errors.Join(ErrInsufficientDiskSpace, err)
	}
	if n < 0 || n > len(data) {
		return 0, errors.New("invalid disk writer byte count")
	}
	if n != len(data) && err == nil {
		err = io.ErrShortWrite
	}
	return n, err
}
