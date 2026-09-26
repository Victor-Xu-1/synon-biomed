package transcript

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const RunnerCorrectionConditionChunkField = "correction_condition_chunk"
const correctionConditionChunkBytes = 64 << 10

// A reference describes bytes in this same atomic checkpoint transaction, not
// an external blob/store. Event size stays bounded independently of condition
// size. Byte chunks use base64 JSON so UTF-8 code points may cross boundaries.
type RunnerCorrectionConditionRef struct {
	Group       string `json:"group"`
	SHA256      string `json:"sha256"`
	Fingerprint string `json:"fingerprint"`
	Bytes       int64  `json:"bytes"`
	Chunks      int    `json:"chunks"`
}

func (ref RunnerCorrectionConditionRef) Validate() error {
	for _, value := range []string{ref.Group, ref.SHA256, ref.Fingerprint} {
		decoded, err := hex.DecodeString(value)
		if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != value {
			return errors.New("correction condition reference digest is invalid")
		}
	}
	if ref.Bytes <= 0 || ref.Chunks <= 0 || int64(ref.Chunks) != (ref.Bytes-1)/correctionConditionChunkBytes+1 {
		return errors.New("correction condition reference extent is invalid")
	}
	return nil
}

func decodeRunnerCorrectionConditionRef(value any) (RunnerCorrectionConditionRef, error) {
	var ref RunnerCorrectionConditionRef
	raw, err := json.Marshal(value)
	if err != nil {
		return ref, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ref); err != nil {
		return ref, err
	}
	return ref, ref.Validate()
}

func prepareRunnerCorrectionStorage(clientID string, cause *RunnerInterruptionCause) (*RunnerInterruptionCause, []byte, error) {
	if cause == nil {
		return nil, nil, nil
	}
	if _, err := RunnerInterruptionCausePayload(*cause); err != nil {
		return nil, nil, err
	}
	if cause.ConditionRef != nil {
		return nil, nil, errors.New("a new interruption must supply complete condition evidence, not a reference")
	}
	if cause.Condition == nil {
		return cause, nil, nil
	}
	raw, err := EncodeRunnerCorrectionCondition(*cause.Condition)
	if err != nil {
		return nil, nil, err
	}
	stored := *cause
	stored.Condition = nil
	stored.ConditionRef = &RunnerCorrectionConditionRef{
		Group: correctionContentDigest([]byte(clientID)), SHA256: correctionContentDigest(raw),
		Fingerprint: cause.Condition.Fingerprint(), Bytes: int64(len(raw)),
		Chunks: (len(raw)-1)/correctionConditionChunkBytes + 1,
	}
	return &stored, raw, nil
}

type runnerCorrectionConditionChunk struct {
	Reference RunnerCorrectionConditionRef `json:"reference"`
	Index     int                          `json:"index"`
	Data      []byte                       `json:"data"`
}

func appendRunnerCorrectionChunksConn(ctx context.Context, conn *sql.Conn, input InterruptRunnerInput,
	phase RunnerPhase, cause *RunnerInterruptionCause, raw []byte, now time.Time) error {
	if len(raw) == 0 {
		return nil
	}
	ref := *cause.ConditionRef
	for index, offset := 0, 0; offset < len(raw); index, offset = index+1, offset+correctionConditionChunkBytes {
		if err := ctx.Err(); err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{RunnerCorrectionConditionChunkField: runnerCorrectionConditionChunk{
			Reference: ref, Index: index, Data: raw[offset:min(offset+correctionConditionChunkBytes, len(raw))],
		}})
		if err != nil {
			return err
		}
		if err := validatePayload(EventSourcePayload, payload, nil); err != nil {
			return err
		}
		if _, _, _, err := appendRunnerCheckpointConn(ctx, conn, AppendRunnerCheckpointInput{
			Claim: input.Claim, ClientMessageID: fmt.Sprintf("%s:condition:%d", input.ClientMessageID, index),
			Phase: phase, PayloadJSON: payload, Resumable: false,
		}, now); err != nil {
			return err
		}
	}
	return nil
}

// RunnerCorrectionAssembler is a single forward-projection reducer. At most
// one contiguous condition is pending; it does not build a history-sized map.
// Callers must supply only owner/branch-fenced canonical checkpoint payloads.
type RunnerCorrectionAssembler struct {
	reference RunnerCorrectionConditionRef
	next      int
	data      bytes.Buffer
}

func (assembler *RunnerCorrectionAssembler) Observe(payload map[string]any) (bool, error) {
	value, present := payload[RunnerCorrectionConditionChunkField]
	if !present {
		return false, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return true, err
	}
	var chunk runnerCorrectionConditionChunk
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&chunk); err != nil {
		return true, err
	}
	if err := chunk.Reference.Validate(); err != nil {
		return true, err
	}
	if chunk.Index == 0 {
		*assembler = RunnerCorrectionAssembler{reference: chunk.Reference}
	}
	if chunk.Reference != assembler.reference || chunk.Index != assembler.next || chunk.Index >= chunk.Reference.Chunks {
		return true, errors.New("correction condition chunks are not consecutive")
	}
	expected := min(int64(correctionConditionChunkBytes), chunk.Reference.Bytes-int64(chunk.Index)*correctionConditionChunkBytes)
	if int64(len(chunk.Data)) != expected {
		return true, errors.New("correction condition chunk has invalid size")
	}
	assembler.data.Write(chunk.Data)
	assembler.next++
	return true, nil
}

func (assembler *RunnerCorrectionAssembler) Resolve(cause RunnerInterruptionCause) (RunnerInterruptionCause, error) {
	if cause.ConditionRef == nil {
		return cause, nil
	}
	ref := *cause.ConditionRef
	if ref != assembler.reference || assembler.next != ref.Chunks || int64(assembler.data.Len()) != ref.Bytes || correctionContentDigest(assembler.data.Bytes()) != ref.SHA256 {
		return cause, errors.New("correction condition evidence is missing or corrupt")
	}
	condition, err := DecodeRunnerCorrectionCondition(assembler.data.Bytes())
	if err != nil {
		return cause, err
	}
	if condition.ReasonCode != cause.ReasonCode || condition.Fingerprint() != ref.Fingerprint {
		return cause, errors.New("correction condition identity conflicts with its evidence")
	}
	cause.Condition, cause.ConditionRef = &condition, nil
	*assembler = RunnerCorrectionAssembler{}
	return cause, nil
}
