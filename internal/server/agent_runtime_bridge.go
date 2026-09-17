package server

import (
	"time"

	"synon-go/internal/agentruntime"
	"synon-go/internal/providers"
)

const agentRuntimeApprovalNamespace = "agent-runtime-approvals"
const agentRuntimeHookNamespace = "hooks"
const agentRuntimeHookAuditNamespace = "agent-runtime-hook-audit"
const agentRuntimeHookRewakeNamespace = "agent-runtime-hook-rewake"
const agentRuntimeRememberedApprovalUser = "agent-runtime"
const agentRuntimeRememberedApprovalClient = "agent-runtime"

const agentRuntimeMCPUnavailableToolName = "mcp_runtime_unavailable"

const agentRuntimeMCPUnavailableDescription = "One or more enabled MCP connectors could not publish tool schemas for this task admission. Their tools are temporarily unavailable; retry after connector recovery or inspect MCP settings."

type serverBuiltinModelClient struct{}

type serverErrorModelClient struct{ err error }

// sessionRunnerAuditedStaticModelClient decorates the deployment-configured
// OpenAI-compatible fallback without changing its request, retry, or terminal
// failure semantics. Saved profiles emit the same audit shape in providers.
type sessionRunnerAuditedStaticModelClient struct {
	delegate agentruntime.ModelClient
	profile  providers.ModelProfile
	audit    func(providers.AuditRecord)
}

// sessionRunnerStaticStreamingCompatibilityClient keeps the established
// deployment-configured fallback terminal error contract while using the same
// streaming provider implementation as saved profiles. This prevents a 503 or
// first-byte timeout from becoming a new automatic continuation policy merely
// because streaming was enabled.
type sessionRunnerStaticStreamingCompatibilityClient struct {
	delegate agentruntime.StreamingModelClient
	timeout  time.Duration
}
