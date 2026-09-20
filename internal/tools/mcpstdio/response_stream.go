package mcpstdio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"os"

	"synon-go/internal/runtimecontrol"
)

const mcpResponseBufferBytes = 64 << 10

// A transport frame may exceed the inline memory budget. Preserve it in a
// private spool under the same measured disk guard as durable evidence before
// decoding. JSON object materialization still costs memory proportional to the
// result; this is a bounded transport buffer, not a constant-memory JSON API.
type mcpResponseBuffer struct {
	ctx    context.Context
	inline bytes.Buffer
	file   *os.File
	writer io.Writer
	size   int64
}

func (b *mcpResponseBuffer) Write(data []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	if b.file == nil && len(data) <= mcpResponseBufferBytes-b.inline.Len() {
		n, err := b.inline.Write(data)
		b.size += int64(n)
		return n, err
	}
	if b.file == nil {
		file, err := os.CreateTemp("", "synon-mcp-response-*")
		if err != nil {
			return 0, err
		}
		b.file = file
		b.writer = runtimecontrol.DiskCapacityWriter(b.ctx, file, file.Name())
		if _, err := b.writer.Write(b.inline.Bytes()); err != nil {
			return 0, err
		}
		b.inline.Reset()
	}
	n, err := b.writer.Write(data)
	b.size += int64(n)
	return n, err
}

func (b *mcpResponseBuffer) reader() (io.ReadSeeker, error) {
	if b.file == nil {
		return bytes.NewReader(b.inline.Bytes()), nil
	}
	_, err := b.file.Seek(0, io.SeekStart)
	return b.file, err
}

func (b *mcpResponseBuffer) close() {
	if b.file != nil {
		_ = b.file.Close()
		_ = os.Remove(b.file.Name())
	}
}

func readMCPResponseLine(ctx context.Context, source *bufio.Reader) (*mcpResponseBuffer, error) {
	result := &mcpResponseBuffer{ctx: ctx}
	for {
		if err := ctx.Err(); err != nil {
			result.close()
			return nil, err
		}
		part, readErr := source.ReadSlice('\n')
		if _, err := result.Write(part); err != nil {
			result.close()
			return nil, err
		}
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		if readErr != nil && !(errors.Is(readErr, io.EOF) && result.size > 0) {
			result.close()
			return nil, readErr
		}
		return result, nil
	}
}

func decodeMCPJSON(source io.Reader, target any) error {
	decoder := json.NewDecoder(source)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("MCP response contains multiple JSON values")
	}
	return nil
}

func readMCPStdioMessage(ctx context.Context, reader *bufio.Reader, sdk bool) (rpcMessage, error) {
	for {
		line, err := readMCPResponseLine(ctx, reader)
		if err != nil {
			return rpcMessage{}, err
		}
		result, err := func() (rpcMessage, error) {
			defer line.close()
			source, err := line.reader()
			if err != nil {
				return rpcMessage{}, err
			}
			if sdk {
				msg, _, err := decodeSDKBridgeReader(source)
				return msg, err
			}
			var result rpcMessage
			err = decodeMCPJSON(source, &result)
			return result, err
		}()
		if errors.Is(err, io.EOF) {
			continue
		} // Blank stdout lines are not a closed transport.
		return result, err
	}
}

func decodeSDKBridgeReader(reader io.Reader) (rpcMessage, bool, error) {
	var raw json.RawMessage
	if err := decodeMCPJSON(reader, &raw); err != nil {
		return rpcMessage{}, false, err
	}
	return decodeSDKBridgeLine(raw)
}

func decodeMCPResponseStream(ctx context.Context, contentType string, source io.Reader) (rpcMessage, error) {
	media, _, _ := mime.ParseMediaType(contentType)
	if media == "text/event-stream" {
		return decodeMCPEventStream(ctx, bufio.NewReaderSize(source, mcpResponseBufferBytes))
	}
	buffer := &mcpResponseBuffer{ctx: ctx}
	defer buffer.close()
	if _, err := io.CopyBuffer(buffer, source, make([]byte, mcpResponseBufferBytes)); err != nil {
		return rpcMessage{}, err
	}
	reader, err := buffer.reader()
	if err != nil {
		return rpcMessage{}, err
	}
	var message rpcMessage
	err = decodeMCPJSON(reader, &message)
	return message, err
}

func decodeMCPEventStream(ctx context.Context, source *bufio.Reader) (rpcMessage, error) {
	data := &mcpResponseBuffer{ctx: ctx}
	defer data.close()
	for {
		line, err := readMCPResponseLine(ctx, source)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return rpcMessage{}, err
		}
		reader, err := line.reader()
		if err != nil {
			line.close()
			return rpcMessage{}, err
		}
		var prefix [5]byte
		n, readErr := io.ReadFull(reader, prefix[:])
		blank := line.size <= 2 && len(bytes.TrimSpace(prefix[:n])) == 0
		if readErr == nil && string(prefix[:]) == "data:" {
			_, err = io.CopyBuffer(data, reader, make([]byte, mcpResponseBufferBytes))
		}
		line.close()
		if err != nil {
			return rpcMessage{}, err
		}
		if blank && data.size > 0 {
			break
		}
	}
	if data.size == 0 {
		return rpcMessage{}, errors.New("remote MCP SSE response did not contain a data event")
	}
	reader, err := data.reader()
	if err != nil {
		return rpcMessage{}, err
	}
	var message rpcMessage
	err = decodeMCPJSON(reader, &message)
	return message, err
}
