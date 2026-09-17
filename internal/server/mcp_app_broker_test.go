package server

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMCPAppBrokerListsValidatesAndRelaysLiveToolCalls(t *testing.T) {
	broker := newMCPAppBrokerWithDurations(time.Second, time.Second)
	defer broker.Close()
	registration, err := broker.Register(mcpAppRegistrationFixture("artifact-1"))
	if err != nil {
		t.Fatal(err)
	}
	tools, err := broker.ListTools("owner", "root", "bundled:ketcher")
	if err != nil || len(tools) != 1 || tools[0].Name != "highlight_atoms" || tools[0].ArtifactID != "artifact-1" {
		t.Fatalf("tools=%#v err=%v", tools, err)
	}
	if _, err := broker.Call(
		context.Background(), "owner", "root", "bundled:ketcher", "artifact-1",
		"highlight_atoms", map[string]any{"atoms": "not-an-array"},
	); err == nil || !strings.Contains(err.Error(), "unexpected argument") {
		t.Fatalf("invalid schema call error=%v", err)
	}

	resultChannel := make(chan struct {
		result mcpAppCallResult
		err    error
	}, 1)
	go func() {
		result, callErr := broker.Call(
			context.Background(), "owner", "root", "bundled:ketcher", "artifact-1",
			"highlight_atoms", map[string]any{"atoms": []any{1.0, 3.0}},
		)
		resultChannel <- struct {
			result mcpAppCallResult
			err    error
		}{result: result, err: callErr}
	}()
	pollContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request, found, err := broker.Poll(pollContext, "owner", registration.ID)
	if err != nil || !found || request.Tool != "highlight_atoms" || request.ArtifactID != "artifact-1" {
		t.Fatalf("request=%#v found=%t err=%v", request, found, err)
	}
	if err := broker.Resolve("owner", registration.ID, request.RequestID, mcpAppCallResult{
		StructuredContent: map[string]any{"highlighted": true},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case completed := <-resultChannel:
		if completed.err != nil {
			t.Fatal(completed.err)
		}
		structured, ok := completed.result.StructuredContent.(map[string]any)
		if !ok || structured["highlighted"] != true {
			t.Fatalf("result=%#v", completed.result)
		}
	case <-time.After(time.Second):
		t.Fatal("live MCP app call did not complete")
	}
}

func TestMCPAppBrokerRequiresArtifactForAmbiguousTilesAndCleansDisconnects(t *testing.T) {
	broker := newMCPAppBrokerWithDurations(time.Second, time.Second)
	defer broker.Close()
	first, err := broker.Register(mcpAppRegistrationFixture("artifact-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Register(mcpAppRegistrationFixture("artifact-2")); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Call(
		context.Background(), "owner", "root", "bundled:ketcher", "",
		"highlight_atoms", map[string]any{"atoms": []any{1.0}},
	); err == nil || !strings.Contains(err.Error(), "artifact_id is required") {
		t.Fatalf("ambiguous call error=%v", err)
	}

	resultChannel := make(chan error, 1)
	go func() {
		_, callErr := broker.Call(
			context.Background(), "owner", "root", "bundled:ketcher", "artifact-1",
			"highlight_atoms", map[string]any{"atoms": []any{1.0}},
		)
		resultChannel <- callErr
	}()
	pollContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, found, err := broker.Poll(pollContext, "owner", first.ID); err != nil || !found {
		t.Fatalf("poll found=%t err=%v", found, err)
	}
	if err := broker.Unregister("owner", first.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case callErr := <-resultChannel:
		if callErr == nil || !strings.Contains(callErr.Error(), "disconnected") {
			t.Fatalf("disconnect error=%v", callErr)
		}
	case <-time.After(time.Second):
		t.Fatal("disconnect did not release live MCP app call")
	}
}

func TestMCPAppBrokerExpiresAbandonedRegistrations(t *testing.T) {
	broker := newMCPAppBrokerWithDurations(time.Second, time.Second)
	defer broker.Close()
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	broker.now = func() time.Time { return now }
	registration, err := broker.Register(mcpAppRegistrationFixture("artifact-1"))
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	tools, err := broker.ListTools("owner", "root", "bundled:ketcher")
	if err != nil || len(tools) != 0 {
		t.Fatalf("expired tools=%#v err=%v", tools, err)
	}
	if _, _, err := broker.Poll(context.Background(), "owner", registration.ID); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expired registration error=%v", err)
	}
}

func TestMCPAppBrokerPropagatesCancellationAndBoundsCallDuration(t *testing.T) {
	broker := newMCPAppBrokerWithDurations(time.Second, 25*time.Millisecond)
	defer broker.Close()
	registration, err := broker.Register(mcpAppRegistrationFixture("artifact-1"))
	if err != nil {
		t.Fatal(err)
	}

	callContext, cancelCall := context.WithCancel(context.Background())
	cancelled := make(chan error, 1)
	go func() {
		_, callErr := broker.Call(
			callContext, "owner", "root", "bundled:ketcher", "artifact-1",
			"highlight_atoms", map[string]any{"atoms": []any{1.0}},
		)
		cancelled <- callErr
	}()
	pollContext, cancelPoll := context.WithTimeout(context.Background(), time.Second)
	defer cancelPoll()
	request, found, err := broker.Poll(pollContext, "owner", registration.ID)
	if err != nil || !found {
		t.Fatalf("poll found=%t err=%v", found, err)
	}
	cancelCall()
	select {
	case callErr := <-cancelled:
		if !errors.Is(callErr, context.Canceled) {
			t.Fatalf("cancel error=%v", callErr)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not release MCP app call")
	}
	if err := broker.Resolve("owner", registration.ID, request.RequestID, mcpAppCallResult{}); err == nil || !strings.Contains(err.Error(), "request not found") {
		t.Fatalf("late result error=%v", err)
	}

	started := time.Now()
	_, err = broker.Call(
		context.Background(), "owner", "root", "bundled:ketcher", "artifact-1",
		"highlight_atoms", map[string]any{"atoms": []any{2.0}},
	)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout error=%v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded timeout took %s", elapsed)
	}
}

func TestServerCloseReleasesMCPAppCallsAndRejectsStaleViewerState(t *testing.T) {
	broker := newMCPAppBrokerWithDurations(time.Second, time.Second)
	app := &Server{mcpApps: broker}
	registration, err := broker.Register(mcpAppRegistrationFixture("artifact-1"))
	if err != nil {
		t.Fatal(err)
	}
	completed := make(chan error, 1)
	go func() {
		_, callErr := broker.Call(
			context.Background(), "owner", "root", "bundled:ketcher", "artifact-1",
			"highlight_atoms", map[string]any{"atoms": []any{1.0}},
		)
		completed <- callErr
	}()
	pollContext, cancelPoll := context.WithTimeout(context.Background(), time.Second)
	defer cancelPoll()
	if _, found, err := broker.Poll(pollContext, "owner", registration.ID); err != nil || !found {
		t.Fatalf("poll found=%t err=%v", found, err)
	}
	closeContext, cancelClose := context.WithTimeout(context.Background(), time.Second)
	defer cancelClose()
	if err := app.Close(closeContext); err != nil {
		t.Fatal(err)
	}
	select {
	case callErr := <-completed:
		if callErr == nil || !strings.Contains(callErr.Error(), "disconnected") {
			t.Fatalf("server close error=%v", callErr)
		}
	case <-time.After(time.Second):
		t.Fatal("server close did not release MCP app call")
	}
	if _, err := broker.Register(mcpAppRegistrationFixture("artifact-stale")); !errors.Is(err, errMCPAppBrokerClosed) {
		t.Fatalf("registration after close error=%v", err)
	}
	if tools, err := newMCPAppBroker().ListTools("owner", "root", "bundled:ketcher"); err != nil || len(tools) != 0 {
		t.Fatalf("restarted broker inherited tools=%#v err=%v", tools, err)
	}
}

func mcpAppRegistrationFixture(artifactID string) mcpAppRegistrationInput {
	return mcpAppRegistrationInput{
		UserID: "owner", ProjectID: "project", RootFrameID: "root", FrameID: "root",
		ServerID: "bundled:ketcher", ServerName: "ketcher-chemistry", ServerSource: "bundled",
		ArtifactID: artifactID,
		Tools: []mcpAppToolDescriptor{{
			Name: "highlight_atoms", Description: "Highlight atoms in the live editor",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"atoms": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				},
				"required": []any{"atoms"}, "additionalProperties": false,
			},
		}},
	}
}
