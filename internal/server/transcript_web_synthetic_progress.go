package server

import (
	"encoding/json"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

// transcriptWebSyntheticProgressNormalizer removes transient progress events
// that arrive after their assistant candidate has already been superseded or
// replaced by provider-authored content. The durable source event remains in
// the transcript chain for audit; only the Web projection omits the stale UI
// placeholder.
type transcriptWebSyntheticProgressNormalizer struct {
	retiredOrdinals map[int64]map[int64]bool
}

func newTranscriptWebSyntheticProgressNormalizer() *transcriptWebSyntheticProgressNormalizer {
	return &transcriptWebSyntheticProgressNormalizer{retiredOrdinals: map[int64]map[int64]bool{}}
}

func (n *transcriptWebSyntheticProgressNormalizer) normalize(
	projected transcriptstore.ProjectedEvent,
) (transcriptstore.ProjectedEvent, bool, error) {
	if n == nil || projected.Event.RunnerAttempt == nil {
		return projected, true, nil
	}
	if projected.Event.Type != "content_reset" && projected.Event.Type != "content_delta" &&
		projected.Event.Type != "assistant_message" && projected.Event.Type != "history_assistant_message" &&
		projected.Event.Type != "runner_finished" {
		return projected, true, nil
	}
	payload, err := transcriptPayloadObject(projected.ResolvedPayloadJSON)
	if err != nil {
		return projected, false, err
	}
	segment, present, err := transcriptstore.ParseAssistantSegmentV1(payload)
	if err != nil {
		return projected, false, err
	}
	retired := transcriptWebRetiredPublicProgress(payload)
	if projected.Event.Type == "content_delta" && !retired {
		// Legacy reasoning-activity checkpoints are audit-only. The provider's
		// public stage prose and tool records are the sole visible timeline
		// authority; projecting this generic placeholder creates a second prose
		// lane and leaves repeated filler in durable history.
		retired = boolValue(payload["synthetic_progress"], false)
	}
	attempt := *projected.Event.RunnerAttempt
	if projected.Event.Type == "content_reset" && present && segment.Ordinal > 1 &&
		n.retiredOrdinals[attempt][segment.Ordinal-1] {
		// Legacy runners retracted the synthetic placeholder with a narrow reset
		// before emitting provider-authored prose at the reset's ordinal. Once the
		// placeholder is audit-only, retaining that reset would either target an
		// absent first segment or retire the preceding real provider segment after
		// ordinal compression. The source pair is immutable; both halves are omitted
		// only from the Web projection.
		return projected, false, nil
	}
	if retired {
		if present {
			ordinals := n.retiredOrdinals[attempt]
			if ordinals == nil {
				ordinals = map[int64]bool{}
				n.retiredOrdinals[attempt] = ordinals
			}
			ordinals[segment.Ordinal] = true
		}
		return projected, false, nil
	}
	if !present || len(n.retiredOrdinals[attempt]) == 0 {
		return projected, true, nil
	}
	retiredBefore := int64(0)
	for ordinal := range n.retiredOrdinals[attempt] {
		if ordinal < segment.Ordinal || (projected.Event.Type == "runner_finished" && ordinal == segment.Ordinal) {
			retiredBefore++
		}
	}
	effective := segment.Ordinal - retiredBefore
	if effective <= 0 {
		if projected.Event.Type != "runner_finished" {
			return projected, false, transcriptstore.ErrEventConflict
		}
		delete(payload, "assistant_segment")
	} else {
		payload["assistant_segment"] = transcriptstore.AssistantSegmentPayloadV1(effective, segment.ReplaceScope)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return projected, false, transcriptstore.ErrEventConflict
	}
	projected.ResolvedPayloadJSON = raw
	return projected, true, nil
}

// public_progress was a short-lived v0.1.0 experiment that persisted a tool
// description as assistant prose before every invocation. It fragmented the
// real text/tool timeline and duplicated the tool card. Keep the source event
// for audit, but retire it from every Web projection path.
func transcriptWebRetiredPublicProgress(payload map[string]any) bool {
	return boolValue(payload["public_progress"], false)
}

func transcriptWebIncrementalSyntheticProgressIsStale(
	synthetic bool,
	durableIdentity, currentIdentity string,
	currentIsSynthetic bool,
	seenIdentities map[string]bool,
) bool {
	if !synthetic {
		return false
	}
	durableIdentity = strings.TrimSpace(durableIdentity)
	currentIdentity = strings.TrimSpace(currentIdentity)
	if currentIdentity != "" && !currentIsSynthetic {
		return true
	}
	return durableIdentity != "" && durableIdentity != currentIdentity && seenIdentities[durableIdentity]
}
