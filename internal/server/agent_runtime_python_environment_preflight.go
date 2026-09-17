package server

import (
	"context"
	"strings"
	"time"

	kernelruntime "synon-go/internal/kernel"
)

type managedPythonSourcePreflighter interface {
	PreflightManagedPythonSource(
		context.Context,
		string,
		string,
	) (kernelruntime.ManagedPythonSourcePreflight, error)
}

// agentRuntimePythonEnvironmentAPIPreflight verifies imported modules and
// attributes against the exact managed environment named by the call. A
// deterministic mismatch is repaired in the engine's private pre-tool turn;
// an unavailable witness fails open so preflight cannot become a new execution
// blocker.
func agentRuntimePythonEnvironmentAPIPreflight(
	publicName string,
	input map[string]any,
	preflighter managedPythonSourcePreflighter,
) map[string]any {
	name := strings.ToLower(strings.TrimSpace(publicName))
	// The repl control kernel can create and import task-local helper modules in
	// the same cell and hosts provider adapters such as PIL image encoding. Its
	// exact import state is therefore not represented by a pre-existing managed
	// environment snapshot. Apply this witness only to the managed Python tool.
	if name != "python" || preflighter == nil {
		return nil
	}
	environment := strings.TrimSpace(stringValue(input["environment"]))
	source := stringValue(input["code"])
	if environment == "" || strings.TrimSpace(source) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	result, err := preflighter.PreflightManagedPythonSource(ctx, environment, source)
	if err != nil || len(result.Missing) == 0 {
		return nil
	}
	return map[string]any{
		"ok": false, "status": "python_environment_api_preflight_required", "executed": false,
		"message": "The selected managed environment does not provide these imported Python APIs: " +
			strings.Join(result.Missing, ", ") + ".",
		"recovery": "Inspect the installed interface and use APIs that are present in the selected environment. Preserve the computation inputs and requested outputs, do not repeat unchanged source, and continue with a materially corrected cell or another compatible tactic inside the same selected implementation.",
	}
}
