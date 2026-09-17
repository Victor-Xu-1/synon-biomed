package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestEngineToolPreambleSurvivesRequiredToolAndPrivatePreflightRepair(t *testing.T) {
	for _, kind := range []string{"required-tool", "private-preflight"} {
		t.Run(kind, func(t *testing.T) {
			const prose = "Checking the existing records before continuing."
			rejected := ToolCall{ID: "proposed", Name: "repl", Arguments: json.RawMessage(`{"blocked":true}`)}
			if kind == "required-tool" {
				rejected.Name = "other"
			}
			model := &repairingStreamingModelClient{responses: []ModelResponse{
				{Message: Message{Role: "assistant", Content: prose, ToolCalls: []ToolCall{rejected}}},
				{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "admitted", Name: "repl", Arguments: json.RawMessage(`{"blocked":false}`)}}}},
				{Message: Message{Role: "assistant", Content: "Done"}},
			}, chunks: [][]string{{prose}, {}, {"Done"}}}
			var executions atomic.Int64
			published := 0
			var events []Event
			engine := Engine{Model: model, Tools: preflightAwareGateway{executions: &executions}, AllowToolPreamble: func(text string) bool { return text == prose }, OnModelDelta: func(event ModelStreamEvent) error {
				if event.ContentDelta == prose {
					published++
				}
				return nil
			}, OnEvent: func(event Event) { events = append(events, event) }}
			choice := map[string]any{"type": "tool", "name": "repl"}
			result, err := engine.Run(context.Background(), RunRequest{Messages: []Message{{Role: "user", Content: "Inspect records"}}, Tools: []ToolSchema{{Name: "repl", Parameters: map[string]any{"type": "object"}}}, InitialToolChoice: choice, MaxToolRounds: 2})
			if err != nil || result.FinalMessage.Content != "Done" || executions.Load() != 1 || published != 1 {
				t.Fatalf("result=%#v err=%v executions=%d publications=%d", result, err, executions.Load(), published)
			}
			retained := 0
			for _, message := range result.Messages {
				if message.Content == prose {
					retained++
					if len(message.ToolCalls) != 0 {
						t.Fatal("rejected proposal escaped into public history")
					}
				}
			}
			if retained != 1 {
				t.Fatalf("retained preambles=%d", retained)
			}
			if len(model.requests) != 3 || !reflect.DeepEqual(model.requests[1].ToolChoice, choice) {
				t.Fatal("repair relaxed required-tool choice")
			}
			for _, event := range events {
				if event.ToolCallID == rejected.ID && (event.Type == EventToolStarted || event.Type == EventToolFailed) {
					t.Fatal("private rejected proposal was executed or published")
				}
			}
		})
	}
}

func TestToolPreamblePublicationKeepsOneBoundaryAndRepairContext(t *testing.T) {
	for _, hasBoundary := range []bool{false, true} {
		message := Message{Role: "assistant", Content: "Inspect records", ToolCalls: []ToolCall{{ID: "proposed", Name: "inspect"}}}
		deltas := []ModelStreamEvent{{ContentDelta: message.Content}}
		if hasBoundary {
			deltas = append(deltas, ModelStreamEvent{Kind: ModelStreamEventToolCallBoundary})
		}
		var emitted []ModelStreamEvent
		engine := Engine{AllowToolPreamble: func(string) bool { return true }, OnModelDelta: func(event ModelStreamEvent) error {
			emitted = append(emitted, event)
			return nil
		}}
		publication, remaining, err := engine.publishToolPreamble(true, message, deltas)
		if err != nil || remaining != nil || len(emitted) != 2 || emitted[0].ContentDelta != message.Content || emitted[1].Kind != ModelStreamEventToolCallBoundary {
			t.Fatalf("boundary=%t emitted=%#v remaining=%#v err=%v", hasBoundary, emitted, remaining, err)
		}
		feedback := Message{Role: "system", Content: "Use the required tool"}
		public, model := publication.toolChoiceRepairMessages([]Message{{Role: "user", Content: "Inspect"}}, feedback)
		if len(public) != 2 || public[1].Content != message.Content || len(public[1].ToolCalls) != 0 || len(model) != 3 || !reflect.DeepEqual(model[2], feedback) {
			t.Fatalf("repair leaked or lost context: public=%#v model=%#v", public, model)
		}
	}
}

func TestToolPreamblePublicationLeavesIneligibleBuffersUntouched(t *testing.T) {
	for _, scenario := range []string{"unconstrained", "no-tools", "no-policy", "denied", "boundary-only"} {
		t.Run(scenario, func(t *testing.T) {
			message := Message{Role: "assistant", Content: "Unpublished", ToolCalls: []ToolCall{{Name: "inspect"}}}
			deltas := []ModelStreamEvent{{ContentDelta: message.Content}}
			enforced, emitted := true, 0
			engine := Engine{AllowToolPreamble: func(string) bool { return true }, OnModelDelta: func(ModelStreamEvent) error { emitted++; return nil }}
			switch scenario {
			case "unconstrained":
				enforced = false
			case "no-tools":
				message.ToolCalls = nil
			case "no-policy":
				engine.AllowToolPreamble = nil
			case "denied":
				engine.AllowToolPreamble = func(string) bool { return false }
			case "boundary-only":
				deltas = []ModelStreamEvent{{Kind: ModelStreamEventToolCallBoundary}}
			}
			publication, remaining, err := engine.publishToolPreamble(enforced, message, deltas)
			if err != nil || len(publication.preserveForRepair(nil)) != 0 {
				t.Fatalf("unpublished prose retained: %#v %v", publication, err)
			}
			if scenario == "boundary-only" {
				if emitted != 1 || remaining != nil {
					t.Fatal("boundary duplicated")
				}
			} else if emitted != 0 || !reflect.DeepEqual(remaining, deltas) {
				t.Fatal("ineligible buffer consumed or published")
			}
		})
	}
}

func TestToolPreamblePublicationPropagatesSinkFailure(t *testing.T) {
	failure := errors.New("publication failed")
	emitted := 0
	engine := Engine{AllowToolPreamble: func(string) bool { return true }, OnModelDelta: func(ModelStreamEvent) error { emitted++; return failure }}
	_, _, err := engine.publishToolPreamble(true, Message{Content: "Inspect", ToolCalls: []ToolCall{{Name: "inspect"}}}, []ModelStreamEvent{{ContentDelta: "Inspect"}})
	if !errors.Is(err, failure) || emitted != 1 {
		t.Fatalf("emitted=%d err=%v", emitted, err)
	}
}
