package server

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func explicitTaskRunTestGraph(stepIDs ...string) map[string]any {
	steps := make([]any, 0, len(stepIDs))
	for index, id := range stepIDs {
		step := map[string]any{
			"id":               id,
			"title":            "Explicit " + id,
			"description":      "Execute caller-authored step " + id + ".",
			"expected_output":  "Durable output for " + id + ".",
			"acceptance_check": "The explicit step completes or records a blocker.",
			"risk_level":       "medium",
			"max_recoveries":   float64(1),
			"executor":         map[string]any{"kind": "agent"},
		}
		if index > 0 {
			step["depends_on"] = []any{stepIDs[index-1]}
		}
		steps = append(steps, step)
	}
	return map[string]any{"steps": steps}
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := map[string]int{}
	for _, value := range left {
		counts[value]++
	}
	for _, value := range right {
		counts[value]--
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}

// v11SkillsDir resolves the repository's bundled v1.1 skills root, mirroring
// how production serve is launched with SYNON_SKILL_DIRS=<repo>/skills.
func v11SkillsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "skills")
}

// v11AgentCatalogRoot resolves the repository's verified v1.1 agent assets so
// tests exercise the same OPERON catalog that production serves.
func v11AgentCatalogRoot(t *testing.T) (string, string) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	return filepath.Join(root, "assets", "synonbiomed", "agents"),
		filepath.Join(root, "assets", "synonbiomed", "agents.manifest.json")
}

// newV11TestServer builds a server with the real v1.1 skill and agent
// catalogs so the bundled OPERON skill closure resolves exactly as it does in
// production.
func newV11TestServer(t *testing.T, options Options) *Server {
	t.Helper()
	options.SkillDirectories = append(options.SkillDirectories, v11SkillsDir(t))
	if options.AgentCatalog == nil && options.AgentCatalogRoot == "" && options.AgentManifestPath == "" {
		agentRoot, manifest := v11AgentCatalogRoot(t)
		options.AgentCatalogRoot = agentRoot
		options.AgentManifestPath = manifest
	}
	server := New(options)
	// The fixture owns the runtime DB created by New, even if a caller later
	// replaces the field for a specialized test. Borrowed workspace/kernel
	// resources retain the caller's existing shutdown order.
	if server.runtimeStoreOwned && server.runtimeStore != nil {
		ownedRuntime := server.runtimeStore
		t.Cleanup(func() {
			if err := ownedRuntime.Close(); err != nil {
				t.Errorf("close owned fixture runtime store: %v", err)
			}
		})
	}
	return server
}

func closeTestServer(t *testing.T, server *Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Close(ctx); err != nil {
		t.Errorf("close test server: %v", err)
	}
}
