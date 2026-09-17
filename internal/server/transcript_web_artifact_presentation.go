package server

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

var transcriptWebTruncatedArtifactImagePattern = regexp.MustCompile(`!\[[^\]\r\n]+\]\(\s*(?:\r?\n|$)`)

// enrichTranscriptWebArtifactPresentation repairs the bounded historical
// window written before assistant events carried current artifact
// heads. It never parses filenames into authority: the exact active branch,
// owner, and runner attempt select a durable artifact snapshot. The frontend
// then uses the same structured references as newly published messages.
func (s *Server) enrichTranscriptWebArtifactPresentation(
	ctx context.Context,
	frameID string,
	messages []map[string]any,
) error {
	attempts := make(map[int64]struct{})
	for _, message := range messages {
		attempt, ok := transcriptWebArtifactRecoveryAttempt(frameID, message)
		if ok {
			attempts[attempt] = struct{}{}
		}
	}
	if len(attempts) == 0 {
		return nil
	}
	if s == nil || s.workspaceStore == nil || s.transcriptStore == nil {
		return transcriptstore.ErrSchemaUnavailable
	}
	frame, found, err := s.workspaceStore.GetFrame(frameID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	ownerID, found, err := s.workspaceStore.ProjectOwnerIDContext(ctx, frame.ProjectID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	stream, found, err := s.transcriptStore.GetFrameStreamBySession(ctx, ownerID, frameID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	snapshots := make(map[int64][]map[string]any, len(attempts))
	for attempt := range attempts {
		snapshot, snapshotErr := s.transcriptStore.CurrentArtifactCommitSnapshot(
			ctx, stream.UID, ownerID, attempt,
		)
		if snapshotErr != nil {
			return snapshotErr
		}
		available := make([]transcriptstore.ArtifactReference, 0, len(snapshot.References))
		for _, reference := range snapshot.References {
			if reference.Availability == transcriptstore.ArtifactAvailable {
				available = append(available, reference)
			}
		}
		snapshots[attempt] = transcriptArtifactReferences(available)
	}
	for _, message := range messages {
		attempt, ok := transcriptWebArtifactRecoveryAttempt(frameID, message)
		if !ok {
			continue
		}
		if references := snapshots[attempt]; len(references) > 0 {
			message["artifact_refs"] = references
		}
	}
	return nil
}

func transcriptWebArtifactRecoveryAttempt(frameID string, message map[string]any) (int64, bool) {
	if strings.TrimSpace(webString(message["type"])) != "text" ||
		strings.TrimSpace(webString(message["position"])) != "left" ||
		transcriptWebMessageHasArtifactReferences(message["artifact_refs"]) {
		return 0, false
	}
	content, ok := message["content"].(map[string]any)
	if !ok || content == nil {
		return 0, false
	}
	text := webString(content["content"])
	if !strings.Contains(text, sessionRunnerArtifactReferenceMarker) &&
		!transcriptWebTruncatedArtifactImagePattern.MatchString(text) {
		return 0, false
	}
	identity := strings.TrimSpace(webString(content["assistant_attempt_id"]))
	if identity == "" {
		identity = strings.TrimSpace(webString(message["id"]))
	}
	prefix := "assistant-" + strings.TrimSpace(frameID) + "-"
	if !strings.HasPrefix(identity, prefix) {
		return 0, false
	}
	remainder := strings.TrimPrefix(identity, prefix)
	if segment := strings.Index(remainder, "-segment-"); segment >= 0 {
		remainder = remainder[:segment]
	}
	attempt, err := strconv.ParseInt(remainder, 10, 64)
	return attempt, err == nil && attempt > 0
}

func transcriptWebMessageHasArtifactReferences(value any) bool {
	switch references := value.(type) {
	case []map[string]any:
		return len(references) > 0
	case []any:
		return len(references) > 0
	default:
		return false
	}
}
