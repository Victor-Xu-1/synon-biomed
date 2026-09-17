package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"sync"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
	"synon-go/internal/toolcontract"
	"synon-go/internal/tools/webfetch"
)

// Read-result reuse is a per-logical-run optimization for explicitly marked
// idempotent sources. It keeps a model from re-downloading the same public
// source while it is synthesizing evidence, without sharing data between
// sessions or caching mutating/unknown tools.
const (
	maxSessionRunnerReadReuseEntries = 128
	maxSessionRunnerReadReuseBytes   = 16 << 20
	maxSessionRunnerReadReuseEntry   = 4 << 20
)

func (g serverAgentRuntimeToolGateway) readReuseEnabled(name string) bool {
	if g.taskRun == nil || g.server == nil {
		return false
	}
	tool, found := g.server.registeredTool(name)
	if !found || !tool.Executable {
		return false
	}
	for _, capability := range tool.Capabilities {
		if strings.EqualFold(strings.TrimSpace(capability), "idempotent-read") {
			return true
		}
	}
	return false
}

type sessionRunnerReadReuseEntry struct {
	value any
	size  int64
}

type sessionRunnerReadReuseCache struct {
	mu      sync.Mutex
	entries map[string]sessionRunnerReadReuseEntry
	order   []string
	bytes   int64
}

func (cache *sessionRunnerReadReuseCache) touch(key string) {
	for index, existing := range cache.order {
		if existing != key {
			continue
		}
		copy(cache.order[index:], cache.order[index+1:])
		cache.order[len(cache.order)-1] = key
		return
	}
	cache.order = append(cache.order, key)
}

func (run *sessionRunnerChatRun) readReuseCache() *sessionRunnerReadReuseCache {
	if run == nil {
		return nil
	}
	// The run is created once per execution unit and this pointer is only
	// initialized under the existing scientific-capability lock. Keeping the
	// cache lazy avoids changing all recovery constructors and preserves the
	// zero-value run used by focused tests.
	lock := run.scientificCapabilityLock()
	lock.Lock()
	defer lock.Unlock()
	if run.readReuse == nil {
		run.readReuse = &sessionRunnerReadReuseCache{entries: make(map[string]sessionRunnerReadReuseEntry)}
	}
	return run.readReuse
}

func sessionRunnerReadReuseKey(name string, input map[string]any) (string, bool) {
	canonical := strings.ToLower(strings.TrimSpace(name))
	if canonical == "" || input == nil {
		return "", false
	}
	// human_description is presentation metadata. web_fetch.prompt is a
	// compatibility hint that does not change the fetched bytes; keep prompt in
	// every other tool's fingerprint because a prompt may be execution input
	// there. All actual web_fetch arguments remain part of the fingerprint,
	// including URL and limits.
	copyInput := make(map[string]any, len(input))
	for key, value := range input {
		field := strings.ToLower(strings.TrimSpace(key))
		if field == "human_description" || canonical == "web_fetch" && field == "prompt" {
			continue
		}
		copyInput[key] = value
	}
	raw, err := json.Marshal(struct {
		Name  string         `json:"name"`
		Input map[string]any `json:"input"`
	}{Name: canonical, Input: copyInput})
	if err != nil || len(raw) == 0 {
		return "", false
	}
	return string(raw), true
}

