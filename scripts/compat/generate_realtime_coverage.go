package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"synon-go/internal/compat/contracts"
	"synon-go/internal/realtime"
)

type coverageManifest struct {
	SchemaVersion int             `json:"schemaVersion"`
	Baseline      string          `json:"baseline"`
	GeneratedAt   string          `json:"generatedAt"`
	Generator     string          `json:"generator"`
	Summary       coverageSummary `json:"summary"`
	Events        []eventCoverage `json:"events"`
	Queries       []queryCoverage `json:"queries"`
	Verification  []string        `json:"verification"`
}

type coverageSummary struct {
	EventsTotal        int     `json:"eventsTotal"`
	EventsImplemented  int     `json:"eventsImplemented"`
	QueriesTotal       int     `json:"queriesTotal"`
	QueriesImplemented int     `json:"queriesImplemented"`
	TotalContracts     int     `json:"totalContracts"`
	TotalImplemented   int     `json:"totalImplemented"`
	CoveragePercent    float64 `json:"coveragePercent"`
}

type eventCoverage struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Status   string `json:"status"`
	Evidence string `json:"evidence"`
}

type queryCoverage struct {
	Name       string `json:"name"`
	Expression string `json:"expression"`
	Status     string `json:"status"`
	Evidence   string `json:"evidence"`
}

func main() {
	out := flag.String("out", "docs/compatibility/v1.1-realtime-contract-coverage.json", "output manifest path")
	flag.Parse()
	if err := realtime.ValidateCatalog(); err != nil {
		fatal(err)
	}
	manifest := coverageManifest{
		SchemaVersion: 1,
		Baseline:      "synonbiomed-v1.1 runtime/assets/web-dist/assets/index-CIcUordt.js",
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Generator:     "go run ./scripts/compat/generate_realtime_coverage.go",
		Events:        make([]eventCoverage, 0, len(contracts.EventTypes)),
		Queries:       make([]queryCoverage, 0, len(contracts.QueryKeys)),
		Verification: []string{
			"go test -race ./internal/realtime -count=1",
			"go test -race ./internal/persistence/workspace -run TestRealtimeEventsPersistAllBaselineTypesAndRemainUserScoped -count=1",
			"go test -race ./internal/server -run TestAllBaselineRealtimeEventsHaveExecutableProducerPaths -count=1",
			"go test -race ./internal/server -run TestCompatEventsReplayOverHTTPSSENAndWebSocket -count=1",
		},
	}
	for _, baseline := range contracts.EventTypes {
		spec, found := realtime.LookupEvent(baseline.Name)
		if !found || string(spec.Kind) != baseline.Kind {
			fatal(fmt.Errorf("event %s catalog mismatch", baseline.Name))
		}
		manifest.Events = append(manifest.Events, eventCoverage{
			Name: baseline.Name, Kind: baseline.Kind, Status: "implemented",
			Evidence: eventEvidence(baseline.Name),
		})
	}
	for _, baseline := range contracts.QueryKeys {
		manifest.Queries = append(manifest.Queries, queryCoverage{
			Name: baseline.Name, Expression: baseline.Expression, Status: "implemented",
			Evidence: "internal/realtime/catalog.go ResolveQueryKey; internal/realtime/catalog_test.go TestEveryRecoveredQueryKeyResolves",
		})
	}
	total := len(manifest.Events) + len(manifest.Queries)
	manifest.Summary = coverageSummary{
		EventsTotal: len(manifest.Events), EventsImplemented: len(manifest.Events),
		QueriesTotal: len(manifest.Queries), QueriesImplemented: len(manifest.Queries),
		TotalContracts: total, TotalImplemented: total, CoveragePercent: 100,
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		fatal(err)
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(*out, raw, 0o644); err != nil {
		fatal(err)
	}
}

func eventEvidence(name string) string {
	base := "internal/server/session_realtime_bridge_test.go TestAllBaselineRealtimeEventsHaveExecutableProducerPaths"
	switch name {
	case "frame_update", "frame_messages_delta", "text_chunk", "text_reset", "frame_activity", "compaction_status", "tool_stdout_chunk", "rate_limit_notice":
		return base + "; internal/server/compat_events_test.go TestWorkspaceMutationsPublishDurableBaselineFrameEvents"
	case "artifact_created", "artifact_deleted", "artifact_priority_update", "artifact_renamed", "artifact_moved", "lineage_ready", "folder_created", "folder_updated", "folder_deleted", "note_update", "routine_update":
		return base + "; internal/server/domain_compat_events_test.go TestArtifactFolderNoteAndRoutineMutationsPublishBaselineEvents"
	case "environment_status", "auth_status_changed":
		return base + "; internal/server/runtime_compat_api_test.go TestRuntimeSystemAndFeedbackCompatibilityAPI"
	case "host_access_granted":
		return base + "; internal/server/host_access_api_test.go TestHostAccessHTTPAPIUsesDurableRealPathGrants"
	case "network_access_granted":
		return base + "; internal/server/workspace_preferences_api_test.go TestRuntimePreferenceHTTPAPIPersistsAcrossServerRestart"
	case "connector_status", "connector_update", "connector_snapshot":
		return base + "; internal/server/mcp_directory_api_test.go TestMCPDirectoryHTTPAPIUsesBaselineRoutes"
	case "pong":
		return "internal/server/session_ws_test.go TestSessionWebSocketReplaysAndReceivesAppendedSessionEvents; " + base
	default:
		return base + "; internal/persistence/workspace/realtime_events_test.go TestRealtimeEventsPersistAllBaselineTypesAndRemainUserScoped"
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
