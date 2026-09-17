package server

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

const (
	maxKernelFramePatternBytes = 2048
	maxKernelLineageNodes      = 1000
)

func isKernelInspectionHostMethod(method string) bool {
	switch method {
	case "host.artifacts", "host.artifact_path", "host.lineage", "host.lineage_graph", "host.frames":
		return true
	default:
		return false
	}
}

func (s *Server) handleKernelInspectionHostCall(ctx context.Context, bound kernelHostExecutionIdentity, access workspace.KernelFrameAccess, method string, args []any, kwargs map[string]any) (any, error) {
	if len(kwargs) != 0 {
		return nil, kernelruntime.NewHostCallError("invalid_arguments", method+": keyword arguments are not accepted on the wire")
	}
	switch method {
	case "host.artifacts":
		payload, err := kernelInspectionObject(args, method)
		if err != nil {
			return nil, err
		}
		return s.kernelBrowseArtifacts(ctx, access, payload)
	case "host.artifact_path":
		versionID, err := kernelInspectionString(args, method)
		if err != nil {
			return nil, err
		}
		return s.kernelArtifactPath(ctx, bound, access, versionID)
	case "host.lineage":
		versionID, err := kernelInspectionString(args, method)
		if err != nil {
			return nil, err
		}
		return s.kernelLineage(ctx, bound, access, strings.ToLower(versionID))
	case "host.lineage_graph":
		payload, err := kernelInspectionObject(args, method)
		if err != nil {
			return nil, err
		}
		return s.kernelLineageGraph(ctx, access, payload)
	case "host.frames":
		payload, err := kernelInspectionObject(args, method)
		if err != nil {
			return nil, err
		}
		return s.kernelBrowseFrames(ctx, access, payload)
	default:
		return nil, kernelruntime.NewHostCallError("method_not_allowed", "host method is not allowed")
	}
}

func kernelInspectionObject(args []any, method string) (map[string]any, error) {
	if len(args) != 1 {
		return nil, kernelruntime.NewHostCallError("invalid_arguments", method+": expected one options object")
	}
	payload, ok := args[0].(map[string]any)
	if !ok {
		return nil, kernelruntime.NewHostCallError("invalid_arguments", method+": options must be an object")
	}
	return payload, nil
}

func kernelInspectionString(args []any, method string) (string, error) {
	if len(args) != 1 {
		return "", kernelruntime.NewHostCallError("invalid_arguments", method+": expected one id")
	}
	value, ok := args[0].(string)
	value = strings.TrimSpace(value)
	if !ok || value == "" || len(value) > 256 {
		return "", kernelruntime.NewHostCallError("invalid_arguments", method+": id must be a non-empty string")
	}
	return value, nil
}

