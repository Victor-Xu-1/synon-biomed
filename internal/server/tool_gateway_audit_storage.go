package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	adaptercommon "synon-go/internal/adapters/common"
	runtimekv "synon-go/internal/persistence/runtimekv"
)

const (
	toolGatewayAuditFieldMaxBytes  = 8 * 1024
	toolGatewayAuditRecordMaxBytes = 32 * 1024
	toolGatewayAuditTextMaxBytes   = 2 * 1024
	toolGatewayAuditMaxEntries     = 256
)

func boundedToolGatewayAuditMap(value map[string]any) (map[string]any, bool) {
	bounded := make(map[string]any, len(value))
	changed := false
	for key, item := range value {
		next, itemChanged := boundedToolGatewayAuditField(item)
		switch key {
		case "origin", "tool", "toolCallId", "status", "id", "timeoutSource", "error":
			if text, ok := next.(string); ok {
				next = truncateUTF8ByBytes(text, toolGatewayAuditTextMaxBytes)
				itemChanged = itemChanged || next != text
			}
		}
		bounded[key] = next
		changed = changed || itemChanged
	}
	raw, err := json.Marshal(bounded)
	if err == nil && len(raw) > toolGatewayAuditRecordMaxBytes {
		digest := sha256.Sum256(raw)
		identity := map[string]any{}
		for _, key := range []string{
			"id", "origin", "tool", "toolCallId", "status", "startedAt", "completedAt", "durationMs",
			"durationMillis", "timeoutMs", "timeoutSource",
		} {
			if item, ok := bounded[key]; ok {
				identity[key] = item
			}
		}
		identity["details"] = map[string]any{
			"kind":   "omitted_record_oversize",
			"bytes":  len(raw),
			"sha256": hex.EncodeToString(digest[:]),
		}
		return identity, true
	}
	return bounded, changed
}

func boundedToolGatewayAuditField(value any) (any, bool) {
	raw, err := json.Marshal(value)
	if err != nil {
		return map[string]any{"kind": "omitted_unserializable"}, true
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return map[string]any{"kind": "omitted_unserializable"}, true
	}
	sanitized := redactToolGatewayAuditValue("", decoded)
	sanitizedRaw, err := json.Marshal(sanitized)
	if err != nil {
		return map[string]any{"kind": "omitted_unserializable"}, true
	}
	changed := !bytes.Equal(raw, sanitizedRaw)
	if len(sanitizedRaw) <= toolGatewayAuditFieldMaxBytes {
		return sanitized, changed
	}
	digest := sha256.Sum256(raw)
	return map[string]any{
		"kind":   "omitted_oversize",
		"bytes":  len(raw),
		"sha256": hex.EncodeToString(digest[:]),
	}, true
}

func redactToolGatewayAuditValue(key string, value any) any {
	if toolGatewayAuditSensitiveKey(key) {
		return "<redacted>"
	}
	switch typed := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(typed))
		for childKey, child := range typed {
			redacted[childKey] = redactToolGatewayAuditValue(childKey, child)
		}
		return redacted
	case []any:
		redacted := make([]any, len(typed))
		for index, child := range typed {
			redacted[index] = redactToolGatewayAuditValue("", child)
		}
		return redacted
	case string:
		return adaptercommon.RedactOutboundText(typed)
	default:
		return value
	}
}

func toolGatewayAuditSensitiveKey(key string) bool {
	normalized := strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, key)
	switch normalized {
	case "authorization", "proxyauthorization", "cookie", "setcookie", "apikey", "accesskey", "accesstoken",
		"refreshtoken", "token", "password", "passwd", "secret", "clientsecret", "privatekey", "credential",
		"credentials", "signature":
		return true
	}
	return strings.HasSuffix(normalized, "apikey") || strings.HasSuffix(normalized, "accesstoken") ||
		strings.HasSuffix(normalized, "refreshtoken") || strings.HasSuffix(normalized, "password") ||
		strings.HasSuffix(normalized, "secret") || strings.HasSuffix(normalized, "privatekey")
}

func (s *Server) persistToolGatewayAudit(auditID string, value map[string]any, updatedAt time.Time) error {
	if s == nil || s.runtimeStore == nil {
		return nil
	}
	return s.runtimeStore.EditNamespace(toolGatewayAuditNamespace, func(entries map[string]runtimekv.Entry) (bool, error) {
		previous := entries[auditID]
		entries[auditID] = runtimekv.Entry{
			Namespace: toolGatewayAuditNamespace,
			Key:       auditID,
			Value:     value,
			Version:   previous.Version + 1,
			UpdatedAt: updatedAt.UTC(),
		}
		pruneToolGatewayAudits(entries)
		return true, nil
	})
}

func pruneToolGatewayAudits(entries map[string]runtimekv.Entry) bool {
	if len(entries) <= toolGatewayAuditMaxEntries {
		return false
	}
	ordered := make([]runtimekv.Entry, 0, len(entries))
	for _, entry := range entries {
		ordered = append(ordered, entry)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].UpdatedAt.Equal(ordered[j].UpdatedAt) {
			return ordered[i].Key < ordered[j].Key
		}
		return ordered[i].UpdatedAt.Before(ordered[j].UpdatedAt)
	})
	for _, entry := range ordered[:len(ordered)-toolGatewayAuditMaxEntries] {
		delete(entries, entry.Key)
	}
	return true
}

func (s *Server) compactToolGatewayAudits() error {
	if s == nil || s.runtimeStore == nil {
		return nil
	}
	return s.runtimeStore.EditNamespace(toolGatewayAuditNamespace, func(entries map[string]runtimekv.Entry) (bool, error) {
		changed := false
		for key, entry := range entries {
			value, ok := entry.Value.(map[string]any)
			if !ok {
				bounded, itemChanged := boundedToolGatewayAuditField(entry.Value)
				if itemChanged {
					entry.Value = bounded
					entries[key] = entry
					changed = true
				}
				continue
			}
			bounded, itemChanged := boundedToolGatewayAuditMap(value)
			if itemChanged {
				entry.Value = bounded
				entries[key] = entry
				changed = true
			}
		}
		changed = pruneToolGatewayAudits(entries) || changed
		return changed, nil
	})
}
