package mcpstdio

import (
	"math"
	"strings"
)

const defaultDiscoveryPageSize = 100

var discoverySizeParameters = []string{
	"page_size", "pageSize", "max_results", "maxResults", "max_records", "maxRecords",
	"page_limit", "pageLimit", "results_per_page", "resultsPerPage", "num_results", "numResults",
	"max_rows", "maxRows", "max_items", "maxItems", "retmax",
	"max_records_returned", "maxRecordsReturned", "max_rows_returned", "maxRowsReturned",
	"max_sample_rows_returned", "maxSampleRowsReturned", "rows_per_page", "rowsPerPage",
	"per_page", "perPage", "result_limit", "resultLimit", "record_limit", "recordLimit",
	"max_samples", "maxSamples", "max_genes", "maxGenes", "limit", "size",
}

var unambiguousDiscoverySizeParameters = map[string]bool{
	"page_size": true, "pageSize": true, "max_results": true, "maxResults": true,
	"page_limit": true, "pageLimit": true, "results_per_page": true, "resultsPerPage": true,
	"num_results": true, "numResults": true,
	"max_records": true, "maxRecords": true, "max_rows": true, "maxRows": true,
	"max_items": true, "maxItems": true, "retmax": true,
	"max_records_returned": true, "maxRecordsReturned": true,
	"max_rows_returned": true, "maxRowsReturned": true,
	"max_sample_rows_returned": true, "maxSampleRowsReturned": true,
	"rows_per_page": true, "rowsPerPage": true, "per_page": true, "perPage": true,
	"result_limit": true, "resultLimit": true, "record_limit": true, "recordLimit": true,
	"max_samples": true, "maxSamples": true, "max_genes": true, "maxGenes": true,
}

// applyDiscoveryDefaults gives discovery tools a useful first page when the
// caller did not choose a page size. It runs at the MCP transport boundary,
// after tools/list and before schema validation/tools/call, so direct dynamic
// tools, the generic MCP tool, workspace connectors, and kernel-host calls all
// share one policy. Explicit caller choices and the live schema's bounds remain
// authoritative.
func applyDiscoveryDefaults(tool ToolProjection, input map[string]any) map[string]any {
	properties, _ := tool.InputSchema["properties"].(map[string]any)
	if len(properties) == 0 {
		return input
	}
	if !isDiscoveryMethod(tool.ToolName) &&
		!(hasUnambiguousDiscoverySizeParameter(properties) && hasDiscoveryQueryParameter(properties)) {
		return input
	}
	for _, name := range discoverySizeParameters {
		if _, explicit := input[name]; explicit {
			return input
		}
	}
	for _, name := range discoverySizeParameters {
		property, _ := properties[name].(map[string]any)
		if len(property) == 0 || !isNumericSchema(property) {
			continue
		}
		value := defaultDiscoveryPageSize
		if minimum, ok := schemaInteger(property["minimum"]); ok && value < minimum {
			value = minimum
		}
		if maximum, ok := schemaInteger(property["maximum"]); ok && value > maximum {
			value = maximum
		}
		if value < 1 {
			return input
		}
		result := cloneInput(input)
		result[name] = value
		return result
	}
	return input
}

func hasUnambiguousDiscoverySizeParameter(properties map[string]any) bool {
	for name := range properties {
		if unambiguousDiscoverySizeParameters[name] {
			return true
		}
	}
	return false
}

func hasDiscoveryQueryParameter(properties map[string]any) bool {
	for _, name := range []string{"query", "q", "term", "search_query", "searchQuery", "keyword", "keywords", "filter", "filters", "text"} {
		if _, exists := properties[name]; exists {
			return true
		}
	}
	return false
}

func isDiscoveryMethod(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, marker := range []string{"search", "find", "list", "discover", "query"} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}

func isNumericSchema(schema map[string]any) bool {
	typeName, _ := schema["type"].(string)
	typeName = strings.ToLower(strings.TrimSpace(typeName))
	return typeName == "integer" || typeName == "number"
}

func schemaInteger(value any) (int, bool) {
	var number float64
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	default:
		return 0, false
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || number != math.Trunc(number) {
		return 0, false
	}
	return int(number), true
}

func cloneInput(input map[string]any) map[string]any {
	cloned := make(map[string]any, len(input)+1)
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func replaceInput(target, source map[string]any) {
	for key := range target {
		delete(target, key)
	}
	for key, value := range source {
		target[key] = value
	}
}