func (s *Server) kernelBrowseArtifacts(ctx context.Context, access workspace.KernelFrameAccess, payload map[string]any) (map[string]any, error) {
	allowed := map[string]bool{"version_id": true, "project_id": true, "frame_id": true, "filename": true, "exact": true, "content": true, "content_type": true, "after": true, "before": true, "include_intermediate": true, "limit": true, "offset": true, "search": true}
	if err := rejectKernelInspectionFields(payload, allowed, "host.artifacts"); err != nil {
		return nil, err
	}
	options := workspace.KernelArtifactBrowseOptions{VersionID: kernelOptionalString(payload["version_id"]), ProjectID: kernelOptionalString(payload["project_id"]), FrameID: kernelOptionalString(payload["frame_id"]), Filename: kernelOptionalString(payload["filename"]), Content: kernelOptionalString(payload["content"]), ContentType: kernelOptionalString(payload["content_type"]), Search: kernelOptionalString(payload["search"]), Limit: 200}
	var err error
	if options.ExactFilename, err = kernelOptionalBool(payload, "exact", false); err != nil {
		return nil, err
	}
	if options.IncludeIntermediate, err = kernelOptionalBool(payload, "include_intermediate", false); err != nil {
		return nil, err
	}
	if options.Limit, err = kernelOptionalInt(payload, "limit", 200, 1, workspace.KernelInspectionMaxRows); err != nil {
		return nil, err
	}
	if options.Offset, err = kernelOptionalInt(payload, "offset", 0, 0, 1_000_000); err != nil {
		return nil, err
	}
	if options.After, err = kernelOptionalTime(payload, "after"); err != nil {
		return nil, err
	}
	if options.Before, err = kernelOptionalTime(payload, "before"); err != nil {
		return nil, err
	}
	if options.FrameID != "" {
		frame, found, readErr := s.workspaceStore.GetKernelFrameAccessContext(ctx, options.FrameID)
		if readErr != nil {
			return nil, classifyKernelHostError(readErr)
		}
		if !found || frame.UserID != access.UserID {
			return nil, kernelruntime.NewHostCallError("not_found", "host.artifacts: frame not found")
		}
		if options.ProjectID != "" && options.ProjectID != "all" && options.ProjectID != frame.Frame.ProjectID {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", "host.artifacts: frame and project filters disagree")
		}
		options.ProjectID = frame.Frame.ProjectID
	}
	records, count, err := s.workspaceStore.ListKernelArtifacts(ctx, access.UserID, access.Frame.ProjectID, options)
	if err != nil {
		return nil, classifyKernelHostError(err)
	}
	scope := "single"
	if options.VersionID != "" {
		scope = "version"
	} else if options.ProjectID == "all" {
		scope = "all"
	}
	result := map[string]any{
		"count": count, "scope": scope, "artifacts": records,
		"provenance": map[string]any{"root_frame_id": access.Frame.RootFrameID, "project_id": access.Frame.ProjectID, "agent_name": access.Frame.AgentName},
	}
	if scope == "single" {
		projectID := options.ProjectID
		if projectID == "" {
			projectID = access.Frame.ProjectID
		}
		result["project_id"] = projectID
	}
	if options.FrameID != "" {
		result["frame_id"] = options.FrameID
	}
	if options.Offset > 0 {
		result["offset"] = options.Offset
	}
	if options.Offset+len(records) < count {
		result["truncated"] = true
	}
	return result, nil
}

func (s *Server) kernelArtifactPath(ctx context.Context, bound kernelHostExecutionIdentity, access workspace.KernelFrameAccess, requestedID string) (string, error) {
	if workspace.IsRunnerLargeToolResultArtifactID(requestedID) || workspace.IsRunnerLargeToolResultVersionID(requestedID) {
		return s.materializeKernelLargeToolResult(ctx, bound.workspaceDir, access, requestedID)
	}
	artifact, version, found, err := s.workspaceStore.GetArtifactVersionMetadata(requestedID)
	if err != nil {
		return "", classifyKernelHostError(err)
	}
	if !found {
		artifact, version, found, err = s.workspaceStore.GetCurrentArtifactVersionMetadata(requestedID)
	}
	if err != nil {
		return "", classifyKernelHostError(err)
	}
	if !found {
		return "", kernelruntime.NewHostCallError("not_found", "host.artifact_path: artifact version not found")
	}
	owned, err := s.workspaceStore.ProjectOwnedBy(artifact.ProjectID, access.UserID)
	if err != nil {
		return "", classifyKernelHostError(err)
	}
	if !owned {
		return "", kernelruntime.NewHostCallError("not_found", "host.artifact_path: artifact version not found")
	}
	return s.materializeKernelArtifact(ctx, bound.workspaceDir, artifact, version)
}

func (s *Server) materializeKernelArtifact(ctx context.Context, workspaceDir string, artifact workspace.Artifact, version workspace.ArtifactVersion) (string, error) {
	_, _, reader, found, err := s.workspaceStore.OpenArtifactVersionContent(version.ID)
	if err != nil {
		return "", classifyKernelHostError(err)
	}
	if !found {
		return "", kernelruntime.NewHostCallError("not_found", "host.artifact_path: artifact version not found")
	}
	defer reader.Close()
	receiptRoot, err := s.kernelMaterializationReceiptRoot()
	if err != nil {
		return "", classifyKernelHostError(err)
	}
	return materializeKernelImmutableContent(
		ctx, workspaceDir, receiptRoot, version.ID, artifact.Name, version.SizeBytes, version.ContentSHA256, reader,
	)
}

