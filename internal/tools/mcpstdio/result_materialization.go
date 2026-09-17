package mcpstdio

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

func schemaProperties(schema map[string]any) []string {
	if schema == nil {
		return nil
	}
	rawProperties, ok := schema["properties"].(map[string]any)
	if !ok {
		return nil
	}
	properties := make([]string, 0, len(rawProperties))
	for property := range rawProperties {
		properties = append(properties, property)
	}
	sort.Strings(properties)
	return properties
}

func BuildToolName(serverName string, toolName string) string {
	return "mcp__" + NormalizeName(serverName) + "__" + NormalizeName(toolName)
}

func NormalizeName(name string) string {
	normalized := nameUnsafePattern.ReplaceAllString(name, "_")
	if strings.HasPrefix(name, "synon.ai ") {
		normalized = regexp.MustCompile(`_+`).ReplaceAllString(normalized, "_")
		normalized = strings.Trim(normalized, "_")
	}
	return normalized
}

func idMatches(value any, expected int) bool {
	switch typed := value.(type) {
	case float64:
		return int(typed) == expected
	case int:
		return typed == expected
	case string:
		return typed == fmt.Sprint(expected)
	default:
		return false
	}
}

func mustRawJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return raw
}

func stringField(data map[string]any, key string) string {
	if value, ok := data[key].(string); ok {
		return value
	}
	return ""
}

func formatToolCallResult(raw json.RawMessage) (string, error) {
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", err
	}
	content, _ := decoded["content"].([]any)
	parts := make([]string, 0, len(content))
	for _, rawPart := range content {
		part, ok := rawPart.(map[string]any)
		if !ok {
			continue
		}
		switch part["type"] {
		case "text":
			if text, ok := part["text"].(string); ok {
				parts = append(parts, text)
			}
		default:
			encoded, err := json.Marshal(part)
			if err == nil {
				parts = append(parts, string(encoded))
			}
		}
	}
	if isError, _ := decoded["isError"].(bool); isError {
		// Some read-only MCP connectors use the protocol error bit for an
		// expected lookup miss while still returning a complete structured
		// result (`found:false`, `... not found`). A miss is data that the model
		// can branch on; raising it as a transport exception aborts the rest of a
		// batched retrieval. Preserve genuine connector, authorization, timeout,
		// and execution errors as errors.
		if value, ok := semanticMCPNotFoundResult(parts); ok {
			return value, nil
		}
		if len(parts) == 0 {
			return "", errors.New("MCP tool returned an error")
		}
		return "", errors.New(strings.Join(parts, "\n"))
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n"), nil
	}
	return string(raw), nil
}

func semanticMCPNotFoundResult(parts []string) (string, bool) {
	if len(parts) != 1 {
		return "", false
	}
	var value map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(parts[0])), &value) != nil || boolValue(value["found"], true) {
		return "", false
	}
	status := strings.ToLower(strings.TrimSpace(fmt.Sprint(value["status"])))
	code := strings.ToLower(strings.TrimSpace(fmt.Sprint(value["code"])))
	errorText := strings.ToLower(strings.TrimSpace(fmt.Sprint(value["error"])))
	semanticMiss := status == "not_found" || status == "not found" || code == "not_found" || code == "not found" ||
		strings.Contains(errorText, "not found")
	if !semanticMiss || boolValue(value["sourceUnavailable"], false) || boolValue(value["retryable"], false) {
		return "", false
	}
	return parts[0], true
}

func boolValue(value any, fallback bool) bool {
	if typed, ok := value.(bool); ok {
		return typed
	}
	return fallback
}

func persistBlob(root string, serverName string, uri string, mimeType string, index int, blob string) (string, string, error) {
	data, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return "", "", err
	}
	if root == "" {
		return "", "", errors.New("file root is not configured")
	}
	dir := filepath.Join(root, "mcp-output")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		return "", "", err
	}
	name := fmt.Sprintf("%s-%s-%d-%s%s", NormalizeName(serverName), NormalizeName(uri), index, hex.EncodeToString(random), mimeExtension(mimeType))
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	rel = filepath.ToSlash(rel)
	return rel, fmt.Sprintf("Binary content saved to %s (%s, %d bytes).", rel, firstNonEmpty(mimeType, "application/octet-stream"), len(data)), nil
}

func mimeExtension(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0])) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "application/json":
		return ".json"
	default:
		return ".bin"
	}
}

func timeoutFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("SYNON_MCP_TIMEOUT_SECONDS"))
	if raw == "" {
		return defaultTimeout
	}
	parsed, err := time.ParseDuration(raw + "s")
	if err != nil || parsed <= 0 {
		return defaultTimeout
	}
	if parsed > 10*time.Minute {
		return 10 * time.Minute
	}
	return parsed
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
