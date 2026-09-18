package server

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"synon-go/internal/executionprep"
)

type executionSourcePreparer interface {
	PrepareExecutionSource(context.Context, executionprep.Request) (executionprep.Result, error)
}

// Both admission and execution consume the same preparation authority. The
// second check re-reads script contents; an earlier successful read must not
// authorize a later, different file body.
func agentExecutionPreparationPreflight(publicName string, input map[string]any, identity *agentKernelContext, preparer executionSourcePreparer) map[string]any {
	language := strings.ToLower(strings.TrimSpace(publicName))
	if language == "repl" {
		language = "python"
	}
	if language != "bash" && language != "python" && language != "r" {
		return nil
	}
	request := executionprep.Request{Language: language, Source: stringValue(input["code"]), Environment: stringValue(input["environment"])}
	if language == "bash" {
		request.Source = stringValue(input["command"])
	}
	if identity != nil {
		request.WorkspaceRoot = identity.workspaceDir
		request.WorkingDir = identity.workspaceDir
		if working := strings.TrimSpace(stringValue(input["working_dir"])); working != "" {
			if filepath.IsAbs(working) {
				request.WorkingDir = working
			} else {
				request.WorkingDir = filepath.Join(identity.workspaceDir, working)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var result executionprep.Result
	var err error
	if preparer != nil {
		result, err = preparer.PrepareExecutionSource(ctx, request)
	} else {
		result, err = executionprep.Analyze(ctx, request, nil)
	}
	if err != nil || len(result.Requirements) == 0 {
		return nil
	}
	status := "durable_download_preflight_required"
	for _, requirement := range result.Requirements {
		if requirement.Effect == executionprep.PackageMutation {
			status = "managed_package_authority_required"
		}
	}
	return map[string]any{
		"ok": false, "schema": "synon.execution-preparation.v1", "status": status, "executed": false,
		"requirements": result.Requirements, "read_files": result.ReadFiles, "unresolved": result.Unresolved,
		"message":  "Execution preparation found effects owned by an existing durable tool authority. No source ran.",
		"recovery": "Satisfy managed_environment requirements with manage_environments or manage_packages and verified import witnesses; satisfy durable_download requirements with download_public_scientific_file using the verified source URL. Preserve the chosen computation and completed work, then execute the source with the prepared environment and local input paths.",
	}
}
