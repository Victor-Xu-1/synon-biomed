package server

import (
	"context"
	"strings"
	"testing"
)

func TestAgentRuntimeBashDownloadPreflightRoutesFileTransferClientsToDurableAuthority(t *testing.T) {
	for _, command := range []string{
		`wget -O dataset.csv.gz https://example.org/dataset.csv.gz`,
		`printf 'ready\n' && env LC_ALL=C /usr/bin/wget2 https://example.org/archive.tar.gz`,
		`curl --output=result.sdf https://example.org/result.sdf`,
		`aria2c https://example.org/matrix.h5`,
	} {
		preflight := agentExecutionPreparationPreflight("bash", map[string]any{"command": command}, nil, nil)
		if preflight == nil || preflight["status"] != "durable_download_preflight_required" ||
			preflight["executed"] != false || !strings.Contains(stringValue(preflight["recovery"]), "download_public_scientific_file") {
			t.Fatalf("command=%q preflight=%#v", command, preflight)
		}
	}
	for _, command := range []string{
		`curl -sS https://example.org/api/status`,
		`printf '%s\n' 'wget -O file https://example.org/file'`,
		`python analysis.py`,
	} {
		if preflight := agentExecutionPreparationPreflight("bash", map[string]any{"command": command}, nil, nil); preflight != nil {
			t.Fatalf("ordinary command=%q preflight=%#v", command, preflight)
		}
	}
}

func TestAgentRuntimeBashDownloadPreflightCreatesNoKernelOperation(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	result, err := fixture.server.executeAgentKernelTool(
		context.Background(), fixture.identity, "bash",
		map[string]any{
			"command":           "wget -O dataset.csv.gz https://example.org/dataset.csv.gz",
			"environment":       "python",
			"human_description": "Downloading a public dataset",
		},
	)
	if err != nil || result["status"] != "durable_download_preflight_required" || result["executed"] != false {
		t.Fatalf("preflight result=%#v err=%v", result, err)
	}
	var operations int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM kernel_local_operations`).Scan(&operations); err != nil || operations != 0 {
		t.Fatalf("download preflight created operations=%d err=%v", operations, err)
	}
}
