package workspace

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

// CreateCompatibilityAsideWithTranscript creates the child Frame, immutable
// history references, and first runnable input in one SQLite transaction.
func (s *Store) CreateCompatibilityAsideWithTranscript(
	ctx context.Context,
	input CompatibilityAsideInput,
	messageID string,
	destinations []string,
) (CompatibilityAsideResult, error) {
	if s == nil || s.db == nil {
		return CompatibilityAsideResult{}, errors.New("workspace store is closed")
	}
	input, err := normalizeCompatibilityAsideInput(input)
	if err != nil {
		return CompatibilityAsideResult{}, err
	}
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return CompatibilityAsideResult{}, errors.New("aside message id is required")
	}

	s.branchMu.Lock()
	defer s.branchMu.Unlock()
	repository := transcriptstore.NewRepository(s.db)
	var result CompatibilityAsideResult
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		ownerID, err := frameOwnerInTransaction(ctx, tx, input.ParentRootFrameID)
		if err != nil {
			return err
		}
		var parentStreamUID string
		if err := tx.QueryRowContext(ctx, `
			SELECT authority.active_stream_uid FROM transcript_frame_authority authority
			JOIN transcript_streams stream
				ON stream.stream_uid=authority.active_stream_uid AND stream.epoch=authority.active_epoch
			WHERE authority.owner_id=? AND authority.session_id=?
				AND authority.read_authority='transcript_payload_v1'
				AND authority.write_authority='transcript_payload_v1'
				AND stream.owner_id=authority.owner_id AND stream.frame_id=? AND stream.kind='frame_ref'`,
			ownerID, input.ParentRootFrameID, input.ParentRootFrameID).Scan(&parentStreamUID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrCompatibilityAsideParentTranscriptMissing
			}
			return err
		}
		parentConfig, found, err := tx.LatestFrameRuntimeConfig(ctx, parentStreamUID, ownerID)
		if err != nil {
			return err
		}
		if !found {
			parentConfig = map[string]any{}
		}
		stripThinking := input.Model != nil && !compatibilityAsideEqualScalar(*input.Model, parentConfig["model"])
		input.canonicalSeedCount, err = tx.CountAsideHistory(ctx, parentStreamUID, ownerID, stripThinking)
		if err != nil {
			return err
		}
		input.canonicalSeed = true
		input.RuntimeConfig = parentConfig
		result, err = s.createCompatibilityAsideInTransaction(ctx, tx, input)
		if err != nil {
			return err
		}
		stream, err := tx.CreateStream(ctx, transcriptstore.CreateStreamInput{
			UID: "frame:" + result.Frame.ID, OwnerID: ownerID, ExternalID: result.Frame.ID,
			SessionID: result.Frame.ID, Kind: transcriptstore.StreamKindFrameRef,
			ProjectID: result.Frame.ProjectID, RootFrameID: result.Frame.RootFrameID,
			FrameID: result.Frame.ID, Epoch: 1,
		})
		if err != nil {
			return err
		}
		if _, err := tx.CloneAsideHistory(ctx, transcriptstore.CloneAsideHistoryInput{
			SourceStreamUID: parentStreamUID, TargetStreamUID: stream.UID,
			OwnerID: ownerID, IncludeForkNotice: input.AsSession, StripThinking: stripThinking,
		}); err != nil {
			return err
		}
		_, _, created, err := tx.AppendFrameUserEvent(ctx, transcriptstore.AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: ownerID, ClientMessageID: messageID,
			FrameEventID: "frame-message:" + result.Frame.ID + ":" + messageID,
			MessageUUID:  messageID, Text: input.Request,
			RuntimeConfig: compatibilityAsideTranscriptRuntimeConfig(result), Destinations: destinations,
		})
		if err != nil {
			return err
		}
		if !created {
			return transcriptstore.ErrEventConflict
		}
		return nil
	})
	if err != nil {
		return CompatibilityAsideResult{}, err
	}
	return result, nil
}

func compatibilityAsideTranscriptRuntimeConfig(result CompatibilityAsideResult) map[string]any {
	config := map[string]any{"agentName": result.Frame.AgentName}
	if result.Model != nil {
		config["model"] = result.Model
	}
	if result.Effort != nil {
		config["effort"] = result.Effort
	}
	for _, key := range compatibilityAsideSessionKnobs {
		if value, found := result.InputData[key]; found {
			config[key] = value
		}
	}
	return config
}
