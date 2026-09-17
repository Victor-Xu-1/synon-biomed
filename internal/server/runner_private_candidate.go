package server

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	transcriptstore "synon-go/internal/persistence/transcript"
)

// Private candidates use the existing durable runner journal, never a public
// content event. The continuation fence binds these exact bytes just as it does
// explicitly published progress, without conflating persistence and delivery.
func (s *Server) checkpointPrivateProviderCandidate(ctx context.Context, run *sessionRunnerChatRun, text string) error {
	if text == "" {
		return nil
	}
	if run == nil || run.Transcript == nil {
		return errors.New("private provider continuation requires Transcript authority")
	}
	if !utf8.ValidString(text) {
		return errors.New("private provider candidate is not valid UTF-8")
	}
	// JSON escaping can expand a source byte sixfold. Chunk well below the
	// immutable event limit, retaining rune boundaries and exact digest order.
	const chunkLimit = transcriptstore.MaxEventPayloadBytes / 12
	for index := 0; text != ""; index++ {
		end := min(len(text), chunkLimit)
		for end < len(text) && !utf8.RuneStart(text[end]) {
			end--
		}
		if err := s.checkpointPrivateProviderCandidateChunk(ctx, run, text[:end], index); err != nil {
			return err
		}
		text = text[end:]
	}
	return nil
}

func (s *Server) checkpointPrivateProviderCandidateChunk(ctx context.Context, run *sessionRunnerChatRun, text string, index int) error {
	segmentIndex := int64(0)
	if run.ProviderContinuation != nil {
		segmentIndex = run.ProviderContinuation.Contract.SegmentIndex
	}
	event, err := s.checkpointTranscriptRunnerEvent(ctx, run.Transcript, transcriptstore.RunnerPhaseExecuting,
		fmt.Sprintf("provider-private-candidate-%d-%d-%d", run.assistantSegmentOrdinal(), segmentIndex, index), map[string]any{
			"provider_private_candidate": map[string]any{"version": 1, "text": text},
			"assistant_segment":          transcriptstore.AssistantSegmentPayloadV1(run.assistantSegmentOrdinal(), ""),
		}, false)
	if err != nil {
		return err
	}
	run.AfterEventID = maxInt64(run.AfterEventID, event.EventID)
	run.recordProviderAcceptedContent(text, event)
	run.ProviderContinuation.PrivateCandidate.WriteString(text)
	return nil
}

func privateProviderCandidateText(payload map[string]any) (string, bool, error) {
	raw, present := payload["provider_private_candidate"]
	if !present {
		return "", false, nil
	}
	value, ok := raw.(map[string]any)
	version, validVersion := providerContinuationInt64Field(value, "version")
	if !ok || len(value) != 2 || !validVersion || version != 1 {
		return "", true, errors.New("invalid private provider candidate contract")
	}
	text, ok := value["text"].(string)
	if !ok || text == "" {
		return "", true, errors.New("empty private provider candidate")
	}
	return text, true, nil
}
