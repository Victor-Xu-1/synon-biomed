package server

import (
	"context"
	"errors"
	"strings"
	"unicode"

	kernelruntime "synon-go/internal/kernel"
)

// resolveKernelMCPHostTargetWithCompatibleMethod keeps exact catalog identity
// as the default. For read-only tools only, it can collapse an over-specific
// invented method name to one catalog method when the catalog tokens are a
// strict subset of the request and the supplied input validates against that
// method. Ambiguous, mutating, typo-only, and schema-incompatible guesses still
// fail closed and direct the model to host.mcp.list_methods.
func (s *Server) resolveKernelMCPHostTargetWithCompatibleMethod(
	ctx context.Context,
	frameID, ownerID, serverName, requestedMethod string,
	input map[string]any,
) (kernelMCPResolution, string, error) {
	resolution, err := s.resolveKernelMCPHostTarget(ctx, frameID, ownerID, serverName, requestedMethod)
	if err == nil {
		return resolution, resolution.tool.ToolName, nil
	}
	var hostErr *kernelruntime.HostCallError
	if !errors.As(err, &hostErr) || hostErr.Code != "unknown_method" {
		return kernelMCPResolution{}, "", err
	}

	runtimeContext, found, contextErr := s.workspaceMCPRuntimeContextWithContext(ctx, frameID)
	if contextErr != nil || !found || runtimeContext.UserID != ownerID {
		return kernelMCPResolution{}, "", err
	}
	requestedTokens := kernelMCPMethodTokens(requestedMethod)
	if len(requestedTokens) < 3 {
		return kernelMCPResolution{}, "", err
	}
	matches := make([]kernelMCPResolution, 0, 1)
	for _, connector := range kernelMCPConnectorMatches(runtimeContext.Connectors, strings.TrimSpace(serverName)) {
		tools, listErr := s.workspaceMCPRuntimeConnectorToolsStable(ctx, runtimeContext.UserID, connector)
		if listErr != nil {
			continue
		}
		for _, tool := range tools {
			if !tool.ReadOnlyHint || runtimeContext.workspaceMCPToolExcluded(connector.ID, tool.ToolName) ||
				!kernelMCPMethodIsStrictSubset(tool.ToolName, requestedTokens) {
				continue
			}
			candidate, resolveErr := s.resolveKernelMCPHostTarget(ctx, frameID, ownerID, serverName, tool.ToolName)
			if resolveErr != nil {
				continue
			}
			validator, compileErr := compileKernelMCPInputValidator(ctx, kernelMCPOracleInputSchema(candidate.tool.InputSchema))
			normalized := normalizeAgentRuntimeMCPArguments(candidate.tool.Name, candidate.tool.InputSchema, input)
			if compileErr != nil || validator.Validate(normalized) != nil {
				continue
			}
			matches = append(matches, candidate)
		}
	}
	if len(matches) != 1 {
		return kernelMCPResolution{}, "", err
	}
	return matches[0], matches[0].tool.ToolName, nil
}

func kernelMCPMethodTokens(name string) map[string]struct{} {
	tokens := map[string]struct{}{}
	for _, token := range strings.FieldsFunc(strings.ToLower(strings.TrimSpace(name)), func(value rune) bool {
		return !unicode.IsLetter(value) && !unicode.IsDigit(value)
	}) {
		if token != "" {
			tokens[token] = struct{}{}
		}
	}
	return tokens
}

func kernelMCPMethodIsStrictSubset(candidate string, requested map[string]struct{}) bool {
	candidateTokens := kernelMCPMethodTokens(candidate)
	if len(candidateTokens) < 2 || len(candidateTokens) >= len(requested) {
		return false
	}
	for token := range candidateTokens {
		if _, ok := requested[token]; !ok {
			return false
		}
	}
	return len(requested)-len(candidateTokens) <= 2
}
