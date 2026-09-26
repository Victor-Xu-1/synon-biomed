package server

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"synon-go/internal/sciencecapability"
)

type managedExecutionOutputOwnership uint8

const (
	managedExecutionOutputOwnershipMissing managedExecutionOutputOwnership = iota
	managedExecutionOutputOwnershipValid
	managedExecutionOutputOwnershipInvalid
)

func inspectManagedExecutionOutputOwnership(directory string) (string, managedExecutionOutputOwnership) {
	raw, err := os.ReadFile(filepath.Join(directory, managedExecutionOutputOwnershipMarker))
	if os.IsNotExist(err) {
		return "", managedExecutionOutputOwnershipMissing
	}
	if err != nil || len(raw) == 0 || len(raw) > 4096 {
		return "", managedExecutionOutputOwnershipInvalid
	}
	var marker struct {
		Schema          string `json:"schema"`
		ExecutionPackID string `json:"execution_pack_id"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&marker) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		marker.Schema != "synon.execution-pack-output-owner.v1" || strings.TrimSpace(marker.ExecutionPackID) == "" {
		return "", managedExecutionOutputOwnershipInvalid
	}
	return strings.TrimSpace(marker.ExecutionPackID), managedExecutionOutputOwnershipValid
}

// augmentAgentSaveArtifactsWithExecutionBundles turns one explicitly saved
// file from a validated execution-pack output into a coherent declared result
// bundle. Output roles and paths come only from the capability registry; the
// gateway contains no task, engine, filename, or scientific-domain branches.
func (s *Server) augmentAgentSaveArtifactsWithExecutionBundles(
	workspaceDir string,
	request agentSaveArtifactsRequest,
	authorities []managedExecutionOutputAuthority,
) agentSaveArtifactsRequest {
	if s == nil || s.scienceCapabilities == nil || len(request.Files) == 0 || len(authorities) == 0 {
		return request
	}
	workspaceRoot, err := canonicalHostDirectory(workspaceDir)
	if err != nil {
		return request
	}
	ownersByRoot := map[string]managedExecutionOutputAuthority{}
	for _, relative := range request.Files {
		candidate := filepath.Join(workspaceRoot, filepath.FromSlash(relative))
		if !managedExecutionPathWithinRoot(workspaceRoot, candidate) {
			continue
		}
		info, statErr := os.Lstat(candidate)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		resolved, resolveErr := filepath.EvalSymlinks(candidate)
		if resolveErr != nil || filepath.Clean(resolved) != filepath.Clean(candidate) {
			continue
		}
		for _, authority := range authorities {
			if managedExecutionAuthorityContainsPath(authority, resolved) && resolved != authority.Root {
				ownersByRoot[authority.Root] = authority
				break
			}
		}
	}
	roots := make([]string, 0, len(ownersByRoot))
	for root := range ownersByRoot {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	fileSet := make(map[string]bool, len(request.Files))
	for _, path := range request.Files {
		fileSet[path] = true
	}
	for _, root := range roots {
		owned := ownersByRoot[root]
		_, engine, found := s.scienceCapabilities.FindExecutionPack(owned.PackID)
		if !found || engine.ExecutionPack.Mode != "local" {
			continue
		}
		outputs := append([]sciencecapability.ExecutionOutput(nil), engine.ExecutionPack.Outputs...)
		sort.Slice(outputs, func(i, j int) bool { return outputs[i].Path < outputs[j].Path })
		for _, output := range outputs {
			if output.Delivery != "snapshot" && output.Delivery != "working_data" {
				continue
			}
			readableRoot := managedExecutionAuthorityReadableRoot(owned)
			relative, found := managedExecutionBundleOutputPath(workspaceRoot, readableRoot, output.Path)
			if !found {
				continue
			}
			withinRoot, err := filepath.Rel(readableRoot, filepath.Join(workspaceRoot, filepath.FromSlash(relative)))
			if err != nil {
				continue
			}
			logical, err := filepath.Rel(workspaceRoot, filepath.Join(owned.Root, withinRoot))
			if err != nil || owned.Digests[filepath.ToSlash(logical)] == "" {
				continue
			}
			if !fileSet[relative] {
				if len(request.Files) >= maxAgentSavedArtifacts {
					break
				}
				request.Files = append(request.Files, relative)
				request.BundleAdditions = append(request.BundleAdditions, relative)
				fileSet[relative] = true
			}
			if request.Destination[relative] == "" {
				request.Destination[relative] = output.Delivery
			}
		}
	}
	return request
}

func managedExecutionBundleOutputPath(workspaceRoot, outputRoot, declared string) (string, bool) {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(declared)), "/")
	if len(parts) < 2 {
		return "", false
	}
	candidate := filepath.Join(outputRoot, filepath.FromSlash(strings.Join(parts[1:], "/")))
	if !managedExecutionPathWithinRoot(outputRoot, candidate) {
		return "", false
	}
	info, err := os.Lstat(candidate)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", false
	}
	relative, err := filepath.Rel(workspaceRoot, candidate)
	if err != nil {
		return "", false
	}
	relative, err = normalizeAgentSavedArtifactPath(relative)
	return relative, err == nil
}
