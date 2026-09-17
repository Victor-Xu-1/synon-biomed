package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"synon-go/internal/persistence/runtimekv"
)

const skillInvocationRuntimeNamespace = "skill-invocations"

type skillUsageProjection struct {
	Name            string  `json:"name"`
	InvocationCount int     `json:"invocationCount"`
	LastUsedAt      *string `json:"lastUsedAt,omitempty"`
}

// handleCompatibilitySkillUsage exposes only the aggregated, read-only Skill
// usage projection needed by the settings UI. It deliberately does not expose
// raw runtime values or the legacy direct-tool execution surface.
func (s *Server) handleCompatibilitySkillUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if _, ok := attachmentUserID(w, r); !ok {
		return
	}
	if s.runtimeStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Runtime store is not configured")
		return
	}
	entries, err := s.runtimeStore.List(skillInvocationRuntimeNamespace)
	if err != nil {
		writeV11Detail(w, http.StatusInternalServerError, "Skill usage is unavailable")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"usage": skillUsageProjections(entries)})
}

// skillUsageProjections is shared by the dedicated Skill endpoint and the
// account overview. It is deliberately a pure projection over cloned runtime
// entries so both APIs keep identical counting and timestamp semantics.
func skillUsageProjections(entries []runtimekv.Entry) []skillUsageProjection {
	usage := make(map[string]skillUsageProjection)
	for _, entry := range entries {
		name, createdAt, ok := skillInvocationProjection(entry.Value, entry.UpdatedAt)
		if !ok {
			continue
		}
		projection := usage[name]
		projection.Name = name
		projection.InvocationCount++
		if createdAt != nil && (projection.LastUsedAt == nil || createdAt.After(parseProjectionTime(*projection.LastUsedAt))) {
			formatted := createdAt.UTC().Format(time.RFC3339Nano)
			projection.LastUsedAt = &formatted
		}
		usage[name] = projection
	}

	result := make([]skillUsageProjection, 0, len(usage))
	for _, projection := range usage {
		result = append(result, projection)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result
}

func skillInvocationProjection(value any, fallback time.Time) (string, *time.Time, bool) {
	record := map[string]any{}
	raw, err := json.Marshal(value)
	if err != nil || json.Unmarshal(raw, &record) != nil {
		return "", nil, false
	}
	name, _ := record["skill"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		if nameValue, ok := record["skillName"].(string); ok {
			name = strings.TrimSpace(nameValue)
		}
	}
	if name == "" {
		return "", nil, false
	}
	for _, key := range []string{"createdAt", "created_at"} {
		if rawTimestamp, ok := record[key].(string); ok {
			if parsed, err := time.Parse(time.RFC3339Nano, rawTimestamp); err == nil {
				return name, &parsed, true
			}
		}
	}
	if !fallback.IsZero() {
		return name, &fallback, true
	}
	return name, nil, true
}

func parseProjectionTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}
