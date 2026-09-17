package server

import (
	"context"

	"encoding/json"

	"errors"

	"io"

	"path/filepath"

	"sort"

	"strings"

	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func (s *Server) unresolvedSessionRunnerArtifactReferenceCount(
	session sessionstore.Session,
	run *sessionRunnerChatRun,
	commits []transcriptstore.ArtifactReferenceInput,
	content string,
) (int, error) {
	references, err := s.unresolvedSessionRunnerArtifactReferences(session, run, commits, content)
	return len(references), err
}

// normalizeSessionRunnerArtifactReferencesToCurrentVersions upgrades a
// user-facing artifact placeholder that names a durable artifact identity to
// the exact immutable version produced by this execution. Providers often
// copy the stable artifact URI/ID from a save receipt even though the final
// contract requires a version ID; this is a lossless canonicalization because
// the artifact head is resolved from the same append-only commit ledger. It
// never invents a version or rewrites an identity that is not in the current
// ledger, so stale and cross-project references still fail closed.
func normalizeSessionRunnerArtifactReferencesToCurrentVersions(
	content string,
	commits []transcriptstore.ArtifactReferenceInput,
) (string, bool) {
	byIdentity := make(map[string]string)
	for _, commit := range latestSessionRunnerArtifactReferences(commits) {
		if commit.Relation != transcriptstore.ArtifactRelationProduced {
			continue
		}
		artifactID := strings.TrimSpace(commit.ArtifactID)
		versionID := strings.TrimSpace(commit.VersionID)
		if artifactID == "" || versionID == "" ||
			!sessionRunnerCanonicalArtifactReferenceIDPattern.MatchString(versionID) {
			continue
		}
		byIdentity[artifactID] = versionID
		byIdentity["art_"+artifactID] = versionID
	}
	if len(byIdentity) == 0 {
		return content, false
	}
	matches := artifactReferencePattern.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return content, false
	}
	var builder strings.Builder
	last := 0
	changed := false
	for _, match := range matches {
		if len(match) < 4 || match[2] < 0 || match[3] <= match[2] || match[3] > len(content) {
			continue
		}
		reference := strings.TrimSpace(content[match[2]:match[3]])
		versionID, found := byIdentity[reference]
		if !found || versionID == reference {
			continue
		}
		if !changed {
			builder.Grow(len(content))
		}
		builder.WriteString(content[last:match[2]])
		builder.WriteString(versionID)
		last = match[3]
		changed = true
	}
	if !changed {
		return content, false
	}
	builder.WriteString(content[last:])
	return builder.String(), true
}

