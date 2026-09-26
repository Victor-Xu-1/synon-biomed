package server

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"synon-go/internal/agentruntime"
)

var (
	errAgentSavedArtifactEvidenceUnavailable = errors.New("saved artifact evidence authority is unavailable")
	errAgentSavedArtifactTextInvalid         = errors.New("saved artifact text encoding is invalid")
	errAgentSavedArtifactRawExecutionLineage = errors.New("raw execution artifact has no exact execution lineage")
)

type agentSavedArtifactEvidencePolicy uint8

const (
	agentSavedArtifactEvidenceNone agentSavedArtifactEvidencePolicy = iota
	agentSavedArtifactEvidenceCitations
	agentSavedArtifactEvidenceRawExecution
)

// agentSavedArtifactEvidencePolicyFor separates model-authored claims from
// byte-faithful execution evidence. Raw logs can legitimately contain tool
// banners, paths, URLs, DOIs, accessions, and numeric tokens that are not
// narrative claims. They therefore require an exact execution write receipt
// instead of citation rewriting. Reports and other readable deliverables keep
// the durable source-evidence gate.
func agentSavedArtifactEvidencePolicyFor(contentType, path string) agentSavedArtifactEvidencePolicy {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(path))) {
	case ".log", ".stdout", ".stderr":
		return agentSavedArtifactEvidenceRawExecution
	case ".cif", ".mmcif", ".pdb", ".pdbqt", ".sdf", ".mol", ".mol2", ".smi":
		// Coordinate and chemical exchange formats are machine-produced data,
		// not model-authored narrative. Their dictionaries, headers, and source
		// records may legitimately embed DOIs, URLs, and accessions. Applying the
		// prose citation gate to those bytes rejects valid scientific outputs and
		// encourages lossy rewriting. Exact file lineage and artifact hashing are
		// the authority for these formats.
		return agentSavedArtifactEvidenceNone
	}
	if sessionRunnerTextArtifact(contentType, path) {
		return agentSavedArtifactEvidenceCitations
	}
	return agentSavedArtifactEvidenceNone
}

type agentSavedArtifactEvidenceError struct {
	References          []string
	Total               int
	AvailableReferences []string
	AvailableTotal      int
}

// sessionRunnerCitationIntegrityRequired keeps citation enforcement under the
// same user-owned verifier-mode authority as the completion reviewer. A
// disabled verifier must not silently turn normal report saving into an
// automatic review loop. Legacy/test contexts without a resolved policy retain
// the stricter behavior so authority gaps still fail closed.
func sessionRunnerCitationIntegrityRequired(run *sessionRunnerChatRun) bool {
	if run == nil {
		return true
	}
	if run.VerificationExplicitlyDisabled {
		return false
	}
	return run.ReviewPolicy == nil || run.ReviewPolicy.EvidenceReviewRequired
}

func (s *Server) agentSavedArtifactCitationIntegrityRequired(ctx context.Context) bool {
	run, ok := transcriptRunnerChatRunFromContext(ctx)
	if !ok {
		return true
	}
	if s.sessionRunnerSourceWorkflowActive(run) {
		// Verifier mode controls whether a separate reviewer runs after the
		// candidate is produced. It must not disable the in-process source
		// lineage contract that keeps unsupported discovery links out of a
		// research deliverable before publication.
		return true
	}
	return sessionRunnerCitationIntegrityRequired(run)
}

func (e *agentSavedArtifactEvidenceError) Error() string {
	if e == nil || len(e.References) == 0 {
		return "saved artifact contains unsupported evidence references"
	}
	return "saved artifact contains unsupported evidence references: " + strings.Join(e.References, ", ")
}

func (s *Server) agentSavedArtifactEvidenceMessages(
	ctx context.Context,
	run transcriptArtifactRun,
) ([]agentruntime.Message, error) {
	if run.Authority == nil {
		return nil, errAgentSavedArtifactEvidenceUnavailable
	}
	// The artifact tool context contains the exact active sessionRunnerChatRun.
	// Reuse it so a continuation keeps its logical-task evidence boundary;
	// reconstructing a run from only the current claim silently drops earlier
	// authoritative source receipts and makes a corrected artifact impossible
	// to publish without re-running already successful research.
	evidenceRun := &sessionRunnerChatRun{Transcript: run.Authority}
	if activeRun, ok := transcriptRunnerChatRunFromContext(ctx); ok && activeRun != nil && activeRun.Transcript != nil &&
		activeRun.Transcript.Stream.UID == run.Authority.Stream.UID &&
		activeRun.Transcript.Stream.OwnerID == run.Authority.Stream.OwnerID {
		evidenceRun = activeRun
	}
	messages, err := s.sessionRunnerDurableEvidenceMessages(ctx, evidenceRun)
	if err != nil {
		return nil, errors.Join(errAgentSavedArtifactEvidenceUnavailable, err)
	}
	materials, materialErr := s.sessionRunnerResearchMaterials(ctx, evidenceRun)
	if materialErr != nil {
		return nil, errors.Join(errAgentSavedArtifactEvidenceUnavailable, materialErr)
	}
	if len(materials.Attempts) > 0 || len(materials.Receipts) > 0 {
		messages = researchQualifiedEvidenceMessages(messages, materials)
	}
	return messages, nil
}

