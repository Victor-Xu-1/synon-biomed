package mcpstdio

import (
	"io"
	"strings"
)

func (b *limitedBuffer) readFrom(reader io.Reader) {
	chunk := make([]byte, 4096)
	for {
		n, err := reader.Read(chunk)
		if n > 0 {
			b.Write(chunk[:n])
		}
		if err != nil {
			return
		}
	}
}

func (b *limitedBuffer) Write(data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := maxStderrBytes - len(b.data)
	if remaining <= 0 {
		b.truncated = true
		return
	}
	if len(data) > remaining {
		b.data = append(b.data, data[:remaining]...)
		b.truncated = true
		return
	}
	b.data = append(b.data, data...)
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	text := strings.TrimSpace(string(b.data))
	if b.truncated {
		return text + " [truncated]"
	}
	return text
}

type namedConfig struct {
	name   string
	config ServerConfig
}