func (run *sessionRunnerChatRun) lookupReadReuse(name string, input map[string]any) (any, bool) {
	key, ok := sessionRunnerReadReuseKey(name, input)
	if !ok {
		return nil, false
	}
	cache := run.readReuseCache()
	if cache == nil {
		return nil, false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, found := cache.entries[key]
	if !found {
		return nil, false
	}
	cache.touch(key)
	return markReadReuse(entry.value), true
}

func (run *sessionRunnerChatRun) storeReadReuse(name string, input map[string]any, value any) {
	if run == nil || value == nil || !sessionRunnerReadReuseResultEligible(value) {
		return
	}
	key, ok := sessionRunnerReadReuseKey(name, input)
	if !ok {
		return
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) == 0 || len(raw) > maxSessionRunnerReadReuseEntry {
		return
	}
	cache := run.readReuseCache()
	if cache == nil {
		return
	}
	entry := sessionRunnerReadReuseEntry{value: value, size: int64(len(raw))}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if previous, exists := cache.entries[key]; exists {
		cache.bytes -= previous.size
		cache.entries[key] = entry
		cache.bytes += entry.size
		cache.touch(key)
		return
	}
	cache.entries[key] = entry
	cache.touch(key)
	cache.bytes += entry.size
	for len(cache.order) > maxSessionRunnerReadReuseEntries || cache.bytes > maxSessionRunnerReadReuseBytes {
		oldest := cache.order[0]
		cache.order = cache.order[1:]
		if old, exists := cache.entries[oldest]; exists {
			delete(cache.entries, oldest)
			cache.bytes -= old.size
		}
	}
}

func (s *Server) hydrateSessionRunnerReadReuse(run *sessionRunnerChatRun, entries []eventjournal.Entry) {
	if s == nil || run == nil {
		return
	}
	gateway := serverAgentRuntimeToolGateway{server: s, taskRun: run}
	for _, entry := range entries {
		message := entry.Message
		if strings.TrimSpace(stringValue(message["type"])) != "runner_checkpoint" ||
			strings.TrimSpace(stringValue(message["toolPhase"])) != "completed" {
			continue
		}
		name := strings.TrimSpace(stringValue(message["toolName"]))
		input := decodeReadReuseMap(message["toolInput"])
		if name == "" || input == nil || !gateway.readReuseEnabled(name) {
			continue
		}
		result, recorded := s.hydrateSessionRunnerReadReuseValue(
			run, name, strings.TrimSpace(stringValue(message["toolCallId"])), message["toolResult"],
		)
		if !recorded {
			continue
		}
		run.storeReadReuse(name, input, result)
	}
}

// hydrateSessionRunnerReadReuseValue restores the exact raw result behind an
// externalized descriptor before placing it in the per-run read cache. The
// descriptor is immutable evidence for its original tool_call_id and start
// event; replaying that small descriptor as the result of a new call makes the
// new tool batch fail receipt validation even though the source read itself
// succeeded. Rehydrating the bytes lets the normal large-result authority bind
// a fresh descriptor to the new call. If the old bytes are unavailable or too
// large for this bounded cache, skip reuse and execute the read normally.
func (s *Server) hydrateSessionRunnerReadReuseValue(
	run *sessionRunnerChatRun,
	toolName, toolCallID string,
	value any,
) (any, bool) {
	decoded, recorded := decodeReadReuseValue(value)
	if !recorded {
		return nil, false
	}
	raw, err := json.Marshal(decoded)
	if err != nil {
		return nil, false
	}
	// markReadReuse adds a presentation-only reused flag. Remove it only from
	// the closed descriptor shape before decoding; it remains part of ordinary
	// source results.
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) == nil && fields["artifact_id"] != nil && fields["version_id"] != nil {
		delete(fields, "reused")
		raw, err = json.Marshal(fields)
		if err != nil {
			return nil, false
		}
	}
	descriptor, _, externalized, err := toolcontract.DecodeExternalizedResult(raw)
	if err != nil {
		return nil, false
	}
	if !externalized {
		return decoded, true
	}
	if s == nil || s.workspaceStore == nil || run == nil || run.Transcript == nil ||
		strings.TrimSpace(toolName) == "" || strings.TrimSpace(toolCallID) == "" ||
		descriptor.SizeBytes <= 0 || descriptor.SizeBytes > maxSessionRunnerReadReuseEntry {
		return nil, false
	}
	stream := run.Transcript.Stream
	record, reader, found, err := s.workspaceStore.OpenRunnerLargeToolResultContent(
		context.Background(), descriptor.VersionID, stream.OwnerID,
	)
	if err != nil || !found || reader == nil {
		if reader != nil {
			_ = reader.Close()
		}
		return nil, false
	}
	content, readErr := io.ReadAll(io.LimitReader(reader, maxSessionRunnerReadReuseEntry+1))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || len(content) == 0 ||
		int64(len(content)) != descriptor.SizeBytes || int64(len(content)) != record.SizeBytes {
		return nil, false
	}
	digest := sha256.Sum256(content)
	if record.ArtifactID != descriptor.ArtifactID || record.VersionID != descriptor.VersionID ||
		record.ContentType != descriptor.ContentType || record.ContentSHA256 != descriptor.SHA256 ||
		record.ContentSHA256 != hex.EncodeToString(digest[:]) || record.StreamUID != stream.UID ||
		record.ProjectID != stream.ProjectID || record.RootFrameID != stream.RootFrameID ||
		record.FrameID != stream.FrameID || record.OwnerUserID != stream.OwnerID ||
		!strings.EqualFold(record.ToolName, strings.TrimSpace(toolName)) ||
		record.ToolCallID != strings.TrimSpace(toolCallID) {
		return nil, false
	}
	var restored any
	if json.Unmarshal(content, &restored) != nil {
		return nil, false
	}
	return restored, true
}

