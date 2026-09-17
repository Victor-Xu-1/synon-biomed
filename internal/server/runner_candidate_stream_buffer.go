package server

import "strings"

// sessionRunnerCandidateStreamBuffer holds one provider response until its
// boundary is known. Narration followed by tool calls is committed as normal
// progress. A no-tool response remains private until all pre-delivery gates
// accept it, preventing a final-looking answer from appearing and then being
// retracted when the same logical task needs a correction.
type sessionRunnerCandidateStreamBuffer struct {
	content strings.Builder
	sealed  bool
}

func (buffer *sessionRunnerCandidateStreamBuffer) append(delta string) {
	if buffer == nil || delta == "" {
		return
	}
	buffer.content.WriteString(delta)
}

func (buffer *sessionRunnerCandidateStreamBuffer) replace(content string) {
	if buffer == nil {
		return
	}
	buffer.content.Reset()
	buffer.content.WriteString(content)
	buffer.sealed = false
}

func (buffer *sessionRunnerCandidateStreamBuffer) seal() {
	if buffer != nil {
		buffer.sealed = true
	}
}

func (buffer *sessionRunnerCandidateStreamBuffer) isSealed() bool {
	return buffer != nil && buffer.sealed
}

func (buffer *sessionRunnerCandidateStreamBuffer) hasContent() bool {
	return buffer != nil && buffer.content.Len() > 0
}

func (buffer *sessionRunnerCandidateStreamBuffer) take() string {
	if buffer == nil {
		return ""
	}
	content := buffer.content.String()
	buffer.content.Reset()
	buffer.sealed = false
	return content
}

func (buffer *sessionRunnerCandidateStreamBuffer) discard() {
	if buffer == nil {
		return
	}
	buffer.content.Reset()
	buffer.sealed = false
}
