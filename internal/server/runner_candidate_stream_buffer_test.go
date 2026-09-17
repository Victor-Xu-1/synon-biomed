package server

import "testing"

func TestSessionRunnerCandidateStreamBufferSeparatesProgressFromFinalCandidate(t *testing.T) {
	buffer := &sessionRunnerCandidateStreamBuffer{}
	buffer.append("progress before tools")
	if buffer.isSealed() || buffer.take() != "progress before tools" {
		t.Fatal("tool-bound progress did not remain publishable")
	}
	buffer.append("candidate final")
	buffer.seal()
	if !buffer.isSealed() {
		t.Fatal("no-tool candidate was not held for pre-delivery validation")
	}
	buffer.discard()
	if buffer.isSealed() || buffer.take() != "" {
		t.Fatal("rejected candidate survived its private validation boundary")
	}
}

func TestSessionRunnerCandidateStreamBufferReplacesPrivateProviderDraft(t *testing.T) {
	buffer := &sessionRunnerCandidateStreamBuffer{}
	buffer.append("我将调用MCP继续处理。")
	buffer.replace("公开证据尚不完整，检索范围需扩展。")
	if got := buffer.take(); got != "公开证据尚不完整，检索范围需扩展。" {
		t.Fatalf("take()=%q", got)
	}
}
