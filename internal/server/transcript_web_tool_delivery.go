package server

import transcriptstore "synon-go/internal/persistence/transcript"

// transcriptWebToolStreamPayload projects one durable tool checkpoint onto the
// existing message.stream lane. History and live delivery deliberately reuse
// the same fact parser and message serializer, so reconnect cannot reinterpret
// the operation's status, identity, parent, or public summary.
func transcriptWebToolStreamPayload(
	claim transcriptstore.DeliveryClaim,
	payload map[string]any,
	sessionID, publicationBoundaryID string,
	artifactRefs []map[string]any,
) (map[string]any, bool, error) {
	projected := transcriptstore.ProjectedEvent{
		Event:               claim.Event,
		ResolvedPayloadJSON: claim.ResolvedPayloadJSON,
		ArtifactReferences:  claim.ArtifactReferences,
	}
	fact, found, err := transcriptToolHistoryFactFromEvent(projected, payload)
	if err != nil || !found || fact.provisional || fact.backgroundAdmission {
		return nil, false, err
	}
	action := transcriptToolHistoryAction{
		fact:     fact,
		identity: transcriptToolHistoryIdentity(claim.StreamUID, fact.attempt, fact.callID),
		msgID:    claim.Event.ClientMessageID,
	}
	message := compactWebConversationMessage(
		transcriptToolHistoryMessage(action, sessionID, claim.Event.CreatedAt.UnixMilli()),
	)
	content, ok := message["content"].(map[string]any)
	if !ok {
		return nil, false, transcriptstore.ErrEventConflict
	}
	return map[string]any{
		"type": "tool_call", "data": content,
		"msg_id": action.identity, "turn_id": sessionID, "conversation_id": sessionID,
		"created_at": claim.Event.CreatedAt.UnixMilli(), "position": "left", "status": message["status"],
		"artifact_refs":               artifactRefs,
		"source_publication_sequence": claim.PublicationSeq,
		"publication_boundary_id":     publicationBoundaryID,
	}, true, nil
}
