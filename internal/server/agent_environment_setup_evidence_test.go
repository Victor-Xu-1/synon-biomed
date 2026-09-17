package server

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
)

func TestManagedEnvironmentSetupEvidenceRequiresSuccessfulDeepReads(t *testing.T) {
	const required = "https://docs.example.org/current/install/"
	fetchArguments, _ := json.Marshal(map[string]any{"url": required + "?platform=linux"})
	searchArguments, _ := json.Marshal(map[string]any{"url": "https://docs.example.org/other"})
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{
			{ID: "search-only", Name: "web_search", Arguments: searchArguments},
			{ID: "unavailable", Name: "web_fetch", Arguments: fetchArguments},
		}},
		{Role: "tool", ToolCallID: "search-only", Content: `{"ok":true,"sources":[{"url":"` + required + `"}]}`},
		{Role: "tool", ToolCallID: "unavailable", Content: `{"ok":true,"result":{"sourceUnavailable":true,"url":"` + required + `"}}`},
	}
	evidence, missing := managedEnvironmentSetupEvidenceFromMessages(messages, []string{required})
	if len(evidence) != 0 || !reflect.DeepEqual(missing, []string{required}) {
		t.Fatalf("search/unavailable evidence=%v missing=%v", evidence, missing)
	}
	messages = append(messages,
		agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "deep-read", Name: "web_fetch", Arguments: fetchArguments,
		}}},
		agentruntime.Message{Role: "tool", ToolCallID: "deep-read", Content: `{"ok":true,"result":{"url":"` + required + `","body":"current compatibility matrix"}}`},
	)
	evidence, missing = managedEnvironmentSetupEvidenceFromMessages(messages, []string{required})
	if len(missing) != 0 || evidence[required] != "tool-call:deep-read" {
		t.Fatalf("deep-read evidence=%v missing=%v", evidence, missing)
	}
}

func TestManagedEnvironmentSetupEvidenceURLKeyIgnoresQueryAndTrailingSlash(t *testing.T) {
	left := managedEnvironmentSetupEvidenceURLKey("https://Docs.Example.org/current/install/?platform=linux")
	right := managedEnvironmentSetupEvidenceURLKey("https://docs.example.org/current/install")
	if left == "" || left != right {
		t.Fatalf("URL keys differ: %q %q", left, right)
	}
}

func TestManagedEnvironmentSetupEvidenceRequiresReadingLargeFetchArtifact(t *testing.T) {
	const (
		required  = "https://docs.example.org/current/install"
		versionID = "ltr-current-install"
	)
	fetchArguments, _ := json.Marshal(map[string]any{"url": required})
	readArguments, _ := json.Marshal(map[string]any{"version_id": versionID})
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "fetch-large", Name: "web_fetch", Arguments: fetchArguments,
		}}},
		{Role: "tool", ToolCallID: "fetch-large", Content: `{"outcome":"succeeded","artifact_id":"large-result","version_id":"` + versionID + `","read_with":"read_file(version_id=\"` + versionID + `\")"}`},
	}
	state := managedEnvironmentSetupEvidenceStateFromMessages(messages, []string{required})
	if len(state.evidence) != 0 || !reflect.DeepEqual(state.missing, []string{required}) || state.pendingReads[required] != versionID {
		t.Fatalf("unread large result state=%+v", state)
	}
	actions := managedEnvironmentSetupEvidenceRequiredActions(state.missing, state.pendingReads)
	if len(actions) != 1 || actions[0]["tool"] != "read_file" ||
		mapValue(actions[0]["input"])["version_id"] != versionID {
		t.Fatalf("required actions=%v", actions)
	}
	messages = append(messages,
		agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "read-large", Name: "read_file", Arguments: readArguments,
		}}},
		agentruntime.Message{Role: "tool", ToolCallID: "read-large", Content: `{"ok":true,"content":"current compatibility matrix"}`},
	)
	state = managedEnvironmentSetupEvidenceStateFromMessages(messages, []string{required})
	if len(state.missing) != 0 || len(state.pendingReads) != 0 || strings.TrimSpace(state.evidence[required]) == "" {
		t.Fatalf("read large result state=%+v", state)
	}
}

func TestManagedEnvironmentSetupEvidenceRequiredActionsUseExactMissingURLs(t *testing.T) {
	missing := []string{
		"https://docs.example.org/current/a",
		"https://docs.example.org/current/b",
	}
	actions := managedEnvironmentSetupEvidenceRequiredActions(missing, nil)
	if len(actions) != len(missing) {
		t.Fatalf("required actions=%v", actions)
	}
	for index, action := range actions {
		input := mapValue(action["input"])
		if action["tool"] != "web_fetch" || input["url"] != missing[index] || input["limit"] != 2<<20 {
			t.Fatalf("action[%d]=%v", index, action)
		}
	}
}
