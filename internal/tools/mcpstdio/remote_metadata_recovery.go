package mcpstdio

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"time"
)

// Transport EOF before/during a metadata response is safe to replay once.
// This is deliberately not a general tools/call retry: an unknown tool may
// already have produced side effects even when its connection disappeared.
func remoteHTTPRPCWithOptions(ctx context.Context, root string, config ServerConfig, sessionID *string, id int, method string, params any, options remoteHTTPRPCOptions) (json.RawMessage, error) {
	result, err := remoteHTTPRPCOnce(ctx, root, config, sessionID, id, method, params, options)
	if cause := context.Cause(ctx); cause != nil {
		return nil, cause
	}
	metadata := method == "tools/list" || method == "resources/list" || method == "prompts/list"
	if err == nil || !metadata || (!errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF)) {
		return result, err
	}
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case <-timer.C:
	}
	log.Printf("remote_mcp_metadata_retry method=%q attempt=2 reason=transport_eof", method)
	result, err = remoteHTTPRPCOnce(ctx, root, config, sessionID, id, method, params, options)
	if cause := context.Cause(ctx); cause != nil {
		return nil, cause
	}
	return result, err
}

// Preserve machine-readable transport identity without exposing a raw URL
// or credential echo through Error(). Only internal error classification
// unwraps the original transport error.
type remoteMCPTransportError struct {
	message string
	cause   error
}

func (e *remoteMCPTransportError) Error() string { return e.message }
func (e *remoteMCPTransportError) Unwrap() error { return e.cause }