func (s *Server) kernelLineage(ctx context.Context, bound kernelHostExecutionIdentity, access workspace.KernelFrameAccess, versionID string) (map[string]any, error) {
	record, found, err := s.workspaceStore.GetArtifactVersionLineageRecord(versionID, true)
	if err != nil {
		return nil, classifyKernelHostError(err)
	}
	if !found || !kernelProjectOwned(s.workspaceStore, record.ProjectID, access.UserID) {
		return nil, kernelruntime.NewHostCallError("not_found", "host.lineage: artifact version not found")
	}
	dependencies, err := s.workspaceStore.ListArtifactVersionDependencies(ctx, versionID, "up")
	if err != nil {
		return nil, classifyKernelHostError(err)
	}
	inputs := make([]map[string]any, 0, len(dependencies))
	for _, dependency := range dependencies {
		artifact, version, found, err := s.workspaceStore.GetArtifactVersionMetadata(dependency.DependsOnVersionID)
		if err != nil {
			return nil, classifyKernelHostError(err)
		}
		if !found || !kernelProjectOwned(s.workspaceStore, artifact.ProjectID, access.UserID) {
			continue
		}
		path, err := s.materializeKernelArtifact(ctx, bound.workspaceDir, artifact, version)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, map[string]any{"version_id": version.ID, "filename": artifact.Name, "path": path, "reference_name": dependency.ReferenceName})
	}
	code := ""
	if record.Code != nil {
		code = *record.Code
	}
	messages := record.Messages
	if messages == nil {
		messages = []any{}
	}
	environment := record.EnvironmentSnapshot
	if environment == nil {
		environment = map[string]any{}
	}
	return map[string]any{"code": code, "messages": messages, "env": environment, "inputs": inputs, "artifact_id": record.ArtifactID, "version_id": record.VersionID, "filename": record.Filename, "project_id": record.ProjectID, "frame_id": record.FrameID, "producing_cell_id": record.ProducingCellID, "checksum": nullableKernelString(record.Checksum), "extraction_pending": record.Pending}, nil
}

