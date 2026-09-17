package kernel

// canonicalCondaRuntimeName is the single compatibility boundary for previously
// persisted environment IDs. These literals remain readable; new catalogs use
// product-owned identifiers. Never hide them by encoding or changing old data.
func canonicalCondaRuntimeName(value string) string {
	switch value {
	case "claude-science-python":
		return "synon-biomed-python-baseline"
	case "claude-science-r":
		return "synon-biomed-r"
	default:
		return value
	}
}
