package server

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

type agentKernelInputArtifactReceipt struct {
	ArtifactID string `json:"artifact_id"`
	VersionID  string `json:"version_id"`
	Checksum   string `json:"checksum"`
	SizeBytes  int64  `json:"size_bytes"`
}

type agentKernelInputArtifactCollector struct {
	mu       sync.Mutex
	receipts map[string]agentKernelInputArtifactReceipt
}

func newAgentKernelInputArtifactCollector() *agentKernelInputArtifactCollector {
	return &agentKernelInputArtifactCollector{receipts: map[string]agentKernelInputArtifactReceipt{}}
}

func (collector *agentKernelInputArtifactCollector) wrap(
	server *Server,
	access workspace.KernelFrameAccess,
	policy *kernelruntime.HostCallPolicy,
) {
	if collector == nil || server == nil || policy == nil || policy.Handler == nil {
		return
	}
	base := policy.Handler
	policy.Handler = func(ctx context.Context, call kernelruntime.HostCall) (any, error) {
		result, err := base(ctx, call)
		if err == nil && call.Method == "host.artifact_path" && len(call.Args) == 1 {
			requestedID := strings.TrimSpace(stringValue(call.Args[0]))
			if receipt, found := server.agentKernelInputArtifactReceipt(ctx, access, requestedID, result); found {
				collector.mu.Lock()
				collector.receipts[receipt.VersionID] = receipt
				collector.mu.Unlock()
			}
		}
		return result, err
	}
}

func (s *Server) agentKernelInputArtifactReceipt(
	ctx context.Context,
	access workspace.KernelFrameAccess,
	requestedID string,
	materialized any,
) (agentKernelInputArtifactReceipt, bool) {
	if s == nil || s.workspaceStore == nil || requestedID == "" {
		return agentKernelInputArtifactReceipt{}, false
	}
	artifact, version, found, err := s.workspaceStore.GetArtifactVersionMetadata(requestedID)
	if err != nil || !found {
		artifact, version, found, err = s.workspaceStore.GetCurrentArtifactVersionMetadata(requestedID)
	}
	path := strings.TrimSpace(stringValue(materialized))
	if err != nil || !found || artifact.ProjectID != access.Frame.ProjectID || path == "" ||
		!isSHA256Hex(strings.ToLower(strings.TrimSpace(version.ContentSHA256))) {
		return agentKernelInputArtifactReceipt{}, false
	}
	receiptRoot, err := s.kernelMaterializationReceiptRoot()
	if err != nil {
		return agentKernelInputArtifactReceipt{}, false
	}
	matches, verifyErr := kernelMaterializedFileMatches(
		ctx, path, kernelMaterializationReceiptPath(receiptRoot, filepath.Dir(filepath.Dir(path)), version.ID),
		version.ID, filepath.Base(artifact.Name), version.SizeBytes, version.ContentSHA256,
	)
	if verifyErr != nil || !matches {
		return agentKernelInputArtifactReceipt{}, false
	}
	return agentKernelInputArtifactReceipt{
		ArtifactID: artifact.ID, VersionID: version.ID,
		Checksum: strings.ToLower(strings.TrimSpace(version.ContentSHA256)), SizeBytes: version.SizeBytes,
	}, true
}

func (collector *agentKernelInputArtifactCollector) snapshot() []agentKernelInputArtifactReceipt {
	if collector == nil {
		return nil
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	result := make([]agentKernelInputArtifactReceipt, 0, len(collector.receipts))
	for _, receipt := range collector.receipts {
		result = append(result, receipt)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].VersionID < result[j].VersionID })
	return result
}

func bindAgentKernelInputArtifactAccessReceipts(
	logInput *workspace.SaveExecutionLogInput,
	result map[string]any,
	eventPayload map[string]any,
	receipts []agentKernelInputArtifactReceipt,
) {
	if len(receipts) == 0 {
		return
	}
	if logInput != nil {
		logInput.Record.Detection = map[string]any{"input_artifact_accesses": receipts}
	}
	if result != nil {
		result["input_artifact_accesses"] = receipts
	}
	if eventPayload != nil {
		eventPayload["input_artifact_accesses"] = receipts
	}
}
