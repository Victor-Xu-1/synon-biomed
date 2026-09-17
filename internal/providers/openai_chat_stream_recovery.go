package providers

import (
	"context"
	"log"
	"strings"

	"synon-go/internal/agentruntime"
)

// No tool in the response has executed yet. A quiet, malformed tool response
// can retry the unchanged request through the provider's ordinary JSON path.
// Visible content is not replayed, and there is no recursive transport fallback.
func (c *streamingRuntimeModelClient) recoverOpenAIChatToolArguments(ctx context.Context, request agentruntime.ModelRequest, original agentruntime.ModelResponse) (agentruntime.ModelResponse, error) {
	if strings.TrimSpace(original.Message.Content) != "" || !hasProviderArgumentDiagnostic(original) {
		return original, nil
	}
	if err := ctx.Err(); err != nil {
		return agentruntime.ModelResponse{}, err
	}
	log.Printf("provider_tool_argument_transport_recovery action=nonstream_attempt")
	repaired, err := c.runtimeModelClient.Complete(ctx, request)
	if err != nil {
		if ctx.Err() != nil {
			return agentruntime.ModelResponse{}, ctx.Err()
		}
		// The original protocol feedback remains available to the agent. The
		// failed repair is separately audited by the same provider client.
		log.Printf("provider_tool_argument_transport_recovery outcome=unavailable error_type=%T", err)
		return original, nil
	}
	if hasProviderArgumentDiagnostic(repaired) {
		log.Printf("provider_tool_argument_transport_recovery outcome=invalid_arguments")
	} else {
		log.Printf("provider_tool_argument_transport_recovery outcome=recovered")
	}
	return repaired, nil
}

func hasProviderArgumentDiagnostic(response agentruntime.ModelResponse) bool {
	for _, call := range response.Message.ToolCalls {
		if strings.TrimSpace(call.ProviderProtocolDiagnostic) != "" {
			return true
		}
	}
	return false
}
