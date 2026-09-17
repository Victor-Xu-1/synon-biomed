package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	workspace "synon-go/internal/persistence/workspace"
)

var artifactReferencePattern = regexp.MustCompile(`\{\{artifact:([^{}]+)\}\}`)

type reproducibilityArchiveFile struct {
	Name     string
	Data     []byte
	Mode     os.FileMode
	Modified time.Time
}

type replaySegment struct {
	FrameID  string
	Agent    string
	Language string
	CondaEnv string
	Records  []workspace.ExecutionLogRecord
}

func (s *Server) writeSessionBundle(
	w http.ResponseWriter,
	r *http.Request,
	store *workspace.Store,
	session workspace.CompatibilitySessionExport,
) {
	if r.Method != http.MethodGet {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	scope := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("scope")))
	if scope == "" {
		scope = "full"
	}
	if scope != "full" && scope != "sliced" {
		writeV11Detail(w, http.StatusBadRequest, "scope must be full or sliced")
		return
	}
	versionID := strings.TrimSpace(r.URL.Query().Get("version_id"))
	if versionID == "" {
		versionID = strings.TrimSpace(r.URL.Query().Get("versionId"))
	}

	var selected workspace.ArtifactLineageRecord
	executionVersionID := ""
	if scope == "sliced" {
		if versionID == "" {
			writeV11Detail(w, http.StatusBadRequest, "version_id is required for sliced scope")
			return
		}
		lineage, found, err := store.GetArtifactVersionLineageRecord(versionID, true)
		if err != nil {
			writeAttachmentStoreError(w, err)
			return
		}
		if !found || !sessionLineageBelongsToRoot(store, lineage, session.RootFrameID) {
			writeV11Detail(w, http.StatusNotFound, "Version "+versionID+" is not part of frame tree "+session.RootFrameID)
			return
		}
		if !lineage.HasCellSources {
			writeV11Detail(w, http.StatusBadRequest, "Version "+versionID+" has no cell_sources")
			return
		}
		selected = lineage
		executionVersionID = versionID
	}

	records, err := store.ListExecutionLog(session.RootFrameID, executionVersionID)
	if err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	if len(records) == 0 {
		writeV11Detail(w, http.StatusNotFound, "No execution log for frame tree "+session.RootFrameID)
		return
	}
	segments := groupReplaySegments(records)
	if len(segments) == 0 {
		writeV11Detail(w, http.StatusBadRequest,
			"No replayable execution cells for frame tree "+session.RootFrameID+" — the session contained only host-side file edits, not kernel cells.")
		return
	}
	files, err := s.buildSessionBundleFiles(store, session, scope, executionVersionID, selected, segments)
	if err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	rootPrefix := shortIdentifier(session.RootFrameID)
	if err := writeReproducibilityArchive(w, "bundle-"+rootPrefix+"-"+scope+".zip", files); err != nil {
		return
	}
}

