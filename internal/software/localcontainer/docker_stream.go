package localcontainer

import (
	"bytes"
	"errors"
	"io"
	"sync"

	"synon-go/internal/processsupervisor"
)

const (
	dockerProgressFrameLimit  = 1 << 20
	dockerDiagnosticTailLimit = 64 << 10
)

// Non-streaming inventory requires complete JSON, not a silently truncated tail.
// A synchronized bound also permits safe diagnostics if process reaping fails.
type boundedDockerOutput struct {
	mu    sync.Mutex
	value []byte
}

func (b *boundedDockerOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.value)+len(p) > 8<<20 {
		return 0, errors.New("Docker command output exceeds the supported size")
	}
	b.value = append(b.value, p...)
	return len(p), nil
}

func (b *boundedDockerOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.value)
}

type dockerStreamCollector struct {
	mu            sync.Mutex
	callbackMu    sync.Mutex
	watchdog      *processsupervisor.InactivityWatchdog
	onLine        func(string)
	stdoutPending []byte
	stderrPending []byte
	stdoutTail    []byte
	stderrTail    []byte
}

type dockerStreamWriter struct {
	collector *dockerStreamCollector
	stderr    bool
}

func newDockerStreamCollector(
	watchdog *processsupervisor.InactivityWatchdog,
	onLine func(string),
) *dockerStreamCollector {
	return &dockerStreamCollector{watchdog: watchdog, onLine: onLine}
}

func (c *dockerStreamCollector) writer(stderr bool) io.Writer {
	return dockerStreamWriter{collector: c, stderr: stderr}
}

func (w dockerStreamWriter) Write(value []byte) (int, error) {
	if w.collector == nil {
		return len(value), nil
	}
	return w.collector.write(w.stderr, value)
}

func (c *dockerStreamCollector) write(stderr bool, value []byte) (int, error) {
	if c.watchdog != nil && len(value) > 0 {
		c.watchdog.MarkActivity()
	}
	c.mu.Lock()
	pending := &c.stdoutPending
	if stderr {
		pending = &c.stderrPending
	}
	*pending = append(*pending, value...)
	if len(*pending) > dockerProgressFrameLimit {
		c.mu.Unlock()
		return 0, errors.New("Docker progress frame exceeded the supported size")
	}
	lines := make([]string, 0, 1)
	for {
		index := bytes.IndexAny(*pending, "\r\n")
		if index < 0 {
			break
		}
		line := string((*pending)[:index])
		advance := index + 1
		if (*pending)[index] == '\r' && advance < len(*pending) && (*pending)[advance] == '\n' {
			advance++
		}
		*pending = append((*pending)[:0], (*pending)[advance:]...)
		c.recordLocked(stderr, line)
		lines = append(lines, line)
	}
	c.mu.Unlock()
	c.emit(lines)
	return len(value), nil
}

func (c *dockerStreamCollector) recordLocked(stderr bool, line string) {
	value := append([]byte(line), '\n')
	if stderr {
		c.stderrTail = appendBoundedDockerTail(c.stderrTail, value)
	} else {
		c.stdoutTail = appendBoundedDockerTail(c.stdoutTail, value)
	}
}

func (c *dockerStreamCollector) emit(lines []string) {
	if c == nil || c.onLine == nil || len(lines) == 0 {
		return
	}
	c.callbackMu.Lock()
	defer c.callbackMu.Unlock()
	for _, line := range lines {
		c.onLine(line)
	}
}

func (c *dockerStreamCollector) result() (string, string) {
	if c == nil {
		return "", ""
	}
	c.mu.Lock()
	lines := make([]string, 0, 2)
	if len(c.stdoutPending) > 0 {
		line := string(c.stdoutPending)
		c.recordLocked(false, line)
		lines = append(lines, line)
		c.stdoutPending = nil
	}
	if len(c.stderrPending) > 0 {
		line := string(c.stderrPending)
		c.recordLocked(true, line)
		lines = append(lines, line)
		c.stderrPending = nil
	}
	stdout, stderr := string(c.stdoutTail), string(c.stderrTail)
	c.mu.Unlock()
	c.emit(lines)
	return stdout, stderr
}

func appendBoundedDockerTail(current, value []byte) []byte {
	if len(value) >= dockerDiagnosticTailLimit {
		return append(current[:0], value[len(value)-dockerDiagnosticTailLimit:]...)
	}
	overflow := len(current) + len(value) - dockerDiagnosticTailLimit
	if overflow > 0 {
		copy(current, current[overflow:])
		current = current[:len(current)-overflow]
	}
	return append(current, value...)
}
