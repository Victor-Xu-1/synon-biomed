package providers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

func readNativeSSE(reader io.Reader, limit int64, dispatch func(string, []byte) error) error {
	if limit <= 0 {
		limit = defaultMaxResponseBytes
	}
	eventLimit := limit
	buffered := bufio.NewReader(reader)
	eventName := ""
	data := make([]string, 0, 1)
	var eventBytes int64
	var semanticBytes int64
	var semanticToolCall bool
	flush := func() error {
		if len(data) == 0 {
			eventName = ""
			eventBytes = 0
			return nil
		}
		raw := strings.TrimSpace(strings.Join(data, "\n"))
		data = data[:0]
		name := strings.TrimSpace(eventName)
		eventName = ""
		eventBytes = 0
		if raw == "" || raw == "[DONE]" {
			return nil
		}
		semantics := nativeSSEEventSemantics([]byte(raw))
		if semantics.Bytes > limit-semanticBytes {
			if semantics.HasToolCall || semantics.ExplicitTerminal {
				return fmt.Errorf("%w: decoded response exceeded %d bytes", errProviderResponseTooLarge, limit)
			}
			return newProviderSemanticSegmentBoundary(limit)
		}
		if semantics.TruncatedReason != "" {
			limited := responseOutputLimit(semantics.TruncatedReason, semanticToolCall || semantics.HasToolCall)
			if !semanticToolCall && !semantics.HasToolCall {
				// A final native chunk may contain both text and its stop reason.
				// Let the protocol decoder retain that text before returning the
				// interruption, while preserving cancellation or parsing errors.
				if dispatchErr := dispatch(name, []byte(raw)); dispatchErr != nil && !IsProviderOutputTokenLimit(dispatchErr) {
					return dispatchErr
				}
			}
			return withOutputLimitDetails(limited, 0, "", nativeTerminalUsage([]byte(raw)))
		}
		semanticBytes += semantics.Bytes
		if semantics.Bytes > 0 || semantics.ExplicitTerminal {
			recordProviderStreamProgress(reader)
		}
		semanticToolCall = semanticToolCall || semantics.HasToolCall
		return dispatch(name, []byte(raw))
	}
	for {
		line, readErr := readProviderSSELine(buffered, eventLimit)
		if line != "" {
			if int64(len(line)) > eventLimit-eventBytes {
				return fmt.Errorf("%w: SSE event exceeded %d bytes", errProviderResponseTooLarge, eventLimit)
			}
			eventBytes += int64(len(line))
		}
		switch {
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		case len(data) > 0 && !strings.HasPrefix(line, ":"):
			data = append(data, line)
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return readErr
			}
			return flush()
		}
	}
}

func readProviderSSELine(reader *bufio.Reader, limit int64) (string, error) {
	var line bytes.Buffer
	for {
		fragment, isPrefix, err := reader.ReadLine()
		if int64(len(fragment)) > limit-int64(line.Len()) {
			return "", fmt.Errorf("%w: SSE event line exceeded %d bytes", errProviderResponseTooLarge, limit)
		}
		_, _ = line.Write(fragment)
		if err != nil {
			return line.String(), err
		}
		if !isPrefix {
			return line.String(), nil
		}
	}
}

type nativeSSEEventSemanticInfo struct {
	Bytes            int64
	TruncatedReason  string
	HasToolCall      bool
	ExplicitTerminal bool
}

func nativeSSEEventSemantics(raw []byte) nativeSSEEventSemanticInfo {
	var event struct {
		Type         string `json:"type"`
		ContentBlock struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Text  string          `json:"text"`
			Input json.RawMessage `json:"input"`
		} `json:"content_block"`
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Thinking    string `json:"thinking"`
			Signature   string `json:"signature"`
			PartialJSON string `json:"partial_json"`
			StopReason  string `json:"stop_reason"`
		} `json:"delta"`
		Candidates []struct {
			FinishReason string `json:"finishReason"`
			Content      struct {
				Parts []struct {
					Text         string `json:"text"`
					FunctionCall *struct {
						Name string `json:"name"`
						Args any    `json:"args"`
					} `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if json.Unmarshal(raw, &event) != nil {
		return nativeSSEEventSemanticInfo{}
	}
	var semantics nativeSSEEventSemanticInfo
	eventType := strings.TrimSpace(event.Type)
	semantics.ExplicitTerminal = eventType == "message_stop"
	switch eventType {
	case "content_block_start":
		semantics.Bytes += int64(len(event.ContentBlock.Text))
		if event.ContentBlock.Type == "tool_use" {
			semantics.HasToolCall = true
			semantics.Bytes += int64(len(strings.TrimSpace(event.ContentBlock.ID)) + len(event.ContentBlock.Name) + len(event.ContentBlock.Input))
		}
	case "content_block_delta":
		semantics.Bytes += int64(len(event.Delta.Text) + len(event.Delta.PartialJSON) + len(event.Delta.Thinking) + len(event.Delta.Signature))
		semantics.HasToolCall = strings.TrimSpace(event.Delta.Type) == "input_json_delta" || event.Delta.PartialJSON != ""
	}
	if stopReason := strings.TrimSpace(event.Delta.StopReason); stopReason != "" {
		semantics.ExplicitTerminal = true
		if isProviderStreamTruncatedReason(stopReason) {
			semantics.TruncatedReason = stopReason
			return semantics
		}
	}
	for _, candidate := range event.Candidates {
		if finishReason := strings.TrimSpace(candidate.FinishReason); finishReason != "" {
			semantics.ExplicitTerminal = true
			if isProviderStreamTruncatedReason(finishReason) {
				semantics.TruncatedReason = finishReason
				return semantics
			}
		}
		for _, part := range candidate.Content.Parts {
			semantics.Bytes += int64(len(part.Text))
			if part.FunctionCall == nil {
				continue
			}
			semantics.HasToolCall = true
			semantics.Bytes += int64(len(part.FunctionCall.Name))
			if part.FunctionCall.Args != nil {
				if encoded, err := json.Marshal(part.FunctionCall.Args); err == nil {
					semantics.Bytes += int64(len(encoded))
				}
			}
		}
	}
	return semantics
}

func isProviderStreamTruncatedReason(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "length", "max_tokens", "max_output_tokens":
		return true
	default:
		return false
	}
}
