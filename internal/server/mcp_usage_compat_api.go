package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"synon-go/internal/mcpdirectory"
	"synon-go/internal/tools/mcpstdio"
)

const mcpInvocationRuntimeNamespace = "mcp-invocations"

type mcpConnectorUsageAccumulator struct {
	InvocationCount int
	LastUsedAt      *time.Time
}

type mcpConnectorUsageIndex struct {
	ByID   map[string]mcpConnectorUsageAccumulator
	ByName map[string]mcpConnectorUsageAccumulator
}

// recordMCPConnectorInvocation is the one persistence boundary for MCP
// invocation statistics. It is best effort, like Skill usage recording, and
// intentionally uses an independent context so a cancelled tool call still
// leaves a truthful audit record.
func (s *Server) recordMCPConnectorInvocation(
	ctx context.Context,
	userID string,
	connectorID string,
	source string,
	connectorName string,
	toolName string,
	callErr error,
) {
	if s == nil || s.runtimeStore == nil {
		return
	}
	userID = strings.TrimSpace(userID)
	connectorID = strings.TrimSpace(connectorID)
	source = strings.TrimSpace(source)
	connectorName = strings.TrimSpace(connectorName)
	toolName = strings.TrimSpace(toolName)
	if userID == "" || (connectorID == "" && connectorName == "") || toolName == "" {
		return
	}
	createdAt := time.Now().UTC()
	invocationID := mcpInvocationID(userID, source, connectorID, connectorName, toolName, createdAt)
	value := map[string]any{
		"id":              invocationID,
		"userId":          userID,
		"connectorId":     connectorID,
		"connectorSource": source,
		"connectorName":   connectorName,
		"toolName":        toolName,
		"status":          mcpInvocationStatus(callErr),
		"createdAt":       createdAt.Format(time.RFC3339Nano),
	}
	persistCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := s.runtimeStore.SetContext(persistCtx, mcpInvocationRuntimeNamespace, invocationID, value); err != nil {
		// Usage is observability data and must never change the MCP result. The
		// runtime store itself remains the source of diagnostics for persistence
		// failures; keep this boundary deliberately non-blocking.
		return
	}
}

func mcpInvocationStatus(err error) string {
	if err == nil {
		return "completed"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "failed"
}

func mcpInvocationID(userID, source, connectorID, connectorName, toolName string, createdAt time.Time) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		"mcp-invocation-v1",
		userID,
		source,
		connectorID,
		connectorName,
		toolName,
		createdAt.Format(time.RFC3339Nano),
	}, "\x00")))
	return "mcp-" + hex.EncodeToString(digest[:])
}

func (s *Server) recordLegacyMCPInvocation(ctx context.Context, serverName, toolName string, callErr error) {
	run, ok := transcriptRunnerChatRunFromContext(ctx)
	if !ok || run.Transcript == nil {
		return
	}
	s.recordMCPConnectorInvocation(
		ctx,
		run.Transcript.Stream.OwnerID,
		"",
		"",
		serverName,
		toolName,
		callErr,
	)
}

func (s *Server) attachMCPConnectorUsage(userID string, connectors []mcpdirectory.Connector) error {
	index, err := s.loadMCPConnectorUsage(userID)
	if err != nil {
		return err
	}
	for i := range connectors {
		connectors[i].Usage = mcpdirectory.ConnectorUsage{}
		if usage, ok := index.ByID[mcpUsageIDKey(connectors[i].Source, connectors[i].ID)]; ok {
			connectors[i].Usage = mcpdirectory.ConnectorUsage{
				InvocationCount: usage.InvocationCount,
				LastUsedAt:      mcpUsageTimeString(usage.LastUsedAt),
			}
			continue
		}
		if usage, ok := index.ByName[mcpUsageNameKey(connectors[i].Name)]; ok {
			connectors[i].Usage = mcpdirectory.ConnectorUsage{
				InvocationCount: usage.InvocationCount,
				LastUsedAt:      mcpUsageTimeString(usage.LastUsedAt),
			}
		}
	}
	return nil
}

func (s *Server) loadMCPConnectorUsage(userID string) (mcpConnectorUsageIndex, error) {
	index := mcpConnectorUsageIndex{
		ByID:   map[string]mcpConnectorUsageAccumulator{},
		ByName: map[string]mcpConnectorUsageAccumulator{},
	}
	if s == nil || s.runtimeStore == nil {
		return index, nil
	}
	entries, err := s.runtimeStore.List(mcpInvocationRuntimeNamespace)
	if err != nil {
		return index, err
	}
	for _, entry := range entries {
		projection, ok := mcpInvocationProjection(entry.Value, entry.UpdatedAt)
		if !ok || projection.UserID != strings.TrimSpace(userID) {
			continue
		}
		if projection.ConnectorID != "" {
			key := mcpUsageIDKey(projection.Source, projection.ConnectorID)
			index.ByID[key] = accumulateMCPUsage(index.ByID[key], projection.CreatedAt)
			continue
		}
		if projection.ConnectorName != "" {
			key := mcpUsageNameKey(projection.ConnectorName)
			index.ByName[key] = accumulateMCPUsage(index.ByName[key], projection.CreatedAt)
		}
	}
	return index, nil
}

type mcpInvocationProjectionRecord struct {
	UserID        string
	ConnectorID   string
	Source        string
	ConnectorName string
	CreatedAt     *time.Time
}

func mcpInvocationProjection(value any, fallback time.Time) (mcpInvocationProjectionRecord, bool) {
	record := map[string]any{}
	raw, err := json.Marshal(value)
	if err != nil || json.Unmarshal(raw, &record) != nil {
		return mcpInvocationProjectionRecord{}, false
	}
	projection := mcpInvocationProjectionRecord{
		UserID:        strings.TrimSpace(stringValue(record["userId"])),
		ConnectorID:   strings.TrimSpace(stringValue(record["connectorId"])),
		Source:        strings.TrimSpace(stringValue(record["connectorSource"])),
		ConnectorName: strings.TrimSpace(stringValue(record["connectorName"])),
	}
	if projection.UserID == "" || (projection.ConnectorID == "" && projection.ConnectorName == "") {
		return mcpInvocationProjectionRecord{}, false
	}
	for _, key := range []string{"createdAt", "created_at"} {
		if timestamp, ok := record[key].(string); ok {
			if parsed, parseErr := time.Parse(time.RFC3339Nano, timestamp); parseErr == nil {
				projection.CreatedAt = &parsed
				return projection, true
			}
		}
	}
	if !fallback.IsZero() {
		projection.CreatedAt = &fallback
	}
	return projection, true
}

func accumulateMCPUsage(current mcpConnectorUsageAccumulator, createdAt *time.Time) mcpConnectorUsageAccumulator {
	current.InvocationCount++
	if createdAt != nil && (current.LastUsedAt == nil || createdAt.After(*current.LastUsedAt)) {
		value := createdAt.UTC()
		current.LastUsedAt = &value
	}
	return current
}

func mcpUsageIDKey(source, connectorID string) string {
	return strings.ToLower(strings.TrimSpace(source)) + "\x00" + strings.TrimSpace(connectorID)
}

func mcpUsageNameKey(name string) string {
	return mcpstdio.NormalizeName(strings.TrimSpace(name))
}

func mcpUsageTimeString(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339Nano)
	return &formatted
}