func (s *Server) kernelLineageGraph(ctx context.Context, access workspace.KernelFrameAccess, payload map[string]any) (map[string]any, error) {
	allowed := map[string]bool{"version_id": true, "direction": true, "max_depth": true, "max_nodes": true}
	if err := rejectKernelInspectionFields(payload, allowed, "host.lineage.graph"); err != nil {
		return nil, err
	}
	versionID := strings.ToLower(strings.TrimSpace(kernelOptionalString(payload["version_id"])))
	if versionID == "" {
		return nil, kernelruntime.NewHostCallError("invalid_arguments", "host.lineage.graph: version_id is required")
	}
	direction := strings.TrimSpace(kernelOptionalString(payload["direction"]))
	if direction == "" {
		direction = "up"
	}
	if direction != "up" && direction != "down" {
		return nil, kernelruntime.NewHostCallError("invalid_arguments", "host.lineage.graph: direction must be up or down")
	}
	maxDepth, err := kernelOptionalInt(payload, "max_depth", 32, 0, 256)
	if err != nil {
		return nil, err
	}
	maxNodes, err := kernelOptionalInt(payload, "max_nodes", 500, 1, maxKernelLineageNodes)
	if err != nil {
		return nil, err
	}
	root, found, err := s.workspaceStore.GetArtifactVersionLineageRecord(versionID, false)
	if err != nil {
		return nil, classifyKernelHostError(err)
	}
	if !found || !kernelProjectOwned(s.workspaceStore, root.ProjectID, access.UserID) {
		return nil, kernelruntime.NewHostCallError("not_found", "host.lineage.graph: artifact version not found")
	}
	type frontierItem struct {
		id    string
		depth int
	}
	frontier := []frontierItem{{id: versionID}}
	seen := map[string]bool{}
	nodes := make([]map[string]any, 0)
	edges := make([]workspace.ArtifactVersionDependency, 0)
	truncated := false
	for len(frontier) > 0 {
		item := frontier[0]
		frontier = frontier[1:]
		if seen[item.id] {
			continue
		}
		if len(nodes) >= maxNodes {
			truncated = true
			break
		}
		record, found, err := s.workspaceStore.GetArtifactVersionLineageRecord(item.id, false)
		if err != nil {
			return nil, classifyKernelHostError(err)
		}
		if !found || !kernelProjectOwned(s.workspaceStore, record.ProjectID, access.UserID) {
			continue
		}
		seen[item.id] = true
		nodes = append(nodes, kernelLineageNode(record))
		if item.depth >= maxDepth {
			continue
		}
		dependencies, err := s.workspaceStore.ListArtifactVersionDependencies(ctx, item.id, direction)
		if err != nil {
			return nil, classifyKernelHostError(err)
		}
		for _, dependency := range dependencies {
			edges = append(edges, dependency)
			next := dependency.DependsOnVersionID
			if direction == "down" {
				next = dependency.VersionID
			}
			frontier = append(frontier, frontierItem{id: next, depth: item.depth + 1})
		}
	}
	return map[string]any{"root_version_id": versionID, "direction": direction, "truncated": truncated, "extraction_pending": root.Pending, "nodes": nodes, "edges": edges}, nil
}

func kernelLineageNode(record workspace.ArtifactLineageRecord) map[string]any {
	code := record.Code != nil && *record.Code != ""
	return map[string]any{"version_id": record.VersionID, "artifact_id": record.ArtifactID, "filename": record.Filename, "version_number": record.VersionNumber, "frame_id": record.FrameID, "producing_cell_id": record.ProducingCellID, "created_at": record.CreatedAt.UTC().Format(time.RFC3339Nano), "content_type": record.ContentType, "size_bytes": record.SizeBytes, "language": record.Language, "checksum": nullableKernelString(record.Checksum), "has_extracted_code": code}
}

