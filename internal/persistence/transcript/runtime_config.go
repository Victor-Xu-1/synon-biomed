package transcript

import (
	"context"
	"encoding/json"
)

// LatestFrameRuntimeConfig returns the newest explicit runtime configuration
// attached to a canonical Frame input. Inputs without a configuration do not
// erase the prior explicit choice.
func (r *Repository) LatestFrameRuntimeConfig(
	ctx context.Context,
	streamUID, ownerID string,
) (map[string]any, bool, error) {
	stream, err := r.GetStream(ctx, streamUID, ownerID)
	if err != nil {
		return nil, false, err
	}
	return latestFrameRuntimeConfig(ctx, r.db, stream)
}

func (tx *ImmediateTransaction) LatestFrameRuntimeConfig(
	ctx context.Context,
	streamUID, ownerID string,
) (map[string]any, bool, error) {
	if tx == nil || tx.repository == nil || tx.conn == nil {
		return nil, false, ErrSchemaUnavailable
	}
	stream, err := getStreamConn(ctx, tx.conn, streamUID, ownerID)
	if err != nil {
		return nil, false, schemaError(err)
	}
	return latestFrameRuntimeConfig(ctx, tx.conn, stream)
}

func latestFrameRuntimeConfig(
	ctx context.Context,
	queryer transcriptQueryer,
	stream Stream,
) (map[string]any, bool, error) {
	if stream.Kind != StreamKindFrameRef {
		return nil, false, ErrEventConflict
	}
	var readAuthority, writeAuthority string
	if err := queryer.QueryRowContext(ctx, `SELECT read_authority,write_authority
		FROM transcript_frame_authority WHERE owner_id=? AND session_id=?`,
		stream.OwnerID, stream.SessionID).Scan(&readAuthority, &writeAuthority); err != nil {
		return nil, false, schemaError(err)
	}
	query := `
		SELECT frame_event.payload
		FROM transcript_events event
		JOIN frame_events frame_event ON frame_event.id=event.frame_event_id
		WHERE event.stream_uid=? AND event.source='frame_ref'
			AND event.event_type IN ('user_message','user_input_response')
			AND json_type(frame_event.payload, '$.runtimeConfig') IS NOT NULL
		ORDER BY event.event_id`
	if readAuthority == "transcript_payload_v1" && writeAuthority == "transcript_payload_v1" {
		query = `SELECT CASE WHEN event.source='payload' THEN event.payload_json ELSE frame_event.payload END
			FROM transcript_events event
			JOIN transcript_branch_state state ON state.stream_uid=event.stream_uid
			JOIN transcript_branch_events member ON member.stream_uid=event.stream_uid
				AND member.branch_id=state.active_branch_id AND member.event_id=event.event_id
			LEFT JOIN frame_events frame_event ON event.source='frame_ref' AND frame_event.id=event.frame_event_id
			WHERE event.stream_uid=? AND event.event_type IN ('user_message','user_input_response')
				AND json_type(CASE WHEN event.source='payload' THEN event.payload_json ELSE frame_event.payload END,
					'$.runtimeConfig') IS NOT NULL
			ORDER BY member.ordinal`
	} else if readAuthority != "legacy_mixed_v1" || writeAuthority != "legacy_frame_ref_v1" {
		return nil, false, ErrEventConflict
	}
	rows, err := queryer.QueryContext(ctx, query, stream.UID)
	if err != nil {
		return nil, false, schemaError(err)
	}
	defer rows.Close()
	config := map[string]any{}
	found := false
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, false, schemaError(err)
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, false, ErrEventConflict
		}
		rawConfig, exists := payload["runtimeConfig"]
		if !exists {
			continue
		}
		if len(rawConfig) == 0 || string(rawConfig) == "null" {
			return nil, false, ErrEventConflict
		}
		patch := map[string]any{}
		if err := json.Unmarshal(rawConfig, &patch); err != nil {
			return nil, false, ErrEventConflict
		}
		if len(patch) == 0 {
			return nil, false, ErrEventConflict
		}
		for key, value := range patch {
			config[key] = value
		}
		found = true
	}
	if err := rows.Err(); err != nil {
		return nil, false, schemaError(err)
	}
	if !found {
		return nil, false, nil
	}
	return config, true, nil
}
