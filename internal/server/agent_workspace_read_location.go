package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	kernelruntime "synon-go/internal/kernel"
)

// A display filename or API URL is not a computation path. Resolve the exact
// authorized version through the same materialization authority as the host
// bridge, in the calling task's workspace, before exposing a usable path.
func (s *Server) readAgentWorkspaceVersionWithLocation(ctx context.Context, identity *agentKernelContext, version agentWorkspaceReadableVersion, input map[string]any) (any, error) {
	root, pathErr := s.ensureAgentWorkspaceRoot(identity)
	path := ""
	if pathErr == nil {
		var receiptRoot string
		receiptRoot, pathErr = s.kernelMaterializationReceiptRoot()
		if pathErr == nil {
			path, pathErr = materializeKernelImmutableContent(ctx, root, receiptRoot, version.versionID, version.filename, version.sizeBytes, version.sha256, version.reader)
		}
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if errors.Is(pathErr, errKernelArtifactContentIntegrity) {
		return nil, errors.New("read_file source content failed integrity verification")
	}
	metadata := map[string]any{"source_version_id": version.versionID}
	if pathErr == nil {
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return nil, err
		}
		file, err := openAgentWorkspaceRegularFile(root, relative)
		if err == nil {
			defer file.Close()
			metadata["file_path"] = path
			metadata["file_path_scope"] = "original_source"
			return readAgentWorkspaceFileWithLocation(ctx, file, version.filename, version.contentType, version.sizeBytes, input, metadata)
		}
		pathErr = err
	}
	// Cache unavailability must not turn readable, durable evidence into a
	// task failure. Verify the original and keep normal pointer/paging access;
	// never expose a path that was not successfully made available.
	if _, err := version.reader.Seek(0, io.SeekStart); err != nil {
		return nil, errors.New("read_file could not reset the source")
	}
	if version.sha256 != "" {
		digest, err := digestAgentWorkspaceReader(ctx, version.reader)
		if cause := context.Cause(ctx); cause != nil {
			return nil, cause
		}
		if err != nil || digest != version.sha256 {
			return nil, errors.New("read_file source content failed integrity verification")
		}
		if _, err := version.reader.Seek(0, io.SeekStart); err != nil {
			return nil, errors.New("read_file could not reset the source")
		}
	}
	metadata["file_path_error"] = "The task-local source copy is unavailable; the original version remains readable."
	code := "storage_error"
	var hostError *kernelruntime.HostCallError
	if errors.As(pathErr, &hostError) {
		code = hostError.Code
	} else if errors.Is(pathErr, os.ErrPermission) {
		code = "permission_denied"
	}
	metadata["file_path_error_code"] = code
	return readAgentWorkspaceFileWithLocation(ctx, version.reader, version.filename, version.contentType, version.sizeBytes, input, metadata)
}

func readAgentWorkspaceFileWithLocation(ctx context.Context, reader io.ReadSeeker, filename, contentType string, size int64, input, location map[string]any) (any, error) {
	encoded, err := json.Marshal(location)
	if err != nil {
		return nil, err
	}
	// Count actual serialized location fields before allocating a source
	// preview. Appending them afterwards can externalize read_file again and
	// hide the very location needed to escape the large-result loop.
	reserved, _ := ctx.Value(agentWorkspaceReadLocationReserveKey{}).(int)
	viewCtx := context.WithValue(ctx, agentWorkspaceReadLocationReserveKey{}, reserved+len(encoded)-1)
	result, err := readAgentWorkspaceFile(viewCtx, reader, filename, contentType, size, input)
	if err != nil {
		return nil, err
	}
	value, parts := unwrapAgentRuntimeRichToolResponse(result)
	fields, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("read_file returned an invalid response")
	}
	for key, value := range location {
		fields[key] = value
	}
	if len(parts) > 0 {
		return agentRuntimeRichToolResponse{value: fields, parts: parts}, nil
	}
	return fields, nil
}
