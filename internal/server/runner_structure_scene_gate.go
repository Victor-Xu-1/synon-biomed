package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// structureSceneManifestSchema is deliberately a single, versioned contract.
// It is consumed by the existing completion gate and by the existing native
// structure preview path; it is not a second delivery protocol.
const (
	structureSceneManifestSchema   = "synon.structure-scene.v1"
	maxStructureSceneManifestBytes = 1 << 20
)

type structureSceneManifest struct {
	Schema            string                      `json:"schema"`
	SceneID           string                      `json:"scene_id"`
	MotherStructure   structureSceneArtifactRef   `json:"mother_structure"`
	DerivedStructures []structureSceneArtifactRef `json:"derived_structures"`
	Layers            []structureSceneLayer       `json:"layers"`
	PreviewImage      structureScenePreviewImage  `json:"preview_image"`
}

type structureSceneArtifactRef struct {
	Name      string `json:"name"`
	VersionID string `json:"version_id"`
}

type structureSceneLayer struct {
	VersionID      string `json:"version_id"`
	Role           string `json:"role"`
	Representation string `json:"representation"`
	Visible        bool   `json:"visible"`
}

type structureScenePreviewImage struct {
	Name      string `json:"name"`
	VersionID string `json:"version_id"`
	SHA256    string `json:"sha256"`
}

// verifyStructureSceneEvidence applies the generic derived-structure
// delivery contract to the current snapshot inventory. A scene is required
// whenever a task delivers two or more user-visible structures; this keeps
// comparison/derived workflows honest without branching on a tool or domain
// name. Single-structure tasks retain their existing completion behavior.
func (s *Server) verifyStructureSceneEvidence(artifacts []sessionReviewerArtifactEvidence) error {
	if s == nil || s.workspaceStore == nil {
		return errors.New("structure scene workspace store is unavailable")
	}
	snapshotArtifacts := make([]sessionReviewerArtifactEvidence, 0, len(artifacts))
	for _, artifact := range artifacts {
		retention, found, err := s.workspaceStore.ArtifactRetentionMode(artifact.ArtifactID)
		if err != nil {
			return fmt.Errorf("read structure scene retention for %s: %w", artifact.Name, err)
		}
		if !found || retention != "snapshot" {
			continue
		}
		intermediate, found, err := s.workspaceStore.ArtifactVersionIntermediate(artifact.VersionID)
		if err != nil {
			return fmt.Errorf("read structure scene version state for %s: %w", artifact.Name, err)
		}
		if found && intermediate {
			continue
		}
		snapshotArtifacts = append(snapshotArtifacts, artifact)
	}

	structures := make([]sessionReviewerArtifactEvidence, 0, len(snapshotArtifacts))
	for _, artifact := range snapshotArtifacts {
		if structureArtifactName(artifact.Name) {
			structures = append(structures, artifact)
		}
	}
	if len(structures) < 2 {
		return nil
	}

	manifestArtifact, manifest, failures := s.findStructureSceneManifest(snapshotArtifacts)
	if manifestArtifact == nil {
		return &sessionRunnerVisualArtifactValidationRequired{
			Artifacts:              structureSceneArtifactNames(structures),
			StructureSceneFailures: failures,
		}
	}
	if len(failures) > 0 {
		return &sessionRunnerVisualArtifactValidationRequired{
			Artifacts:              append(structureSceneArtifactNames(structures), manifestArtifact.Name),
			StructureSceneFailures: failures,
		}
	}

	images := make([]sessionReviewerArtifactEvidence, 0, len(snapshotArtifacts))
	for _, artifact := range snapshotArtifacts {
		if visualArtifactName(artifact.Name) {
			images = append(images, artifact)
		}
	}
	failures = validateStructureSceneManifest(manifest, structures, images)
	if len(failures) > 0 {
		return &sessionRunnerVisualArtifactValidationRequired{
			Artifacts:              append(structureSceneArtifactNames(structures), manifestArtifact.Name),
			StructureSceneFailures: failures,
		}
	}

	validatedHashes, err := s.validatedVisualReviewImageHashes()
	if err != nil {
		return err
	}
	preview := findArtifactByVersionID(images, manifest.PreviewImage.VersionID)
	if preview == nil {
		// validateStructureSceneManifest already reports this, but keeping this
		// guard makes the hash lookup fail closed if the contract is extended.
		return &sessionRunnerVisualArtifactValidationRequired{
			Artifacts:              append(structureSceneArtifactNames(structures), manifestArtifact.Name),
			StructureSceneFailures: []string{"preview_image is not a current snapshot image"},
		}
	}
	if _, found := validatedHashes[strings.ToLower(strings.TrimSpace(preview.ContentSHA256))]; !found {
		return &sessionRunnerVisualArtifactValidationRequired{
			Artifacts:              append(structureSceneArtifactNames(structures), preview.Name),
			StructureSceneFailures: []string{"preview_image is not bound to a passing VisualReview record"},
		}
	}
	return nil
}

