package server

import "testing"

func TestProviderContinuationAcceptsTypedOutputLimitFence(t *testing.T) {
	for _, reason := range []string{
		"provider_stream_interrupted", "provider_stream_no_progress",
		sessionRunnerProviderOutputTokenLimitReasonCode, sessionRunnerModelProtocolErrorReasonCode,
	} {
		if !providerContinuationInterruptionFenceReason(reason) {
			t.Fatalf("typed continuation fence rejected: %s", reason)
		}
	}
	if providerContinuationInterruptionFenceReason("tool_call_failed") {
		t.Fatal("unrelated interruption was accepted as a provider continuation fence")
	}
}

func TestProviderContinuationAcceptsNoProgressFenceAfterClosedBoundary(t *testing.T) {
	contracts := []sessionRunnerProviderContinuationV1{{PreviousAttempt: 4}}
	matched, err := matchProviderContinuationInterruptionFence(contracts, false, 4)
	if err != nil || matched {
		t.Fatalf("closed continuation rejected no-progress fence: matched=%t err=%v", matched, err)
	}
	matched, err = matchProviderContinuationInterruptionFence(contracts, true, 4)
	if err != nil || !matched {
		t.Fatalf("open continuation boundary was not matched: matched=%t err=%v", matched, err)
	}
	if _, err := matchProviderContinuationInterruptionFence(contracts, true, 5); err == nil {
		t.Fatal("foreign attempt matched an open continuation boundary")
	}
}
