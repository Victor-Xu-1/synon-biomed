package main

import (
	"os"
	"strings"
	"testing"
)

func TestStartupDoesNotOwnACompetingBundledMCPPollLoop(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"probeBundledMCPConnectorsUntilReady",
		"retrying while Synon-managed runtime becomes ready",
	} {
		if strings.Contains(string(source), forbidden) {
			t.Fatalf("startup retained competing bundled MCP retry path %q", forbidden)
		}
	}
}