func (s *Server) findStructureSceneManifest(
	artifacts []sessionReviewerArtifactEvidence,
) (*sessionReviewerArtifactEvidence, structureSceneManifest, []string) {
	for index := range artifacts {
		artifact := &artifacts[index]
		if strings.ToLower(filepath.Ext(strings.TrimSpace(artifact.Name))) != ".json" {
			continue
		}
		content, err := s.readStructureSceneArtifact(artifact.VersionID)
		if err != nil {
			continue
		}
		var envelope struct {
			Schema string `json:"schema"`
		}
		if json.Unmarshal(content, &envelope) != nil || envelope.Schema != structureSceneManifestSchema {
			continue
		}
		var manifest structureSceneManifest
		if err := json.Unmarshal(content, &manifest); err != nil {
			return artifact, structureSceneManifest{}, []string{"structure-scene manifest is not valid JSON: " + err.Error()}
		}
		return artifact, manifest, nil
	}
	return nil, structureSceneManifest{}, []string{"missing snapshot manifest with schema " + structureSceneManifestSchema}
}

func (s *Server) readStructureSceneArtifact(versionID string) ([]byte, error) {
	_, _, reader, found, err := s.workspaceStore.OpenArtifactVersionContent(versionID)
	if err != nil {
		return nil, err
	}
	if !found || reader == nil {
		return nil, errors.New("artifact version content is unavailable")
	}
	defer reader.Close()
	content, err := io.ReadAll(io.LimitReader(reader, maxStructureSceneManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxStructureSceneManifestBytes {
		return nil, errors.New("structure-scene manifest exceeds the size limit")
	}
	return content, nil
}

func validateStructureSceneManifest(
	manifest structureSceneManifest,
	structures []sessionReviewerArtifactEvidence,
	images []sessionReviewerArtifactEvidence,
) []string {
	failures := []string{}
	if strings.TrimSpace(manifest.Schema) != structureSceneManifestSchema {
		failures = append(failures, "schema must be "+structureSceneManifestSchema)
	}
	if strings.TrimSpace(manifest.SceneID) == "" {
		failures = append(failures, "scene_id is required")
	}
	if len(manifest.DerivedStructures) == 0 {
		failures = append(failures, "derived_structures must contain at least one structure")
	}
	mother, motherFailure := resolveStructureSceneReference(manifest.MotherStructure, structures, "mother_structure")
	if motherFailure != "" {
		failures = append(failures, motherFailure)
	}
	requiredLayers := map[string]struct{}{}
	if mother != nil {
		requiredLayers[mother.VersionID] = struct{}{}
	}
	seenDerived := map[string]struct{}{}
	for index, reference := range manifest.DerivedStructures {
		label := fmt.Sprintf("derived_structures[%d]", index)
		derived, failure := resolveStructureSceneReference(reference, structures, label)
		if failure != "" {
			failures = append(failures, failure)
			continue
		}
		if mother != nil && derived.VersionID == mother.VersionID {
			failures = append(failures, label+" must differ from mother_structure")
			continue
		}
		if _, duplicate := seenDerived[derived.VersionID]; duplicate {
			failures = append(failures, label+" repeats a structure version")
			continue
		}
		seenDerived[derived.VersionID] = struct{}{}
		requiredLayers[derived.VersionID] = struct{}{}
	}
	if len(manifest.Layers) == 0 {
		failures = append(failures, "layers must expose mother_structure and every derived structure")
	}
	layerVersions := map[string]struct{}{}
	for index, layer := range manifest.Layers {
		versionID := strings.TrimSpace(layer.VersionID)
		label := fmt.Sprintf("layers[%d]", index)
		if versionID == "" {
			failures = append(failures, label+" version_id is required")
			continue
		}
		if _, duplicate := layerVersions[versionID]; duplicate {
			failures = append(failures, label+" repeats a version_id")
			continue
		}
		layerVersions[versionID] = struct{}{}
		if _, required := requiredLayers[versionID]; required && !layer.Visible {
			failures = append(failures, label+" must be visible")
		}
		if _, required := requiredLayers[versionID]; required && strings.TrimSpace(layer.Representation) == "" {
			failures = append(failures, label+" representation is required")
		}
	}
	for versionID := range requiredLayers {
		if _, found := layerVersions[versionID]; !found {
			failures = append(failures, "layers omit required structure version "+versionID)
		}
	}

	preview := findArtifactByVersionID(images, manifest.PreviewImage.VersionID)
	if preview == nil {
		failures = append(failures, "preview_image must reference a current snapshot image")
	} else {
		if strings.TrimSpace(manifest.PreviewImage.Name) == "" || manifest.PreviewImage.Name != preview.Name {
			failures = append(failures, "preview_image name does not match its version")
		}
		manifestHash := strings.ToLower(strings.TrimSpace(manifest.PreviewImage.SHA256))
		artifactHash := strings.ToLower(strings.TrimSpace(preview.ContentSHA256))
		if !isSHA256(manifestHash) || manifestHash != artifactHash {
			failures = append(failures, "preview_image sha256 does not match the immutable artifact version")
		}
	}
	return uniqueSortedStrings(failures)
}

func resolveStructureSceneReference(
	reference structureSceneArtifactRef,
	structures []sessionReviewerArtifactEvidence,
	label string,
) (*sessionReviewerArtifactEvidence, string) {
	name := strings.TrimSpace(reference.Name)
	versionID := strings.TrimSpace(reference.VersionID)
	if name == "" || versionID == "" {
		return nil, label + " requires exact name and version_id"
	}
	artifact := findArtifactByVersionID(structures, versionID)
	if artifact == nil {
		return nil, label + " does not reference a current snapshot structure"
	}
	if artifact.Name != name {
		return nil, label + " name does not match its immutable version"
	}
	return artifact, ""
}

func findArtifactByVersionID(artifacts []sessionReviewerArtifactEvidence, versionID string) *sessionReviewerArtifactEvidence {
	versionID = strings.TrimSpace(versionID)
	if versionID == "" {
		return nil
	}
	for index := range artifacts {
		if strings.TrimSpace(artifacts[index].VersionID) == versionID {
			return &artifacts[index]
		}
	}
	return nil
}

func structureSceneArtifactNames(artifacts []sessionReviewerArtifactEvidence) []string {
	names := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		names = append(names, artifact.Name)
	}
	return uniqueSortedStrings(names)
}

func structureArtifactName(name string) bool {
	extension := strings.ToLower(strings.TrimSpace(filepath.Ext(name)))
	if extension == ".gz" || extension == ".bgz" {
		name = strings.TrimSuffix(name, filepath.Ext(name))
		extension = strings.ToLower(filepath.Ext(name))
	}
	switch extension {
	case ".pdb", ".pdbqt", ".pqr", ".cif", ".mmcif", ".mol", ".sdf", ".mol2", ".gro", ".xyz":
		return true
	default:
		return false
	}
}

func isSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f' || character >= 'A' && character <= 'F') {
			return false
		}
	}
	return true
}