// unresolvedSessionRunnerArtifactReferences is the diagnostic companion to
// unresolvedSessionRunnerArtifactReferenceCount.  Completion validation keeps
// the count-oriented API for callers that only need a gate, while correction
// turns receive the exact immutable references that could not be resolved.
// Returning the references in stable order makes a repeated validator result
// deterministic and gives the model an actionable correction target without
// changing the acceptance policy.
func (s *Server) unresolvedSessionRunnerArtifactReferences(
	session sessionstore.Session,
	run *sessionRunnerChatRun,
	commits []transcriptstore.ArtifactReferenceInput,
	content string,
) ([]string, error) {
	matches := artifactReferencePattern.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil, nil
	}
	projectID := sessionRunnerProjectID(session)
	if s == nil || s.workspaceStore == nil || projectID == "" {
		return nil, errors.New("runner artifact authority is unavailable")
	}
	canonicalAttempt := run != nil && run.Transcript != nil
	currentAttemptVersions := map[string]struct{}{}
	for _, commit := range commits {
		currentAttemptVersions[commit.VersionID] = struct{}{}
	}
	unresolved := map[string]struct{}{}
	for _, match := range matches {
		if len(match) != 2 {
			continue
		}
		reference := strings.TrimSpace(match[1])
		if reference == "" {
			unresolved[reference] = struct{}{}
			continue
		}
		if canonicalAttempt {
			if strings.HasPrefix(reference, "art_") {
				unresolved[reference] = struct{}{}
				continue
			}
			if _, allowed := currentAttemptVersions[reference]; !allowed {
				unresolved[reference] = struct{}{}
				continue
			}
		}
		var artifactProjectID string
		var resolvedArtifactID string
		var found bool
		var err error
		if strings.HasPrefix(reference, "art_") {
			artifact, _, currentFound, currentErr := s.workspaceStore.GetCurrentArtifactVersionMetadata(strings.TrimPrefix(reference, "art_"))
			resolvedArtifactID, artifactProjectID, found, err = artifact.ID, artifact.ProjectID, currentFound, currentErr
		} else {
			artifact, _, versionFound, versionErr := s.workspaceStore.GetArtifactVersionMetadata(reference)
			resolvedArtifactID, artifactProjectID, found, err = artifact.ID, artifact.ProjectID, versionFound, versionErr
		}
		if err != nil {
			return nil, err
		}
		if !found || artifactProjectID != projectID {
			unresolved[reference] = struct{}{}
			continue
		}
		if mode, modeFound, modeErr := s.workspaceStore.ArtifactRetentionMode(resolvedArtifactID); modeErr != nil {
			return nil, modeErr
		} else if modeFound && mode == "working_data" {
			// Internal audit/construction artifacts remain durable and reviewer-readable,
			// but they are not valid user-facing final-answer references.
			unresolved[reference] = struct{}{}
		}
	}
	result := make([]string, 0, len(unresolved))
	for reference := range unresolved {
		result = append(result, reference)
	}
	sort.Strings(result)
	return result, nil
}

func (s *Server) sessionRunnerArtifactCommitReferences(
	ctx context.Context,
	run *sessionRunnerChatRun,
) ([]transcriptstore.ArtifactReferenceInput, error) {
	if run == nil || run.Transcript == nil {
		return nil, nil
	}
	if s == nil || s.transcriptStore == nil {
		return nil, errors.New("runner transcript artifact ledger is unavailable")
	}
	authority := run.Transcript
	snapshot, err := s.transcriptStore.CurrentArtifactCommitSnapshot(
		ctx, authority.Stream.UID, authority.Stream.OwnerID, authority.Claim.Attempt,
	)
	if err != nil {
		return nil, err
	}
	result := make([]transcriptstore.ArtifactReferenceInput, 0, len(run.ContinuationArtifactReferences)+len(snapshot.References))
	for _, ref := range run.ContinuationArtifactReferences {
		artifactID := strings.TrimSpace(ref.ArtifactID)
		versionID := strings.TrimSpace(ref.VersionID)
		if artifactID == "" || versionID == "" {
			continue
		}
		internal, err := s.sessionRunnerInternalArtifact(artifactID)
		if err != nil {
			return nil, err
		}
		if internal {
			continue
		}
		result = append(result, transcriptstore.ArtifactReferenceInput{
			ArtifactID: artifactID, VersionID: versionID, Relation: transcriptstore.ArtifactRelationProduced,
		})
	}
	for _, ref := range snapshot.References {
		artifactID := strings.TrimSpace(ref.ArtifactID)
		internal, err := s.sessionRunnerInternalArtifact(artifactID)
		if err != nil {
			return nil, err
		}
		if internal {
			continue
		}
		result = append(result, transcriptstore.ArtifactReferenceInput{
			ArtifactID: ref.ArtifactID, VersionID: ref.VersionID, Relation: ref.Relation,
		})
	}
	return latestSessionRunnerArtifactReferences(result), nil
}