func (s *Server) writeScriptReproducibilityBundle(
	w http.ResponseWriter,
	store *workspace.Store,
	artifact workspace.Artifact,
	version workspace.ArtifactVersion,
	lineage workspace.ArtifactLineageRecord,
) {
	if lineage.Code == nil || strings.TrimSpace(*lineage.Code) == "" {
		writeV11Detail(w, http.StatusBadRequest,
			"Version "+version.ID+" has no extracted_code — it may predate code extraction, or extraction is still pending")
		return
	}
	if lineage.FrameID == nil || strings.TrimSpace(*lineage.FrameID) == "" {
		writeV11Detail(w, http.StatusBadRequest, "Version "+version.ID+" has no associated frame")
		return
	}
	language := "text"
	if lineage.Language != nil {
		language = normalizedReplayLanguage(*lineage.Language)
	}
	extension := scriptLanguageExtension(language)
	sourceName := "run." + extension
	if language == "bash" {
		sourceName = "run-source.sh"
	}
	source, inputs, err := scriptBundleInputs(store, artifact.ProjectID, *lineage.Code)
	if err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	files := make([]reproducibilityArchiveFile, 0, len(inputs)+6)
	files = append(files, reproducibilityArchiveFile{Name: sourceName, Data: []byte(source), Modified: version.CreatedAt})
	for _, input := range inputs {
		input.Name = "inputs/" + input.Name
		input.Modified = version.CreatedAt
		files = append(files, input)
	}
	environmentData, err := encodeEnvironmentYAML(lineage.EnvironmentSnapshot, "script-"+shortIdentifier(version.ID))
	if err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	if len(environmentData) > 0 {
		files = append(files, reproducibilityArchiveFile{Name: "environment.yml", Data: environmentData, Modified: version.CreatedAt})
	}
	if tape := operonTape(lineage.Interactions); len(tape) > 0 {
		files = append(files,
			reproducibilityArchiveFile{Name: "operon_tape.json", Data: tape, Modified: version.CreatedAt},
			reproducibilityArchiveFile{Name: "synon_operon_replay.py", Data: []byte(operonReplayShim), Modified: version.CreatedAt},
		)
	}
	files = append(files,
		reproducibilityArchiveFile{
			Name: "run.sh", Data: []byte(scriptBundleRunner(version.ID, language, sourceName, len(environmentData) > 0)),
			Mode: 0o755, Modified: version.CreatedAt,
		},
		reproducibilityArchiveFile{
			Name: "README.md", Data: []byte(scriptBundleREADME(artifact, version, lineage, language)), Modified: version.CreatedAt,
		},
	)
	_ = writeReproducibilityArchive(w, "script-"+shortIdentifier(version.ID)+".zip", files)
}

func scriptLanguageExtension(language string) string {
	switch language {
	case "python":
		return "py"
	case "r":
		return "R"
	case "bash":
		return "sh"
	default:
		return "txt"
	}
}

func scriptBundleInputs(
	store *workspace.Store, projectID, source string,
) (string, []reproducibilityArchiveFile, error) {
	matches := artifactReferencePattern.FindAllStringSubmatch(source, -1)
	seen := make(map[string]string, len(matches))
	files := make([]reproducibilityArchiveFile, 0, len(matches))
	usedNames := make(map[string]int)
	for _, match := range matches {
		artifactID := strings.TrimSpace(match[1])
		if artifactID == "" {
			continue
		}
		if name, found := seen[artifactID]; found {
			source = strings.ReplaceAll(source, match[0], "inputs/"+name)
			continue
		}
		inputArtifact, _, content, found, err := store.OpenCurrentArtifactContent(artifactID)
		if err != nil {
			return "", nil, err
		}
		if !found || inputArtifact.ProjectID != projectID {
			if content != nil {
				_ = content.Close()
			}
			return "", nil, fmt.Errorf("referenced artifact %q not found in script project", artifactID)
		}
		data, readErr := io.ReadAll(content)
		closeErr := content.Close()
		if readErr != nil {
			return "", nil, fmt.Errorf("read script input %q: %w", artifactID, readErr)
		}
		if closeErr != nil {
			return "", nil, fmt.Errorf("close script input %q: %w", artifactID, closeErr)
		}
		name := uniqueArchiveFilename(safeArchiveFilename(inputArtifact.Name, inputArtifact.ID), usedNames)
		seen[artifactID] = name
		source = strings.ReplaceAll(source, match[0], "inputs/"+name)
		files = append(files, reproducibilityArchiveFile{Name: name, Data: data})
	}
	return source, files, nil
}

func scriptBundleRunner(versionID, language, sourceName string, hasEnvironment bool) string {
	command := "cat " + sourceName
	switch language {
	case "python":
		command = "python " + sourceName
	case "r":
		command = "Rscript " + sourceName
	case "bash":
		command = "bash " + sourceName
	}
	prefix := "#!/usr/bin/env bash\nset -euo pipefail\ncd \"$(dirname \"$0\")\"\n"
	if !hasEnvironment {
		return prefix + command + "\n"
	}
	environmentName := "script-" + shortIdentifier(versionID)
	return prefix +
		"command -v conda >/dev/null 2>&1 || { echo \"conda is required to recreate this environment\" >&2; exit 1; }\n" +
		"if conda env list | awk '{print $1}' | grep -Fxq '" + environmentName + "'; then\n" +
		"  conda env update --name '" + environmentName + "' --file environment.yml --prune\n" +
		"else\n  conda env create --name '" + environmentName + "' --file environment.yml\nfi\n" +
		"conda run --no-capture-output --name '" + environmentName + "' " + command + "\n"
}

