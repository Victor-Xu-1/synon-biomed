package server

import "testing"

type bundledMCPReadinessFixture struct {
	name  string
	ready bool
}

func (fixture bundledMCPReadinessFixture) MCPPythonEnvironmentName() string {
	return fixture.name
}

func (fixture bundledMCPReadinessFixture) RuntimeReady(language, environment string) bool {
	return fixture.ready && language == "python" && environment == fixture.name
}

func TestBundledMCPProbeWaitsForTheManagedRuntimeAuthority(t *testing.T) {
	if bundledMCPRuntimeIsReady(nil) {
		t.Fatal("nil runtime became ready")
	}
	if bundledMCPRuntimeIsReady(bundledMCPReadinessFixture{name: "synon-biomed-mcp-python"}) {
		t.Fatal("unready runtime became ready")
	}
	if !bundledMCPRuntimeIsReady(bundledMCPReadinessFixture{name: "synon-biomed-mcp-python", ready: true}) {
		t.Fatal("verified managed MCP runtime was not accepted")
	}
}