func (s *Server) validateAgentSavedArtifactEvidence(
	snapshot *os.File,
	evidence []agentruntime.Message,
) error {
	return s.validateAgentSavedArtifactEvidenceForPathWithPolicy("", snapshot, evidence, true)
}

func (s *Server) validateAgentSavedArtifactEvidenceForPath(
	path string,
	snapshot *os.File,
	evidence []agentruntime.Message,
) error {
	return s.validateAgentSavedArtifactEvidenceForPathWithPolicy(path, snapshot, evidence, true)
}

func (s *Server) validateAgentSavedArtifactEvidenceForPathWithPolicy(
	path string,
	snapshot *os.File,
	evidence []agentruntime.Message,
	includeCitationReferences bool,
) error {
	return s.validateAgentSavedArtifactEvidenceStream(context.Background(), path, snapshot, evidence, includeCitationReferences)
}

func (s *Server) validateAgentSavedArtifactEvidenceStream(ctx context.Context, path string, snapshot *os.File, evidence []agentruntime.Message, includeCitationReferences bool) (resultErr error) {
	if snapshot == nil {
		return errAgentSavedArtifactEvidenceUnavailable
	}
	if _, err := snapshot.Seek(0, io.SeekStart); err != nil {
		return err
	}
	defer func() { _, err := snapshot.Seek(0, io.SeekStart); resultErr = errors.Join(resultErr, err) }()
	if err := validateRunnerTextEncoding(ctx, snapshot); err != nil {
		return err
	}
	unsupported, err := scanAgentSavedArtifactEvidence(ctx, path, snapshot, evidence, includeCitationReferences, s.sessionRunnerEvidenceTool)
	if err != nil {
		return err
	}
	unsupported = uniqueSortedFolded(unsupported)
	if len(unsupported) == 0 {
		return nil
	}
	sort.Strings(unsupported)
	totalUnsupported := len(unsupported)
	if totalUnsupported > 32 {
		unsupported = append([]string(nil), unsupported[:32]...)
	}
	available := verifiedSessionRunnerStructuredReferencesUsing(evidence, s.sessionRunnerEvidenceTool)
	totalAvailable := len(available)
	if totalAvailable > 32 {
		available = append([]string(nil), available[:32]...)
	}
	return &agentSavedArtifactEvidenceError{
		References: unsupported, Total: totalUnsupported,
		AvailableReferences: available, AvailableTotal: totalAvailable,
	}
}

func agentSavedArtifactContainsStructuredEvidence(ctx context.Context, path string, snapshot *os.File) (found bool, resultErr error) {
	if snapshot == nil {
		return false, errAgentSavedArtifactEvidenceUnavailable
	}
	if _, err := snapshot.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	defer func() { _, err := snapshot.Seek(0, io.SeekStart); resultErr = errors.Join(resultErr, err) }()
	if err := validateRunnerTextEncoding(ctx, snapshot); err != nil {
		return false, err
	}
	if _, err := snapshot.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv", ".tsv":
		err := visitRunnerEvidenceRows(ctx, snapshot, path, func(headers map[string]int) bool {
			found = runnerEvidenceLedgerHeaderShape(headers)
			return false
		}, nil)
		return found, err
	case ".md", ".markdown":
		errFound := errors.New("structured evidence table found")
		err := visitRunnerMarkdownRows(ctx, snapshot, func(_ int, _ int, header, _ []string) error {
			headers := make(map[string]int, len(header))
			for index, value := range header {
				headers[normalizeRunnerTableToken(value)] = index
			}
			if runnerEvidenceLedgerHeaderShape(headers) {
				found = true
				return errFound
			}
			return nil
		})
		if errors.Is(err, errFound) {
			err = nil
		}
		return found, err
	default:
		return false, nil
	}
}