func scriptBundleREADME(
	artifact workspace.Artifact,
	version workspace.ArtifactVersion,
	lineage workspace.ArtifactLineageRecord,
	language string,
) string {
	frameID := ""
	if lineage.FrameID != nil {
		frameID = *lineage.FrameID
	}
	return "# Synon script reproducibility bundle\n\n" +
		"Artifact: `" + artifact.ID + "`  \nVersion: `" + version.ID + "`  \nFrame: `" + frameID + "`  \nLanguage: `" + language + "`\n\n" +
		"Execute `./run.sh`. Referenced artifacts are available under `inputs/`.\n"
}

const operonReplayShim = `"""Minimal deterministic reader for captured Synon host-call tapes."""
import json
from pathlib import Path


def load_calls(path="operon_tape.json"):
    return json.loads(Path(path).read_text(encoding="utf-8"))["calls"]
`

func sessionLineageBelongsToRoot(store *workspace.Store, lineage workspace.ArtifactLineageRecord, rootFrameID string) bool {
	if strings.TrimSpace(lineage.RootFrameID) != "" {
		return lineage.RootFrameID == rootFrameID
	}
	if lineage.FrameID == nil {
		return false
	}
	frame, found, err := store.GetFrame(*lineage.FrameID)
	return err == nil && found && frame.RootFrameID == rootFrameID
}

func groupReplaySegments(records []workspace.ExecutionLogRecord) []replaySegment {
	segments := make([]replaySegment, 0)
	indexes := make(map[string]int)
	for _, record := range records {
		if strings.EqualFold(record.KernelKind, "host_tool") || strings.EqualFold(record.Language, "diff") {
			continue
		}
		language := normalizedReplayLanguage(record.Language)
		environment := strings.TrimSpace(record.CondaEnv)
		if environment == "" {
			environment = "base"
		}
		key := record.FrameID + "\x00" + record.KernelKind + "\x00" + environment + "\x00" + language
		index, found := indexes[key]
		if !found {
			index = len(segments)
			indexes[key] = index
			segments = append(segments, replaySegment{
				FrameID: record.FrameID, Agent: record.AgentName, Language: language, CondaEnv: environment,
			})
		}
		segments[index].Records = append(segments[index].Records, record)
	}
	return segments
}

func normalizedReplayLanguage(language string) string {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "py", "python3":
		return "python"
	case "shell", "sh", "zsh":
		return "bash"
	default:
		if strings.TrimSpace(language) == "" {
			return "text"
		}
		return strings.ToLower(strings.TrimSpace(language))
	}
}

