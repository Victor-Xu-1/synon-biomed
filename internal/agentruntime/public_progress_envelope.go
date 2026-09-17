package agentruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	PublicProgressEnvelopeBegin    = "<|PublicProgressBegin|>"
	PublicProgressEnvelopeEnd      = "<|PublicProgressEnd|>"
	PublicProgressEnvelopeContract = "To stream an explicitly public progress update before the response outcome is known, emit exactly <|PublicProgressBegin|>{\"version\":1,\"id\":\"stable-short-id\",\"text\":\"user-visible progress\"}<|PublicProgressEnd|>. Put only concise user-visible process narration in text, never private reasoning or a final conclusion. Text outside this envelope remains a private final candidate until a tool or terminal boundary proves its role. Use a fresh id for each materially new update and never reuse an id with different text."

	maxPublicProgressEnvelopeBytes = 20 * 1024
	maxPublicProgressTextBytes     = 16 * 1024
	maxPublicProgressTextRunes     = 4096
	maxPublicProgressBlockIDBytes  = 128
	maxPublicProgressBlocks        = 64
)

type publicProgressEnvelope struct {
	Version int
	ID      string
	Text    string
}

// publicProgressEnvelopeDecoder is the single provider-neutral decoder for
// explicitly public model progress. Candidate text outside an envelope keeps
// its existing final-gated semantics. A complete envelope is validated before
// any of its text is emitted, so malformed control bytes never reach the UI.
type publicProgressEnvelopeDecoder struct {
	emit       func(ModelStreamEvent) error
	pending    string
	inEnvelope bool
	raw        strings.Builder
	candidate  strings.Builder
	visible    strings.Builder
	count      int
	ids        map[string]struct{}
}

func newPublicProgressEnvelopeDecoder(emit func(ModelStreamEvent) error) *publicProgressEnvelopeDecoder {
	return &publicProgressEnvelopeDecoder{emit: emit, ids: make(map[string]struct{})}
}

func (d *publicProgressEnvelopeDecoder) write(content string) error {
	if content == "" {
		return errors.New("public progress decoder received an empty content delta")
	}
	if !utf8.ValidString(content) {
		return errors.New("public progress decoder received invalid UTF-8")
	}
	if d.raw.Len()+len(content) > maxOpenAIChatResponseBytes {
		return errors.New("public progress response exceeds the model response byte limit")
	}
	d.raw.WriteString(content)
	d.pending += content
	return d.consume(false)
}

func (d *publicProgressEnvelopeDecoder) boundary() error {
	return d.consume(true)
}

func (d *publicProgressEnvelopeDecoder) finish() error {
	return d.consume(true)
}

func (d *publicProgressEnvelopeDecoder) consume(flushOutside bool) error {
	for {
		if d.inEnvelope {
			end := strings.Index(d.pending, PublicProgressEnvelopeEnd)
			if end < 0 {
				if len(d.pending) > maxPublicProgressEnvelopeBytes {
					return errors.New("public progress envelope exceeds the byte limit")
				}
				if flushOutside {
					return errors.New("public progress envelope is incomplete at the model boundary")
				}
				return nil
			}
			payload := d.pending[:end]
			d.pending = d.pending[end+len(PublicProgressEnvelopeEnd):]
			if strings.Contains(payload, PublicProgressEnvelopeBegin) || len(payload) > maxPublicProgressEnvelopeBytes {
				return errors.New("public progress envelope is nested or oversized")
			}
			envelope, err := decodePublicProgressEnvelope(payload)
			if err != nil {
				return err
			}
			if d.count >= maxPublicProgressBlocks {
				return errors.New("public progress response exceeds the block limit")
			}
			if _, duplicate := d.ids[envelope.ID]; duplicate {
				return fmt.Errorf("public progress block id %q is duplicated", envelope.ID)
			}
			d.ids[envelope.ID] = struct{}{}
			d.count++
			d.visible.WriteString(envelope.Text)
			if d.emit != nil {
				if err := d.emit(ModelStreamEvent{
					Kind: ModelStreamEventPublicProgressDelta, BlockID: envelope.ID, ContentDelta: envelope.Text,
				}); err != nil {
					return err
				}
				if err := d.emit(ModelStreamEvent{
					Kind: ModelStreamEventPublicProgressBoundary, BlockID: envelope.ID,
				}); err != nil {
					return err
				}
			}
			d.inEnvelope = false
			continue
		}

		begin := strings.Index(d.pending, PublicProgressEnvelopeBegin)
		if begin >= 0 {
			if err := d.emitCandidate(d.pending[:begin]); err != nil {
				return err
			}
			d.pending = d.pending[begin+len(PublicProgressEnvelopeBegin):]
			d.inEnvelope = true
			continue
		}
		if strings.Contains(d.pending, PublicProgressEnvelopeEnd) {
			return errors.New("public progress response contains an unmatched end marker")
		}
		if flushOutside {
			if partialPublicProgressMarker(d.pending) {
				return errors.New("public progress response ends with an incomplete control marker")
			}
			if err := d.emitCandidate(d.pending); err != nil {
				return err
			}
			d.pending = ""
			return nil
		}
		keep := publicProgressMarkerSuffixLength(d.pending)
		if err := d.emitCandidate(d.pending[:len(d.pending)-keep]); err != nil {
			return err
		}
		d.pending = d.pending[len(d.pending)-keep:]
		return nil
	}
}

