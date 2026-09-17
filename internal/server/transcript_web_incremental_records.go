package server

import (
	"encoding/json"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func cloneTranscriptWebIncrementalRecord(
	record *transcriptstore.TranscriptWebMessageRecord,
) *transcriptstore.TranscriptWebMessageRecord {
	if record == nil {
		return nil
	}
	copy := *record
	copy.MessageJSON = append([]byte(nil), record.MessageJSON...)
	copy.ArtifactReferences = append([]transcriptstore.TranscriptWebMessageArtifactReference(nil), record.ArtifactReferences...)
	if record.VisibleIndex != nil {
		value := *record.VisibleIndex
		copy.VisibleIndex = &value
	}
	return &copy
}

func cloneTranscriptWebIncrementalAttempt(
	value transcriptWebIncrementalAssistantAttemptCheckpoint,
) transcriptWebIncrementalAssistantAttemptCheckpoint {
	copy := value
	copy.SegmentIdentities = append([]string(nil), value.SegmentIdentities...)
	copy.CurrentMessage = cloneTranscriptWebIncrementalRecord(value.CurrentMessage)
	copy.Rollback = nil
	return copy
}

func transcriptWebIncrementalReferenceMaps(
	references []transcriptstore.ArtifactReference,
) []map[string]any {
	return transcriptArtifactReferences(references)
}

func transcriptWebStoredReferenceMaps(
	references []transcriptstore.TranscriptWebMessageArtifactReference,
) []map[string]any {
	result := make([]map[string]any, 0, len(references))
	for _, reference := range references {
		value := map[string]any{
			"artifact_id": reference.ArtifactID, "version_id": reference.VersionID,
			"relation": string(reference.Relation),
		}
		if reference.Availability != "" {
			value["availability"] = string(reference.Availability)
		}
		if reference.RunnerAttempt != nil {
			value["attempt"] = *reference.RunnerAttempt
			value["source_event_id"] = reference.SourceEventID
			value["ordinal"] = reference.SourceReferenceOrdinal
		} else {
			value["filename"] = reference.Filename
			value["content_type"] = reference.ContentType
			value["size_bytes"] = reference.SizeBytes
			value["checksum"] = reference.Checksum
		}
		result = append(result, value)
	}
	return result
}

func transcriptWebIncrementalMessageMap(
	record transcriptstore.TranscriptWebMessageRecord,
) (map[string]any, error) {
	var message map[string]any
	if len(record.MessageJSON) == 0 || json.Unmarshal(record.MessageJSON, &message) != nil {
		return nil, transcriptstore.ErrEventConflict
	}
	return message, nil
}