// sessionRunnerActiveArtifactCommitReferences projects the current delivery
// set out of the append-only continuation ledger. History remains durable for
// audit and resume, but an artifact superseded by a later correction must not
// remain a competing final deliverable forever. The current attempt's saved
// artifacts are authoritative; an explicit final reference may also retain an
// unchanged artifact from an earlier attempt. A new independent input revision
// with neither signal has no artifact deliverable to validate: projecting the
// full historical ledger there would make an unrelated prior report a mutable
// obligation of the new turn. Explicit continuation tasks retain the full
// logical-task ledger so correction and follow-up work still fail closed.
func (s *Server) sessionRunnerActiveArtifactCommitReferences(
	ctx context.Context,
	run *sessionRunnerChatRun,
	commits []transcriptstore.ArtifactReferenceInput,
	content string,
) ([]transcriptstore.ArtifactReferenceInput, error) {
	if run == nil || run.Transcript == nil || s == nil || s.transcriptStore == nil {
		return commits, nil
	}
	currentAttemptRefs, err := s.transcriptStore.ListArtifactCommitReferences(
		ctx, run.Transcript.Stream.UID, run.Transcript.Stream.OwnerID, run.Transcript.Claim.Attempt,
	)
	if err != nil {
		return nil, err
	}
	currentVersions := make(map[string]struct{}, len(currentAttemptRefs))
	hasCurrentProduced := false
	for _, ref := range currentAttemptRefs {
		versionID := strings.TrimSpace(ref.VersionID)
		if versionID == "" {
			continue
		}
		currentVersions[versionID] = struct{}{}
		if ref.Relation == transcriptstore.ArtifactRelationProduced {
			hasCurrentProduced = true
		}
	}
	explicitVersions := make(map[string]struct{})
	for _, match := range artifactReferencePattern.FindAllStringSubmatch(content, -1) {
		if len(match) == 2 && strings.TrimSpace(match[1]) != "" {
			explicitVersions[strings.TrimSpace(match[1])] = struct{}{}
		}
	}
	continuesPriorWork := len(run.ContinuationArtifactReferences) > 0
	if continuesPriorWork {
		for _, commit := range commits {
			if commit.Relation != transcriptstore.ArtifactRelationProduced || strings.TrimSpace(commit.VersionID) == "" {
				continue
			}
			artifact, _, found, metadataErr := s.workspaceStore.GetArtifactVersionMetadata(commit.VersionID)
			if metadataErr != nil {
				return nil, metadataErr
			}
			if found && sessionRunnerArtifactNameSatisfiesRequiredDeliverable(run.TaskIntent, artifact.Name) {
				explicitVersions[strings.TrimSpace(commit.VersionID)] = struct{}{}
			}
		}
	}
	selected := selectSessionRunnerActiveArtifactReferences(
		commits, currentVersions, explicitVersions, hasCurrentProduced,
		continuesPriorWork,
	)
	return s.deduplicateSessionRunnerProducedArtifactReferences(selected, explicitVersions)
}

func selectSessionRunnerActiveArtifactReferences(
	commits []transcriptstore.ArtifactReferenceInput,
	currentVersions, explicitVersions map[string]struct{},
	hasCurrentProduced, continuesPriorWork bool,
) []transcriptstore.ArtifactReferenceInput {
	if !hasCurrentProduced && len(explicitVersions) == 0 {
		if continuesPriorWork {
			return latestSessionRunnerArtifactReferences(commits)
		}
		return nil
	}
	result := make([]transcriptstore.ArtifactReferenceInput, 0, len(commits))
	for _, commit := range commits {
		versionID := strings.TrimSpace(commit.VersionID)
		_, current := currentVersions[versionID]
		_, explicit := explicitVersions[versionID]
		if current || explicit {
			result = append(result, commit)
		}
	}
	return latestSessionRunnerArtifactReferences(result)
}

// latestSessionRunnerArtifactReferences mirrors the transcript repository's
// current-head semantics across execution-unit continuation receipts. A later
// save of the same artifact replaces the older version instead of making both
// versions valid final-answer references.
func latestSessionRunnerArtifactReferences(refs []transcriptstore.ArtifactReferenceInput) []transcriptstore.ArtifactReferenceInput {
	result := make([]transcriptstore.ArtifactReferenceInput, 0, len(refs))
	positions := make(map[string]int, len(refs))
	for _, ref := range refs {
		artifactID := strings.TrimSpace(ref.ArtifactID)
		versionID := strings.TrimSpace(ref.VersionID)
		if artifactID == "" || versionID == "" {
			continue
		}
		key := artifactID + "\x00" + string(ref.Relation)
		normalized := transcriptstore.ArtifactReferenceInput{
			ArtifactID: artifactID, VersionID: versionID, Relation: ref.Relation,
		}
		if index, found := positions[key]; found {
			result[index] = normalized
			continue
		}
		positions[key] = len(result)
		result = append(result, normalized)
	}
	return result
}