func (s *Server) kernelBrowseFrames(ctx context.Context, access workspace.KernelFrameAccess, payload map[string]any) (map[string]any, error) {
	allowed := map[string]bool{"frame_id": true, "pattern": true, "project_id": true, "status": true, "roots_only": true, "has_task": true, "after": true, "before": true, "max_results": true, "offset": true, "include_tool_results": true}
	if err := rejectKernelInspectionFields(payload, allowed, "host.frames"); err != nil {
		return nil, err
	}
	frameID := strings.TrimSpace(kernelOptionalString(payload["frame_id"]))
	projectID := strings.TrimSpace(kernelOptionalString(payload["project_id"]))
	pattern := kernelOptionalString(payload["pattern"])
	status := strings.TrimSpace(kernelOptionalString(payload["status"]))
	defaultLimit := 30
	if frameID != "" {
		defaultLimit = 50
	} else if strings.TrimSpace(pattern) != "" {
		defaultLimit = 20
	}
	maxResults, err := kernelOptionalInt(payload, "max_results", defaultLimit, 1, 500)
	if err != nil {
		return nil, err
	}
	offset, err := kernelOptionalInt(payload, "offset", 0, 0, 1_000_000)
	if err != nil {
		return nil, err
	}
	rootsOnly, err := kernelOptionalBool(payload, "roots_only", true)
	if err != nil {
		return nil, err
	}
	hasTask, err := kernelOptionalBool(payload, "has_task", false)
	if err != nil {
		return nil, err
	}
	includeToolResults, err := kernelOptionalBool(payload, "include_tool_results", true)
	if err != nil {
		return nil, err
	}
	after, err := kernelOptionalTime(payload, "after")
	if err != nil {
		return nil, err
	}
	before, err := kernelOptionalTime(payload, "before")
	if err != nil {
		return nil, err
	}
	if frameID != "" {
		frameAccess, found, readErr := s.workspaceStore.GetKernelFrameAccessContext(ctx, frameID)
		if readErr != nil {
			return nil, classifyKernelHostError(readErr)
		}
		if !found || frameAccess.UserID != access.UserID {
			return nil, kernelruntime.NewHostCallError("not_found", "host.frames: frame not found")
		}
		if projectID != "" && projectID != "all" && projectID != frameAccess.Frame.ProjectID {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", "host.frames: frame and project filters disagree")
		}
		return s.kernelFrameDetail(frameAccess.Frame, pattern, maxResults, offset, includeToolResults)
	}
	projects, err := s.kernelInspectionProjects(access.UserID, access.Frame.ProjectID, projectID)
	if err != nil {
		return nil, err
	}
	var matcher *regexp.Regexp
	if strings.TrimSpace(pattern) != "" {
		if len([]byte(pattern)) > maxKernelFramePatternBytes {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", "host.frames: pattern exceeds 2048 bytes")
		}
		matcher, err = regexp.Compile("(?i)" + pattern)
		if err != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", "host.frames: invalid regular expression")
		}
	}
	frames := make([]workspace.Frame, 0)
	for _, project := range projects {
		for pageOffset := 0; ; pageOffset += 1000 {
			page, readErr := s.workspaceStore.ListFrames(project.ID, 1000, pageOffset)
			if readErr != nil {
				return nil, classifyKernelHostError(readErr)
			}
			frames = append(frames, page...)
			if len(page) < 1000 {
				break
			}
		}
	}
	rows := make([]map[string]any, 0)
	searchedFrames := 0
	for _, frame := range frames {
		if strings.EqualFold(frame.AgentName, "CONCIERGE") || frame.ID == access.Frame.ID && matcher != nil {
			continue
		}
		if rootsOnly && frame.ID != frame.RootFrameID {
			continue
		}
		if status != "" && frame.Status != status {
			continue
		}
		if after != nil && frame.UpdatedAt.Before(*after) {
			continue
		}
		if before != nil && !frame.UpdatedAt.Before(*before) {
			continue
		}
		metadata, _, readErr := s.workspaceStore.GetFrameRuntimeMetadata(frame.ID)
		if readErr != nil {
			return nil, classifyKernelHostError(readErr)
		}
		if hasTask && strings.TrimSpace(metadata.TaskSummary) == "" {
			continue
		}
		searchedFrames++
		row := kernelFrameBrowseRecord(frame, metadata)
		if matcher != nil {
			snippets, matchErr := s.kernelFrameSearchSnippets(frame, row, matcher)
			if matchErr != nil {
				return nil, matchErr
			}
			if len(snippets) == 0 {
				continue
			}
			row["input"] = nullableKernelString(metadata.TaskSummary)
			row["snippets"] = snippets
		}
		_, artifactCount, readErr := s.workspaceStore.ListKernelArtifacts(ctx, access.UserID, access.Frame.ProjectID, workspace.KernelArtifactBrowseOptions{ProjectID: frame.ProjectID, FrameID: frame.ID, IncludeIntermediate: true, Limit: 1})
		if readErr != nil {
			return nil, classifyKernelHostError(readErr)
		}
		row["artifact_count"] = artifactCount
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i]["updated_at"].(string) > rows[j]["updated_at"].(string) })
	total := len(rows)
	if offset >= total {
		rows = []map[string]any{}
	} else {
		rows = rows[offset:]
		if len(rows) > maxResults {
			rows = rows[:maxResults]
		}
	}
	scope := "single"
	if projectID == "all" {
		scope = "all"
	}
	result := map[string]any{"offset": offset}
	if matcher != nil {
		result["mode"] = "search"
		result["pattern"] = pattern
		result["matches"] = rows
		result["total_matches"] = total
		result["searched_frames"] = searchedFrames
		if scope == "all" {
			result["projects_searched"] = len(projects)
		} else {
			result["project_id"] = projects[0].ID
		}
	} else {
		result["mode"] = "browse"
		result["count"] = len(rows)
		result["scope"] = scope
		result["frames"] = rows
		if scope == "single" {
			result["project_id"] = projects[0].ID
		}
	}
	if offset == 0 {
		delete(result, "offset")
	}
	if offset+len(rows) < total {
		result["truncated"] = true
	}
	return result, nil
}