func (s *Server) buildSessionBundleFiles(
	store *workspace.Store,
	session workspace.CompatibilitySessionExport,
	scope, versionID string,
	selected workspace.ArtifactLineageRecord,
	segments []replaySegment,
) ([]reproducibilityArchiveFile, error) {
	modified, _ := time.Parse(time.RFC3339Nano, session.ExportedAt)
	files := make([]reproducibilityArchiveFile, 0, len(segments)*6+2)
	manifestSegments := make([]map[string]any, 0, len(segments))
	for index, segment := range segments {
		framePrefix := shortIdentifier(segment.FrameID)
		agent := safeArchiveFilename(strings.ToLower(segment.Agent), "agent")
		environment := safeArchiveFilename(segment.CondaEnv, "base")
		base := fmt.Sprintf("frames/%s-%s/%02d-%s/", agent, framePrefix, index+1, environment)
		segment, inputs, err := replaySegmentInputs(store, session.ProjectID, segment)
		if err != nil {
			return nil, err
		}
		notebook, err := buildReplayNotebook(segment)
		if err != nil {
			return nil, err
		}
		files = append(files, reproducibilityArchiveFile{Name: base + "notebook.ipynb", Data: notebook, Modified: modified})
		lineage := selected
		if scope == "full" {
			lineage = sessionSegmentLineage(store, session.Artifacts, segment.FrameID)
		}
		if environmentYAML, err := encodeEnvironmentYAML(lineage.EnvironmentSnapshot, segment.CondaEnv); err != nil {
			return nil, err
		} else if len(environmentYAML) > 0 {
			files = append(files, reproducibilityArchiveFile{Name: base + "environment.yml", Data: environmentYAML, Modified: modified})
		}
		if tape := operonTape(lineage.Interactions); len(tape) > 0 {
			files = append(files, reproducibilityArchiveFile{Name: base + "operon_tape.json", Data: tape, Modified: modified})
		}
		for _, input := range inputs {
			input.Name = base + "inputs/" + input.Name
			input.Modified = modified
			files = append(files, input)
		}
		outputVersionID := ""
		if scope == "sliced" {
			outputVersionID = versionID
		}
		outputs, err := sessionSegmentOutputs(store, session.Artifacts, segment.FrameID, outputVersionID)
		if err != nil {
			return nil, err
		}
		for _, output := range outputs {
			output.Name = base + "outputs/" + output.Name
			output.Modified = modified
			files = append(files, output)
		}
		files = append(files, reproducibilityArchiveFile{
			Name: base + "run.sh", Data: []byte(sessionReplayScript(segment)), Mode: 0o755, Modified: modified,
		})
		manifestSegments = append(manifestSegments, map[string]any{
			"frame_id": segment.FrameID, "agent_name": segment.Agent, "language": segment.Language,
			"conda_env": segment.CondaEnv, "cell_count": len(segment.Records), "path": strings.TrimSuffix(base, "/"),
		})
	}
	manifest := map[string]any{
		"schema_version": 1, "kind": "synon-session-reproducibility-bundle",
		"root_frame_id": session.RootFrameID, "project_id": session.ProjectID,
		"scope": scope, "version_id": nullableBundleString(versionID),
		"generated_at": session.ExportedAt, "segments": manifestSegments,
	}
	manifestData, err := indentedJSON(manifest)
	if err != nil {
		return nil, err
	}
	files = append(files,
		reproducibilityArchiveFile{Name: "manifest.json", Data: manifestData, Modified: modified},
		reproducibilityArchiveFile{Name: "README.md", Data: []byte(sessionBundleREADME(session, scope)), Modified: modified},
	)
	return files, nil
}

func sessionSegmentLineage(
	store *workspace.Store, artifacts []workspace.CompatibilitySessionArtifact, frameID string,
) workspace.ArtifactLineageRecord {
	for _, artifact := range artifacts {
		if artifact.FrameID == nil || *artifact.FrameID != frameID {
			continue
		}
		lineage, found, err := store.GetArtifactVersionLineageRecord(artifact.VersionID, true)
		if err == nil && found && lineage.EnvironmentSnapshot != nil {
			return lineage
		}
	}
	return workspace.ArtifactLineageRecord{}
}

func buildReplayNotebook(segment replaySegment) ([]byte, error) {
	cells := make([]map[string]any, 0, len(segment.Records))
	for _, record := range segment.Records {
		outputs := make([]map[string]any, 0, 2)
		if record.Stdout != "" {
			outputs = append(outputs, map[string]any{"output_type": "stream", "name": "stdout", "text": splitNotebookLines(record.Stdout)})
		}
		if record.Stderr != "" {
			outputs = append(outputs, map[string]any{"output_type": "stream", "name": "stderr", "text": splitNotebookLines(record.Stderr)})
		}
		cells = append(cells, map[string]any{
			"cell_type": "code", "execution_count": record.CellIndex,
			"metadata": map[string]any{
				"synon_execution_id": record.ID, "exit_status": record.ExitStatus,
				"executed_at": record.ExecutedAt.UTC().Format(time.RFC3339Nano),
			},
			"outputs": outputs, "source": splitNotebookLines(record.Source),
		})
	}
	notebook := map[string]any{
		"cells": cells,
		"metadata": map[string]any{
			"kernelspec":    map[string]any{"display_name": segment.CondaEnv, "language": segment.Language, "name": segment.CondaEnv},
			"language_info": map[string]any{"name": segment.Language},
			"synon":         map[string]any{"frame_id": segment.FrameID, "agent_name": segment.Agent},
		},
		"nbformat": 4, "nbformat_minor": 5,
	}
	return indentedJSON(notebook)
}

