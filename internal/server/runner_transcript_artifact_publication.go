package server

import (
	"context"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

// appendTranscriptAssistantEventWithTurnArtifacts is the sole final-answer
// artifact publication path. It binds artifacts produced by this exact runner
// attempt and prior immutable versions explicitly cited by this exact message;
// unrelated branch heads never become outputs of a later user turn.
func (s *Server) appendTranscriptAssistantEventWithTurnArtifacts(
	ctx context.Context,
	authority *transcriptRunnerAuthority,
	input transcriptstore.AppendEventInput,
) error {
	snapshot, err := s.transcriptStore.CurrentArtifactCommitSnapshot(
		ctx, authority.Stream.UID, authority.Stream.OwnerID, authority.Claim.Attempt,
	)
	if err != nil {
		return err
	}
	references, err := assistantMessageArtifactReferences(snapshot, authority.Claim.Attempt, input.PayloadJSON)
	if err != nil {
		return err
	}
	if len(references) == 0 {
		_, _, _, err = s.transcriptStore.AppendAssistantEventWithCommittedArtifacts(ctx, input)
		return err
	}
	_, _, _, err = s.transcriptStore.AppendAssistantEventWithArtifacts(
		ctx,
		transcriptstore.AppendAssistantEventWithArtifactsInput{
			Claim:           input.Claim,
			ClientMessageID: input.ClientMessageID,
			Source:          input.Source,
			PayloadJSON:     input.PayloadJSON,
			FrameEventID:    input.FrameEventID,
			Destinations:    input.Destinations,
			References:      references,
		},
	)
	if err != nil {
		return err
	}
	// Mark this attempt's commits as bound to the already-created assistant
	// event. Prior-attempt heads remain explicit references while current
	// commits cannot be rebound to the terminal event.
	_, _, _, err = s.transcriptStore.AppendAssistantEventWithCommittedArtifacts(ctx, input)
	return err
}

func assistantMessageArtifactReferences(
	snapshot transcriptstore.ArtifactCommitSnapshot,
	attempt int64,
	payloadJSON []byte,
) ([]transcriptstore.ArtifactReferenceInput, error) {
	if attempt <= 0 || snapshot.ThroughAttempt != attempt {
		return nil, transcriptstore.ErrEventConflict
	}
	payload, err := transcriptPayloadObject(payloadJSON)
	if err != nil {
		return nil, err
	}
	content := transcriptPayloadText(payload)
	references := make([]transcriptstore.ArtifactReferenceInput, 0, len(snapshot.References))
	for _, reference := range snapshot.References {
		if reference.Availability != transcriptstore.ArtifactAvailable || reference.RunnerAttempt > attempt {
			continue
		}
		currentAttempt := reference.RunnerAttempt == attempt
		explicitPriorReference := !currentAttempt && strings.Contains(
			content,
			"{{artifact:"+reference.VersionID+"}}",
		)
		if !currentAttempt && !explicitPriorReference {
			continue
		}
		relation := reference.Relation
		if explicitPriorReference {
			relation = transcriptstore.ArtifactRelationCited
		}
		references = append(references, transcriptstore.ArtifactReferenceInput{
			ArtifactID: reference.ArtifactID,
			VersionID:  reference.VersionID,
			Relation:   relation,
		})
	}
	return references, nil
}
