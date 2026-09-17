package server

import (
	"strings"
	"time"
)

func defaultRunnerDiagnostics() RunnerDiagnostics {
	return RunnerDiagnostics{
		Enabled:               true,
		Provider:              WorkspaceSessionRunnerProvider,
		RunnerID:              defaultSessionRunnerID,
		ChatToolRoundLimit:    0,
		PollIntervalMS:        int(defaultSessionRunnerPollInterval / time.Millisecond),
		LeaseTTLSeconds:       int(defaultSessionRunnerLeaseTTL / time.Second),
		ReplayLimit:           defaultSessionRunnerReplayLimit,
		OutputLimitBytes:      defaultSessionRunnerOutputLimitBytes,
		RuntimeModelAuthority: true,
	}
}

func redactDiagnosticSecretSource(source string) string {
	trimmed := strings.TrimSpace(source)
	if trimmed == "" {
		return "none"
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "env:") || strings.HasPrefix(lower, "config:") || strings.HasPrefix(lower, "setting:") {
		return trimmed
	}
	if strings.HasPrefix(lower, "bearer ") || strings.HasPrefix(lower, "sk-") || strings.Contains(lower, "api_key=") || strings.Contains(lower, "apikey=") || strings.Contains(lower, "token=") || strings.Contains(lower, "secret=") {
		return "<redacted>"
	}
	return trimmed
}
func isZeroRunnerDiagnostics(diagnostics RunnerDiagnostics) bool {
	return !diagnostics.Enabled &&
		strings.TrimSpace(diagnostics.Provider) == "" &&
		strings.TrimSpace(diagnostics.RunnerID) == "" &&
		!diagnostics.CommandConfigured &&
		strings.TrimSpace(diagnostics.ChatEndpoint) == "" &&
		strings.TrimSpace(diagnostics.ChatModel) == "" &&
		diagnostics.ChatToolRoundLimit == 0 &&
		diagnostics.PollIntervalMS == 0 &&
		diagnostics.LeaseTTLSeconds == 0 &&
		diagnostics.ReplayLimit == 0 &&
		diagnostics.OutputLimitBytes == 0 &&
		!diagnostics.RuntimeModelAuthority
}

func supportedSessionRunnerProvider(provider string) bool {
	switch strings.TrimSpace(provider) {
	case "openai_chat", "command", "go_builtin", WorkspaceSessionRunnerProvider:
		return true
	default:
		return false
	}
}

func runnerSetupHints(provider string) []string {
	base := []string{"Set SYNON_RUNNER_ENABLED=true"}
	switch strings.TrimSpace(provider) {
	case "command":
		return append(base,
			"Set SYNON_RUNNER_PROVIDER=command",
			"Set SYNON_RUNNER_COMMAND to the runner command",
		)
	case "go_builtin":
		return append(base,
			"Set SYNON_RUNNER_PROVIDER=go_builtin",
			"Use the deterministic endpoint only for explicit test or development execution",
		)
	case WorkspaceSessionRunnerProvider:
		return []string{
			"Create or import an enabled model Provider in Settings",
			"Select that Provider as the active model authority",
			"Store its API credential in the encrypted credential vault when required",
		}
	case "openai_chat", "", "disabled":
		return append(base,
			"Set SYNON_RUNNER_PROVIDER=openai_chat",
			"Set SYNON_RUNNER_CHAT_ENDPOINT to an OpenAI-compatible /v1/chat/completions endpoint",
			"Set SYNON_RUNNER_CHAT_MODEL to the model name",
			"Set SYNON_RUNNER_CHAT_API_KEY when the endpoint requires authentication",
			"Optionally set SYNON_RUNNER_CHAT_TOOLS to a comma-separated allowed tool list",
		)
	default:
		return append(base,
			"Set SYNON_RUNNER_PROVIDER to one of workspace, openai_chat, command, or go_builtin",
		)
	}
}
