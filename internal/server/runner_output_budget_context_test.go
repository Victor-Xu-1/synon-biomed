package server

import (
	"reflect"
	eventjournal "synon-go/internal/persistence/journal"
	"testing"
)

func TestOutputBudgetRecoveryDoesNotRewriteTaskInstructions(t *testing.T) {
	entries := []eventjournal.Entry{{Message: eventjournal.Message{"type": "user_message", "role": "user", "content": "Prepare a detailed report."}}}
	before := sessionEntriesToChatMessages("system", entries)
	entries = append(entries, eventjournal.Entry{Message: eventjournal.Message{"type": "runner_checkpoint", "status": "interrupted", "reason_code": sessionRunnerProviderOutputTokenLimitReasonCode}})
	after := sessionEntriesToChatMessages("system", entries)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("budget recovery altered task instructions: before=%#v after=%#v", before, after)
	}
	state := &sessionRunnerProviderContinuationState{}
	state.Content.WriteString("已接受的内容\n")
	before = appendProviderContinuationContext(nil, state)
	state.ConsecutiveNoProgress = 5
	after = appendProviderContinuationContext(nil, state)
	if !reflect.DeepEqual(before, after) || after[0].Content != "已接受的内容\n" {
		t.Fatalf("continuation changed its accepted prefix or added instructions: %#v", after)
	}
}