func (s *Server) kernelFrameDetail(frame workspace.Frame, pattern string, limit, offset int, includeToolResults bool) (map[string]any, error) {
	metadata, _, err := s.workspaceStore.GetFrameRuntimeMetadata(frame.ID)
	if err != nil {
		return nil, classifyKernelHostError(err)
	}
	var messages []any
	if stored, ok := metadata.ContextData["_messages"].([]any); ok {
		messages = append(messages, stored...)
	} else {
		events, readErr := s.workspaceStore.ListFrameEvents(frame.ID, 0, 1000)
		if readErr != nil {
			return nil, classifyKernelHostError(readErr)
		}
		messages = make([]any, 0, len(events))
		for _, event := range events {
			messages = append(messages, map[string]any{"id": event.ID, "sequence": event.Sequence, "type": event.Type, "payload": event.Payload, "created_at": event.CreatedAt.UTC().Format(time.RFC3339Nano)})
		}
	}
	total := len(messages)
	if !includeToolResults {
		filtered := messages[:0]
		for _, message := range messages {
			if kernelMessageIsToolResult(message) {
				continue
			}
			filtered = append(filtered, message)
		}
		messages = filtered
	}
	if strings.TrimSpace(pattern) != "" {
		matcher, compileErr := regexp.Compile("(?i)" + pattern)
		if compileErr != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", "host.frames: invalid regular expression")
		}
		filtered := messages[:0]
		for _, message := range messages {
			raw, _ := json.Marshal(message)
			if matcher.Match(raw) {
				filtered = append(filtered, message)
			}
		}
		messages = filtered
	}
	filteredTotal := len(messages)
	if offset >= filteredTotal {
		messages = []any{}
	} else {
		messages = messages[offset:]
		if len(messages) > limit {
			messages = messages[:limit]
		}
	}
	result := map[string]any{"mode": "detail", "frame_id": frame.ID, "name": nullableKernelString(frame.Name), "agent_name": frame.AgentName, "status": frame.Status, "project_id": frame.ProjectID, "created_at": frame.CreatedAt.UTC().Format(time.RFC3339Nano), "input": nullableKernelString(metadata.TaskSummary), "messages": messages, "total_messages": total, "truncated": offset+len(messages) < filteredTotal}
	if filteredTotal != total {
		result["filtered_messages"] = filteredTotal
	}
	if offset > 0 {
		result["offset"] = offset
	}
	return result, nil
}

func kernelFrameBrowseRecord(frame workspace.Frame, metadata workspace.FrameRuntimeMetadata) map[string]any {
	return map[string]any{"id": frame.ID, "project_id": frame.ProjectID, "parent_frame_id": nullableKernelString(frame.ParentFrameID), "root_frame_id": frame.RootFrameID, "agent_name": frame.AgentName, "status": frame.Status, "name": nullableKernelString(frame.Name), "task_summary": nullableKernelString(metadata.TaskSummary), "artifact_count": 0, "created_at": frame.CreatedAt.UTC().Format(time.RFC3339Nano), "updated_at": frame.UpdatedAt.UTC().Format(time.RFC3339Nano), "completed_at": nil}
}