// deduplicateSessionRunnerProducedArtifactReferences keeps one authoritative
// user-facing reference when byte-identical generated files were saved from
// different workspace paths. Path-scoped artifact identities and every commit
// remain durable for audit; this only normalizes the active delivery set.
// Versions explicitly referenced by the terminal answer take precedence.
func (s *Server) deduplicateSessionRunnerProducedArtifactReferences(
	refs []transcriptstore.ArtifactReferenceInput,
	preferredVersions map[string]struct{},
) ([]transcriptstore.ArtifactReferenceInput, error) {
	if s == nil || s.workspaceStore == nil || len(refs) < 2 {
		return refs, nil
	}
	result := make([]transcriptstore.ArtifactReferenceInput, 0, len(refs))
	positions := map[string]int{}
	for _, ref := range refs {
		if ref.Relation != transcriptstore.ArtifactRelationProduced {
			result = append(result, ref)
			continue
		}
		artifact, version, found, err := s.workspaceStore.GetArtifactVersionMetadata(strings.TrimSpace(ref.VersionID))
		if err != nil {
			return nil, err
		}
		name := strings.TrimSpace(artifact.Name)
		kind := strings.ToLower(strings.TrimSpace(artifact.Kind))
		checksum := strings.ToLower(strings.TrimSpace(version.ContentSHA256))
		if !found || name == "" || kind == "" || checksum == "" {
			result = append(result, ref)
			continue
		}
		identity := name + "\x00" + kind + "\x00" + checksum
		if position, duplicate := positions[identity]; duplicate {
			_, candidatePreferred := preferredVersions[strings.TrimSpace(ref.VersionID)]
			_, currentPreferred := preferredVersions[strings.TrimSpace(result[position].VersionID)]
			if candidatePreferred && !currentPreferred {
				result[position] = ref
			}
			continue
		}
		positions[identity] = len(result)
		result = append(result, ref)
	}
	return result, nil
}

func (s *Server) sessionRunnerInternalArtifact(artifactID string) (bool, error) {
	if s == nil || s.workspaceStore == nil {
		return false, errors.New("runner workspace artifact authority is unavailable")
	}
	mode, found, err := s.workspaceStore.ArtifactRetentionMode(strings.TrimSpace(artifactID))
	if err != nil {
		return false, err
	}
	return found && mode == "working_data", nil
}