func (d *publicProgressEnvelopeDecoder) emitCandidate(content string) error {
	if content == "" {
		return nil
	}
	if strings.Contains(content, "<|PublicProgress") {
		return errors.New("public progress response contains a malformed control marker")
	}
	d.candidate.WriteString(content)
	d.visible.WriteString(content)
	if d.emit != nil {
		return d.emit(ModelStreamEvent{Kind: ModelStreamEventContentDelta, ContentDelta: content})
	}
	return nil
}

func (d *publicProgressEnvelopeDecoder) rawContent() string       { return d.raw.String() }
func (d *publicProgressEnvelopeDecoder) candidateContent() string { return d.candidate.String() }
func (d *publicProgressEnvelopeDecoder) visibleContent() string   { return d.visible.String() }
func (d *publicProgressEnvelopeDecoder) envelopeCount() int       { return d.count }

func publicProgressMarkerSuffixLength(content string) int {
	maximum := min(len(content), len(PublicProgressEnvelopeBegin)-1)
	for size := maximum; size > 0; size-- {
		if strings.HasSuffix(content, PublicProgressEnvelopeBegin[:size]) {
			return size
		}
	}
	return 0
}

func partialPublicProgressMarker(content string) bool {
	return len(content) >= len("<|Public") && strings.HasPrefix(PublicProgressEnvelopeBegin, content)
}

func decodePublicProgressEnvelope(payload string) (publicProgressEnvelope, error) {
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.UseNumber()
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return publicProgressEnvelope{}, errors.New("public progress envelope must be one JSON object")
	}
	var envelope publicProgressEnvelope
	seen := make(map[string]struct{}, 3)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return publicProgressEnvelope{}, errors.New("public progress envelope contains an invalid key")
		}
		key, ok := token.(string)
		if !ok {
			return publicProgressEnvelope{}, errors.New("public progress envelope key must be a string")
		}
		if _, duplicate := seen[key]; duplicate {
			return publicProgressEnvelope{}, fmt.Errorf("public progress envelope duplicates %q", key)
		}
		seen[key] = struct{}{}
		switch key {
		case "version":
			var number json.Number
			if err := decoder.Decode(&number); err != nil || number.String() != "1" {
				return publicProgressEnvelope{}, errors.New("public progress envelope version must be 1")
			}
			envelope.Version = 1
		case "id":
			if err := decoder.Decode(&envelope.ID); err != nil {
				return publicProgressEnvelope{}, errors.New("public progress envelope id must be a string")
			}
		case "text":
			if err := decoder.Decode(&envelope.Text); err != nil {
				return publicProgressEnvelope{}, errors.New("public progress envelope text must be a string")
			}
		default:
			return publicProgressEnvelope{}, fmt.Errorf("public progress envelope field %q is unsupported", key)
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return publicProgressEnvelope{}, errors.New("public progress envelope JSON object is incomplete")
	}
	if err := requirePublicProgressJSONEOF(decoder); err != nil {
		return publicProgressEnvelope{}, err
	}
	if envelope.Version != 1 || !validPublicProgressBlockID(envelope.ID) {
		return publicProgressEnvelope{}, errors.New("public progress envelope requires version 1 and a safe id")
	}
	if strings.TrimSpace(envelope.Text) == "" || !utf8.ValidString(envelope.Text) ||
		len(envelope.Text) > maxPublicProgressTextBytes || utf8.RuneCountInString(envelope.Text) > maxPublicProgressTextRunes ||
		!validPublicProgressTextCharacters(envelope.Text) || strings.Contains(envelope.Text, PublicProgressEnvelopeBegin) ||
		strings.Contains(envelope.Text, PublicProgressEnvelopeEnd) {
		return publicProgressEnvelope{}, errors.New("public progress envelope text is empty, unsafe, or oversized")
	}
	return envelope, nil
}

func validPublicProgressTextCharacters(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t' {
			return false
		}
	}
	return true
}

func requirePublicProgressJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("public progress envelope contains trailing JSON")
	}
	return nil
}

func validPublicProgressBlockID(value string) bool {
	if value == "" || len(value) > maxPublicProgressBlockIDBytes || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("._:-", character) {
			continue
		}
		return false
	}
	return true
}