// Durable journal readers normally decode toolInput/toolResult into maps, but
// older checkpoints and large-result recovery can retain their exact JSON as
// json.RawMessage or a string. Normalize those representations at this one
// replay boundary so resume does not silently lose reusable successful reads.
func decodeReadReuseMap(value any) map[string]any {
	decoded, ok := decodeReadReuseValue(value)
	if !ok {
		return nil
	}
	return mapValue(decoded)
}

func decodeReadReuseValue(value any) (any, bool) {
	switch typed := value.(type) {
	case json.RawMessage:
		if len(typed) == 0 {
			return nil, false
		}
		var decoded any
		if err := json.Unmarshal(typed, &decoded); err != nil {
			return nil, false
		}
		return decoded, true
	case []byte:
		return decodeReadReuseValue(json.RawMessage(typed))
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
			return value, true
		}
		return decodeReadReuseValue(json.RawMessage(trimmed))
	default:
		return value, value != nil
	}
}

func sessionRunnerReadReuseResultEligible(value any) bool {
	return value != nil && !isAgentRuntimeReadReuseFailure(value)
}

func isAgentRuntimeReadReuseFailure(value any) bool {
	// webfetch.Result exposes its documented envelope; do not cache partial or
	// unavailable responses, because a later retry may legitimately recover.
	if result, ok := value.(webfetch.Result); ok {
		return result.SourceUnavailable || result.Partial || result.Error != ""
	}
	if result, ok := value.(*webfetch.Result); ok && result != nil {
		return result.SourceUnavailable || result.Partial || result.Error != ""
	}
	// Other typed source outputs (search, patent, and full-text clients) expose
	// their status through JSON fields rather than a shared Go interface. Apply
	// the same closed envelope classifier after a bounded marshal so recoverable
	// or failed reads are never retained as successful evidence.
	raw, err := json.Marshal(value)
	if err != nil {
		return true
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return true
	}
	return agentruntime.ClassifyToolResult(envelope) != agentruntime.ToolResultSucceeded
}

func markReadReuse(value any) any {
	// Keep the original typed result contract while giving the model a small,
	// machine-readable signal that no second network request was necessary.
	switch result := value.(type) {
	case map[string]any:
		copy := make(map[string]any, len(result)+1)
		for key, nested := range result {
			copy[key] = nested
		}
		copy["reused"] = true
		if nested, ok := copy["result"].(webfetch.Result); ok {
			nested.Reused = true
			copy["result"] = nested
		} else if nested, ok := copy["result"].(*webfetch.Result); ok && nested != nil {
			nestedCopy := *nested
			nestedCopy.Reused = true
			copy["result"] = &nestedCopy
		}
		return copy
	case webfetch.Result:
		result.Reused = true
		return result
	case *webfetch.Result:
		if result == nil {
			return value
		}
		copy := *result
		copy.Reused = true
		return &copy
	default:
		return value
	}
}