func (s *Server) kernelFrameSearchSnippets(frame workspace.Frame, record map[string]any, matcher *regexp.Regexp) ([]string, error) {
	snippets := make([]string, 0, 3)
	appendMatch := func(value any) {
		if len(snippets) >= 3 {
			return
		}
		raw, _ := json.Marshal(value)
		if matcher.Match(raw) {
			text := string(raw)
			if len(text) > 500 {
				text = text[:500]
			}
			snippets = append(snippets, text)
		}
	}
	appendMatch(record)
	events, err := s.workspaceStore.ListFrameEvents(frame.ID, 0, 1000)
	if err != nil {
		return nil, classifyKernelHostError(err)
	}
	for _, event := range events {
		appendMatch(event)
	}
	return snippets, nil
}

func kernelMessageIsToolResult(message any) bool {
	mapping, ok := message.(map[string]any)
	if !ok {
		return false
	}
	if mapping["type"] == "tool_result" || mapping["role"] == "tool" {
		return true
	}
	content, _ := mapping["content"].([]any)
	for _, block := range content {
		if item, ok := block.(map[string]any); ok && item["type"] == "tool_result" {
			return true
		}
	}
	return false
}

func (s *Server) kernelInspectionProjects(ownerUserID, currentProjectID, requested string) ([]workspace.Project, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		requested = currentProjectID
	}
	if requested != "all" {
		owned, err := s.workspaceStore.ProjectOwnedBy(requested, ownerUserID)
		if err != nil {
			return nil, classifyKernelHostError(err)
		}
		if !owned {
			return nil, kernelruntime.NewHostCallError("not_found", "kernel inspection project not found")
		}
		project, found, err := s.workspaceStore.GetProject(requested)
		if err != nil || !found {
			return nil, classifyKernelHostError(err)
		}
		return []workspace.Project{project}, nil
	}
	projects := make([]workspace.Project, 0)
	for offset := 0; ; offset += 1000 {
		page, err := s.workspaceStore.ListProjectsForUser(ownerUserID, 1000, offset)
		if err != nil {
			return nil, classifyKernelHostError(err)
		}
		projects = append(projects, page...)
		if len(page) < 1000 {
			break
		}
	}
	return projects, nil
}

func rejectKernelInspectionFields(payload map[string]any, allowed map[string]bool, method string) error {
	for key := range payload {
		if !allowed[key] {
			return kernelruntime.NewHostCallError("invalid_arguments", fmt.Sprintf("%s: unknown option %q", method, key))
		}
	}
	return nil
}

func kernelOptionalString(value any) string {
	if value == nil {
		return ""
	}
	text, _ := value.(string)
	return text
}

func kernelOptionalBool(payload map[string]any, key string, fallback bool) (bool, error) {
	value, found := payload[key]
	if !found || value == nil {
		return fallback, nil
	}
	result, ok := value.(bool)
	if !ok {
		return false, kernelruntime.NewHostCallError("invalid_arguments", "host inspection "+key+" must be a bool")
	}
	return result, nil
}

func kernelOptionalInt(payload map[string]any, key string, fallback, minimum, maximum int) (int, error) {
	value, found := payload[key]
	if !found || value == nil {
		return fallback, nil
	}
	result, ok := value.(int)
	if !ok || result < minimum || result > maximum {
		return 0, kernelruntime.NewHostCallError("invalid_arguments", fmt.Sprintf("host inspection %s must be an integer from %d to %d", key, minimum, maximum))
	}
	return result, nil
}

func kernelOptionalTime(payload map[string]any, key string) (*time.Time, error) {
	value, found := payload[key]
	if !found || value == nil {
		return nil, nil
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return nil, kernelruntime.NewHostCallError("invalid_arguments", "host inspection "+key+" must be an ISO date or datetime")
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		parsed, err = time.Parse("2006-01-02", text)
	}
	if err != nil {
		return nil, kernelruntime.NewHostCallError("invalid_arguments", "host inspection "+key+" must be an ISO date or datetime")
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func kernelProjectOwned(store *workspace.Store, projectID, ownerUserID string) bool {
	owned, err := store.ProjectOwnedBy(projectID, ownerUserID)
	return err == nil && owned
}

func nullableKernelString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
