package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// agentRuntimePublicScientificSourcePreflight keeps an invalid scientific
// download plan inside the model's private repair loop. The actual download
// tool remains fail-closed, but a guessed or transformed URL must not first
// become a public failed tool call before the model returns to the authoritative
// source method. This is source-contract validation, not domain-specific URL
// reconstruction: every accepted URL must already exist in the current durable
// transcript branch.
func (g serverAgentRuntimeToolGateway) agentRuntimePublicScientificSourcePreflight(
	publicName string,
	input map[string]any,
) map[string]any {
	if strings.TrimSpace(publicName) != "download_public_scientific_file" ||
		g.server == nil || g.server.transcriptStore == nil || g.server.workspaceStore == nil ||
		g.taskRun == nil || g.taskRun.Transcript == nil {
		return nil
	}
	request, err := parseAgentPublicScientificFileRequest(input)
	if err != nil {
		return map[string]any{
			"ok": false, "status": "scientific_download_contract_preflight_required", "executed": false,
			"message": "The requested scientific download does not satisfy the declared download contract, so the call was rejected before execution.",
			"recovery": g.agentRuntimePublicScientificRecovery(
				"Do not repeat this malformed call. If the native file is still necessary, use one exact URL returned by a completed source result and a path-free filename with the same format extension; otherwise continue with sufficient verified evidence or another advertised route. Contract detail: " + err.Error(),
			),
		}
	}
	stream := g.taskRun.Transcript.Stream
	validationCtx := withTranscriptRunnerChatRun(context.Background(), g.taskRun)
	if _, err = g.server.validateAgentPublicScientificSourceURL(
		validationCtx, stream.UID, stream.OwnerID, request,
	); err == nil {
		return nil
	}
	if !errors.Is(err, errAgentPublicScientificFileSource) {
		return nil
	}
	return map[string]any{
		"ok": false, "status": "scientific_download_source_preflight_required", "executed": false,
		"message": "The requested scientific file URL is not present in a completed durable source-tool result, so the call was rejected before execution.",
		"recovery": g.agentRuntimePublicScientificRecovery(
			"Do not retry another guessed or rewritten URL. Choose another advertised evidence route or continue with the verified evidence already available. Only when the native file remains essential, obtain one exact downloadable URL from an authoritative source result before making a corrected download call.",
		),
	}
}

func (g serverAgentRuntimeToolGateway) agentRuntimePublicScientificRecovery(base string) string {
	if g.server == nil || g.taskRun == nil || g.taskRun.Transcript == nil {
		return base
	}
	stream := g.taskRun.Transcript.Stream
	candidates, err := g.server.agentPublicScientificDownloadCandidates(
		context.Background(), stream.UID, stream.OwnerID, 3,
	)
	if err != nil || len(candidates) == 0 {
		return base
	}
	latest := candidates[len(candidates)-1]
	return fmt.Sprintf(
		"%s The newest exact durable handoff is url=%q, filename=%q; copy those values without modification.",
		strings.TrimSpace(base), latest.URL, latest.Filename,
	)
}