func (s *Server) sessionRunnerResearchArtifactManifestLedger(
	projectID string,
	commits []transcriptstore.ArtifactReferenceInput,
) (string, error) {
	const maxLedgerEntries = 256
	if s == nil || s.workspaceStore == nil || strings.TrimSpace(projectID) == "" {
		return "", errors.New("runner research artifact authority is unavailable")
	}
	type ledgerEntry struct {
		ArtifactID string `json:"artifact_id"`
		VersionID  string `json:"version_id"`
		Filename   string `json:"filename"`
		SHA256     string `json:"sha256"`
	}
	entries := make([]ledgerEntry, 0, len(commits))
	seenVersions := map[string]struct{}{}
	for _, commit := range commits {
		if commit.Relation != transcriptstore.ArtifactRelationProduced {
			continue
		}
		artifactID := strings.TrimSpace(commit.ArtifactID)
		versionID := strings.TrimSpace(commit.VersionID)
		if artifactID == "" || versionID == "" {
			continue
		}
		if _, duplicate := seenVersions[versionID]; duplicate {
			continue
		}
		artifact, version, found, err := s.workspaceStore.GetArtifactVersionMetadata(versionID)
		if err != nil {
			return "", err
		}
		if !found {
			_, artifactFound, lookupErr := s.workspaceStore.GetArtifact(artifactID)
			if lookupErr != nil {
				return "", lookupErr
			}
			if artifactFound {
				return "", errors.New("runner research artifact version is unavailable")
			}
			continue
		}
		if artifact.ProjectID != projectID || strings.TrimSpace(artifact.ID) != artifactID {
			return "", errors.New("runner research artifact version is unavailable")
		}
		filename := filepath.ToSlash(strings.TrimSpace(artifact.Name))
		if filename == "provenance_manifest.json" {
			continue
		}
		seenVersions[versionID] = struct{}{}
		entries = append(entries, ledgerEntry{
			ArtifactID: artifactID, VersionID: versionID, Filename: filename,
			SHA256: strings.ToLower(strings.TrimSpace(version.ContentSHA256)),
		})
	}
	sort.Slice(entries, func(left, right int) bool {
		if entries[left].Filename != entries[right].Filename {
			return entries[left].Filename < entries[right].Filename
		}
		if entries[left].ArtifactID != entries[right].ArtifactID {
			return entries[left].ArtifactID < entries[right].ArtifactID
		}
		return entries[left].VersionID < entries[right].VersionID
	})
	truncated := len(entries) > maxLedgerEntries
	if truncated {
		entries = entries[:maxLedgerEntries]
	}
	payload, err := json.Marshal(map[string]any{
		"schema": "synon.current_artifact_ledger.v1", "entries": entries, "truncated": truncated,
	})
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func (s *Server) sessionRunnerCompletionArtifactCandidateReferences(
	session sessionstore.Session,
	run *sessionRunnerChatRun,
	commits []transcriptstore.ArtifactReferenceInput,
	content string,
) (*sessionRunnerArtifactCandidateReferences, error) {
	return s.sessionRunnerCompletionArtifactCandidateReferencesWithSelection(
		session, run, commits, content, nil, false,
	)
}

func (s *Server) sessionRunnerCompletionArtifactCandidateReferencesWithSelection(
	session sessionstore.Session,
	run *sessionRunnerChatRun,
	commits []transcriptstore.ArtifactReferenceInput,
	content string,
	selectedResearchVersions map[string]struct{},
	researchManifestFound bool,
) (*sessionRunnerArtifactCandidateReferences, error) {
	result := &sessionRunnerArtifactCandidateReferences{
		positive: map[string]struct{}{},
		negative: map[string]struct{}{},
		origins:  map[string][]string{},
	}
	if run == nil || run.Transcript == nil {
		return result, nil
	}
	projectID := sessionRunnerProjectID(session)
	if s == nil || s.workspaceStore == nil || projectID == "" {
		// Standalone sessions (for example IM chats) have no project artifact
		// authority. There are no committed artifacts to compare against, and
		// unresolvedSessionRunnerArtifactReferenceCount still fails closed when
		// the final content actually names a candidate reference.
		return result, nil
	}
	allowedVersions := map[string]struct{}{}
	latestProducedVersion := map[string]string{}
	latestProducedName := map[string]string{}
	latestReservedVersion := map[string]string{}
	for _, commit := range commits {
		versionID := strings.TrimSpace(commit.VersionID)
		if versionID == "" {
			continue
		}
		allowedVersions[versionID] = struct{}{}
		if commit.Relation == transcriptstore.ArtifactRelationProduced {
			artifactID := strings.TrimSpace(commit.ArtifactID)
			if artifactID != "" {
				artifact, _, found, err := s.workspaceStore.GetArtifactVersionMetadata(versionID)
				if err != nil {
					return nil, err
				}
				if !found {
					_, artifactFound, lookupErr := s.workspaceStore.GetArtifact(artifactID)
					if lookupErr != nil {
						return nil, lookupErr
					}
					if artifactFound {
						return nil, errors.New("runner produced artifact version identity is unavailable")
					}
					continue
				}
				if artifact.ProjectID != projectID || strings.TrimSpace(artifact.ID) != artifactID {
					return nil, errors.New("runner produced artifact version identity is unavailable")
				}
				name := strings.TrimSpace(artifact.Name)
				// ListArtifactCommitReferences is ordered by durable source event
				// and ordinal, so overwriting selects this attempt's final version.
				latestProducedVersion[artifactID] = versionID
				latestProducedName[artifactID] = name
				if sessionRunnerReservedCompletionArtifactName(name) {
					// A reserved logical contract has one current produced authority
					// even when a repair created a new artifact identity.
					latestReservedVersion[name] = versionID
				}
			}
		}
	}
	selectedVersions := map[string]struct{}{}
	if researchManifestFound {
		for versionID := range selectedResearchVersions {
			if _, allowed := allowedVersions[versionID]; allowed {
				selectedVersions[versionID] = struct{}{}
			}
		}
	} else {
		for artifactID, versionID := range latestProducedVersion {
			// Large tool results are an internal evidence transport. They are
			// already evaluated through the durable tool-evidence messages above
			// and are not an implicit user deliverable. Only an explicit final
			// artifact reference may promote one into completion candidates.
			if isRunnerLargeToolResultArtifactID(artifactID) {
				continue
			}
			name := latestProducedName[artifactID]
			if sessionRunnerReservedCompletionArtifactName(name) && latestReservedVersion[name] != versionID {
				continue
			}
			selectedVersions[versionID] = struct{}{}
		}
	}
	for _, versionID := range latestReservedVersion {
		selectedVersions[versionID] = struct{}{}
	}
	for _, match := range artifactReferencePattern.FindAllStringSubmatch(content, -1) {
		if len(match) != 2 {
			continue
		}
		versionID := strings.TrimSpace(match[1])
		if _, allowed := allowedVersions[versionID]; allowed {
			selectedVersions[versionID] = struct{}{}
		}
	}
	versionIDs := make([]string, 0, len(selectedVersions))
	for versionID := range selectedVersions {
		versionIDs = append(versionIDs, versionID)
	}
	sort.Strings(versionIDs)
	for _, versionID := range versionIDs {
		artifactText, artifactName, textArtifact, err := s.sessionRunnerArtifactVersionText(projectID, versionID)
		if err != nil {
			return nil, err
		}
		if textArtifact {
			origin := "artifact:" + strings.TrimSpace(artifactName) + "@" + versionID
			rejected, recognized, failure := decodeSessionRunnerRejectedReferenceV1(artifactText, origin)
			if !recognized && strings.TrimSpace(artifactName) == sessionRunnerRejectedReferenceArtifactNameV1 {
				result.contractFailures = append(result.contractFailures,
					truncateSessionRunnerReferenceDiagnostic(origin+"#missing_schema", 512))
				continue
			}
			if recognized {
				if failure != "" {
					result.contractFailures = append(result.contractFailures, failure)
				} else {
					result.rejected = append(result.rejected, rejected...)
					for _, candidate := range rejected {
						appendSessionRunnerReferenceOrigin(result.origins, candidate.reference, candidate.origin)
					}
				}
				continue
			}
			// Embedded binary payloads are transport bytes, not narrative claims.
			// In particular, random base64 sequences can resemble scientific
			// accessions such as dbSNP identifiers. Keep the surrounding HTML and
			// every visible identifier subject to grounding while excluding only
			// validated data-URI payload bytes from citation extraction.
			candidateText, _ := redactSessionReviewerDataURIs(artifactText)
			addSessionRunnerArtifactCandidateReferencesWithOrigin(result, candidateText, origin)
		}
	}
	return result, nil
}

func sessionRunnerReservedCompletionArtifactName(name string) bool {
	return strings.TrimSpace(name) == sessionRunnerRejectedReferenceArtifactNameV1
}

func (s *Server) sessionRunnerArtifactVersionText(projectID, versionID string) (string, string, bool, error) {
	artifact, version, reader, found, err := s.workspaceStore.OpenArtifactVersionContent(versionID)
	if err != nil {
		return "", "", false, err
	}
	if !found {
		if reader != nil {
			_ = reader.Close()
		}
		return "", "", false, nil
	}
	if artifact.ProjectID != projectID {
		_ = reader.Close()
		return "", "", false, errors.New("runner artifact version is unavailable")
	}
	lineage, lineageFound, err := s.workspaceStore.GetArtifactVersionLineageRecord(versionID, false)
	if err != nil {
		_ = reader.Close()
		return "", artifact.Name, false, err
	}
	if lineageFound && !lineage.IsUserUpload && lineage.Language != nil &&
		strings.EqualFold(strings.TrimSpace(*lineage.Language), agentPublicScientificArtifactLanguage) {
		// A validated public scientific download is an immutable source payload,
		// not model-authored narrative. URLs, DOIs, and accessions embedded in its
		// upstream metadata must remain byte-faithful and are not claims made by
		// the final answer. Source authority is enforced separately by the
		// download receipt and scientific-evidence gate; the user-visible answer
		// and report remain subject to normal citation grounding.
		_ = reader.Close()
		return "", artifact.Name, false, nil
	}
	if agentSavedArtifactEvidencePolicyFor(artifact.Kind, artifact.Name) != agentSavedArtifactEvidenceCitations {
		// Completion must use the same evidence classification as save_artifacts.
		// Raw execution files and machine scientific formats such as mmCIF, PDB,
		// PDBQT, and SDF are byte-faithful data, not model-authored prose. Their
		// embedded identifiers remain subject to lineage and scientific-format
		// validation, but must never be reinterpreted as narrative citations.
		_ = reader.Close()
		return "", artifact.Name, false, nil
	}
	if !sessionRunnerTextArtifact(artifact.Kind, artifact.Name) {
		_ = reader.Close()
		return "", artifact.Name, false, nil
	}
	if version.SizeBytes > v11ArtifactBinaryLimit {
		_ = reader.Close()
		return "", artifact.Name, false, errors.New("runner text artifact exceeds the verified completion limit")
	}
	data, readErr := io.ReadAll(io.LimitReader(reader, v11ArtifactBinaryLimit+1))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		return "", artifact.Name, false, errors.Join(readErr, closeErr)
	}
	if len(data) > v11ArtifactBinaryLimit {
		return "", artifact.Name, false, errors.New("runner text artifact exceeds the verified completion limit")
	}
	return string(data), artifact.Name, true, nil
}

