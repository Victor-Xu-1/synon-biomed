package contracts

import "testing"

func TestV11ContractBaselineIsComplete(t *testing.T) {
	summary := V11Summary()
	if summary.ServiceMethodCount != 190 || summary.HTTPRouteCount != 34 || summary.EventTypeCount != 47 || summary.QueryKeyCount != 60 {
		t.Fatalf("v1.1 contract counts = %+v, want methods=190 http_routes=34 events=47 query_keys=60", summary)
	}
	for _, method := range []string{
		"getAgents",
		"createAgent",
		"getArtifactLineage",
		"saveArtifactVersion",
		"listCustomMCPServers",
		"setMcpToolGrant",
		"listMCPConnectors",
		"exportSession",
	} {
		if !V11HasServiceMethod(method) {
			t.Fatalf("missing required v1.1 service method %q", method)
		}
	}
	for _, route := range []string{
		"POST /api/compute/inference-providers",
		"POST /api/compute/providers/:name/probe",
		"GET /api/compute/jobs/:jobId/logs",
		"PUT /api/compute/session/:rootFrameId/enabled/:name",
		"GET /api/compute/local/download",
	} {
		if !V11HasHTTPRoute(route) {
			t.Fatalf("missing required v1.1 HTTP route %q", route)
		}
	}
	for _, event := range []string{"artifact_created", "compute_job_update", "mcp_app_tool_call"} {
		if !V11HasEventType(event) {
			t.Fatalf("missing required v1.1 event %q", event)
		}
	}
	for _, key := range []string{"agents", "artifactLineage", "computeJobs", "mcpToolGrants"} {
		if !V11HasQueryKey(key) {
			t.Fatalf("missing required v1.1 query key %q", key)
		}
	}
	if got := V11DomainMethodCount("mcp"); got != 21 {
		t.Fatalf("v1.1 mcp method count = %d, want 21", got)
	}
}
