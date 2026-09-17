package agentruntime

// expandEngineToolSchemas keeps runtime discovery and the failed-call guard on
// one authoritative advertised-tool snapshot. The model loop only owns when
// expansion occurs; this helper owns how the expanded set is propagated.
func expandEngineToolSchemas(
	gateway ToolGateway,
	expansion ModelToolSchemaExpansion,
	enabled bool,
	current []ToolSchema,
) []ToolSchema {
	if !enabled {
		return current
	}
	current = appendUniqueToolSchemas(current, expansion.AdditionalModelToolSchemas(current)...)
	if guard, ok := gateway.(*failedToolCallGuard); ok {
		guard.updateToolSchemas(current)
	}
	return current
}