func (s *Server) sessionRunnerReferencedArtifactTexts(
	session sessionstore.Session,
	run *sessionRunnerChatRun,
	commits []transcriptstore.ArtifactReferenceInput,
	content string,
) ([]string, error) {
	if run == nil || run.Transcript == nil {
		return nil, nil
	}
	projectID := sessionRunnerProjectID(session)
	allowed := map[string]struct{}{}
	for _, commit := range commits {
		allowed[commit.VersionID] = struct{}{}
	}
	texts := []string{}
	seen := map[string]struct{}{}
	for _, match := range artifactReferencePattern.FindAllStringSubmatch(content, -1) {
		if len(match) != 2 {
			continue
		}
		versionID := strings.TrimSpace(match[1])
		if _, ok := allowed[versionID]; !ok {
			continue
		}
		if _, ok := seen[versionID]; ok {
			continue
		}
		seen[versionID] = struct{}{}
		artifactText, _, textArtifact, err := s.sessionRunnerArtifactVersionText(projectID, versionID)
		if err != nil {
			return nil, err
		}
		if textArtifact {
			texts = append(texts, artifactText)
		}
	}
	return texts, nil
}

func sessionRunnerTextArtifact(kind, name string) bool {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if strings.HasPrefix(kind, "text/") || kind == "markdown" || kind == "csv" || kind == "json" ||
		kind == "application/json" || kind == "application/xml" {
		return true
	}
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(name))) {
	case ".md", ".txt", ".csv", ".tsv", ".json", ".jsonl", ".yaml", ".yml", ".xml", ".html", ".htm", ".bib", ".ris", ".tex", ".py", ".r", ".ipynb":
		return true
	default:
		return false
	}
}