func splitNotebookLines(value string) []string {
	if value == "" {
		return []string{}
	}
	parts := strings.SplitAfter(value, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func encodeEnvironmentYAML(snapshot any, fallbackName string) ([]byte, error) {
	if snapshot == nil {
		return nil, nil
	}
	if text, ok := snapshot.(string); ok {
		text = strings.TrimSpace(text)
		if text == "" {
			return nil, nil
		}
		return []byte(text + "\n"), nil
	}
	if mapping, ok := snapshot.(map[string]any); ok {
		if _, found := mapping["name"]; !found {
			copy := make(map[string]any, len(mapping)+1)
			copy["name"] = fallbackName
			for key, value := range mapping {
				copy[key] = value
			}
			snapshot = copy
		}
	}
	encoded, err := yaml.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("encode reproducibility environment: %w", err)
	}
	return encoded, nil
}

func operonTape(interactions any) []byte {
	items, ok := interactions.([]any)
	if !ok {
		return nil
	}
	hostCalls := make([]any, 0)
	for _, item := range items {
		mapping, ok := item.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := mapping["kind"].(string)
		if kind == "host_tool" || kind == "host_call" || kind == "operon" {
			hostCalls = append(hostCalls, item)
		}
	}
	if len(hostCalls) == 0 {
		return nil
	}
	encoded, err := indentedJSON(map[string]any{"version": 1, "calls": hostCalls})
	if err != nil {
		return nil
	}
	return encoded
}

func replaySegmentInputs(
	store *workspace.Store, projectID string, segment replaySegment,
) (replaySegment, []reproducibilityArchiveFile, error) {
	seen := make(map[string]string)
	files := make([]reproducibilityArchiveFile, 0)
	usedNames := make(map[string]int)
	for recordIndex := range segment.Records {
		matches := artifactReferencePattern.FindAllStringSubmatch(segment.Records[recordIndex].Source, -1)
		for _, match := range matches {
			artifactID := strings.TrimSpace(match[1])
			if artifactID == "" {
				continue
			}
			if name, found := seen[artifactID]; found {
				segment.Records[recordIndex].Source = strings.ReplaceAll(segment.Records[recordIndex].Source, match[0], "inputs/"+name)
				continue
			}
			artifact, _, content, found, err := store.OpenCurrentArtifactContent(artifactID)
			if err != nil {
				return replaySegment{}, nil, err
			}
			if !found || artifact.ProjectID != projectID {
				if content != nil {
					_ = content.Close()
				}
				return replaySegment{}, nil, fmt.Errorf("referenced artifact %q not found in session project", artifactID)
			}
			data, readErr := io.ReadAll(content)
			closeErr := content.Close()
			if readErr != nil {
				return replaySegment{}, nil, fmt.Errorf("read referenced artifact %q: %w", artifactID, readErr)
			}
			if closeErr != nil {
				return replaySegment{}, nil, fmt.Errorf("close referenced artifact %q: %w", artifactID, closeErr)
			}
			name := uniqueArchiveFilename(safeArchiveFilename(artifact.Name, artifact.ID), usedNames)
			seen[artifactID] = name
			segment.Records[recordIndex].Source = strings.ReplaceAll(segment.Records[recordIndex].Source, match[0], "inputs/"+name)
			files = append(files, reproducibilityArchiveFile{Name: name, Data: data})
		}
	}
	return segment, files, nil
}

func sessionSegmentOutputs(
	store *workspace.Store, artifacts []workspace.CompatibilitySessionArtifact, frameID, versionID string,
) ([]reproducibilityArchiveFile, error) {
	files := make([]reproducibilityArchiveFile, 0)
	usedNames := make(map[string]int)
	for _, exported := range artifacts {
		if exported.FrameID == nil || *exported.FrameID != frameID {
			continue
		}
		if versionID != "" && exported.VersionID != versionID {
			continue
		}
		artifact, _, content, found, err := store.OpenArtifactVersionContent(exported.VersionID)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		data, err := io.ReadAll(content)
		closeErr := content.Close()
		if err != nil {
			return nil, fmt.Errorf("read session output %q: %w", exported.VersionID, err)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close session output %q: %w", exported.VersionID, closeErr)
		}
		name := uniqueArchiveFilename(safeArchiveFilename(artifact.Name, artifact.ID), usedNames)
		files = append(files, reproducibilityArchiveFile{Name: name, Data: data})
	}
	return files, nil
}

func sessionReplayScript(segment replaySegment) string {
	environmentFile := "environment.yml"
	return `#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
if [[ -f ` + environmentFile + ` ]]; then
  command -v conda >/dev/null 2>&1 || { echo "conda is required to recreate this environment" >&2; exit 1; }
  env_name="synon-` + shortIdentifier(segment.FrameID) + `"
  if conda env list | awk '{print $1}' | grep -Fxq "$env_name"; then
    conda env update --name "$env_name" --file ` + environmentFile + ` --prune
  else
    conda env create --name "$env_name" --file ` + environmentFile + `
  fi
  conda run --no-capture-output --name "$env_name" jupyter nbconvert --to notebook --execute --inplace notebook.ipynb
else
  command -v jupyter >/dev/null 2>&1 || { echo "jupyter is required to replay this notebook" >&2; exit 1; }
  jupyter nbconvert --to notebook --execute --inplace notebook.ipynb
fi
`
}

func sessionBundleREADME(session workspace.CompatibilitySessionExport, scope string) string {
	return "# Synon reproducibility bundle\n\n" +
		"Session: `" + session.RootFrameID + "`  \nScope: `" + scope + "`\n\n" +
		"Run each segment's `run.sh` from its own directory. Inputs and captured outputs are stored beside the notebook.\n"
}

func nullableBundleString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func indentedJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func shortIdentifier(value string) string {
	value = safeArchiveFilename(strings.TrimSpace(value), "unknown")
	if len(value) > 8 {
		return value[:8]
	}
	return value
}

func writeReproducibilityArchive(w http.ResponseWriter, filename string, files []reproducibilityArchiveFile) error {
	temporary, err := os.CreateTemp("", "synon-reproducibility-*.zip")
	if err != nil {
		writeAttachmentStoreError(w, fmt.Errorf("create reproducibility archive: %w", err))
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	archive := zip.NewWriter(temporary)
	sort.SliceStable(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		name := path.Clean(strings.ReplaceAll(file.Name, `\`, "/"))
		if name == "." || strings.HasPrefix(name, "../") || path.IsAbs(name) {
			err = fmt.Errorf("unsafe reproducibility archive path %q", file.Name)
			break
		}
		if _, found := seen[name]; found {
			err = fmt.Errorf("duplicate reproducibility archive path %q", name)
			break
		}
		seen[name] = struct{}{}
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		modified := file.Modified
		if modified.IsZero() {
			modified = time.Unix(0, 0).UTC()
		}
		header.SetModTime(modified)
		mode := file.Mode
		if mode == 0 {
			mode = 0o644
		}
		header.SetMode(mode)
		var entry io.Writer
		entry, err = archive.CreateHeader(header)
		if err == nil {
			_, err = io.Copy(entry, bytes.NewReader(file.Data))
		}
		if err != nil {
			break
		}
	}
	if closeErr := archive.Close(); err == nil {
		err = closeErr
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		writeAttachmentStoreError(w, fmt.Errorf("build reproducibility archive: %w", err))
		return err
	}
	opened, err := os.Open(temporaryName)
	if err != nil {
		writeAttachmentStoreError(w, fmt.Errorf("open reproducibility archive: %w", err))
		return err
	}
	defer opened.Close()
	info, err := opened.Stat()
	if err != nil {
		writeAttachmentStoreError(w, fmt.Errorf("stat reproducibility archive: %w", err))
		return err
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, err = io.Copy(w, opened)
	if err != nil && !errors.Is(err, http.ErrHandlerTimeout) {
		return err
	}
	return nil
}
