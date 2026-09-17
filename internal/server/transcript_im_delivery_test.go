package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	adaptercommon "synon-go/internal/adapters/common"
	adapterfeishu "synon-go/internal/adapters/feishu"
	"synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestIMRunnerUsesTranscriptAuthorityAndDeliversThroughReceiptWorker(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"transcript answer"}}]}`))
	}))
	defer provider.Close()

	received := make(chan adaptercommon.ServerMessage, 8)
	receiptErrors := make(chan error, 8)
	var server *Server
	dispatcher, err := adaptercommon.NewSessionOutboundDispatcher(context.Background(), adaptercommon.SessionOutboundDispatcherOptions{
		FlushInterval: time.Millisecond,
		Handlers: map[string]adaptercommon.SessionOutboundHandler{
			"wechat": func(_ context.Context, _ adaptercommon.IMSessionBinding, message adaptercommon.ServerMessage) (adaptercommon.OutboundReport, error) {
				received <- message
				return adaptercommon.OutboundReport{Platform: "wechat", DeliverySummary: "delivered"}, nil
			},
		},
		OnResult: func(result adaptercommon.SessionOutboundResult) {
			if server != nil {
				receiptErrors <- server.RecordIMOutboundResult(result)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	server = New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir(), IMOutbound: dispatcher, StartBackgroundServices: true})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	sessionID, _, err := server.recordInboundLiveSession(inboundLiveSessionRecord{
		Platform: "wechat", ChatID: "chat-transcript", SenderID: "sender-a", Text: "answer through transcript",
		MessageID: "message-a", ContextToken: "ctx-transcript", SourceEventID: "source-a", ClientMessageID: "client-a", OwnerUserID: "owner-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{SessionID: sessionID, RunnerID: "runner-im", Endpoint: provider.URL + "/v1/chat/completions", Model: "local-model",
		LeaseTTL: time.Minute, MaxAttempts: 1, DisableSkillDiscovery: true,
	})
	if err != nil || result.Status != "completed" {
		entries, _ := server.eventJournal.ReadAll(sessionID)
		var terminalPayload string
		_ = db.QueryRow(`SELECT payload_json FROM transcript_events WHERE event_type='runner_finished' ORDER BY created_at DESC LIMIT 1`).Scan(&terminalPayload)
		t.Fatalf("runner=%#v err=%v entries=%#v terminal=%s", result, err, entries, terminalPayload)
	}
	deadline := time.After(5 * time.Second)
	terminalDelivered := false
	messageTypes := make([]string, 0, 4)
	for !terminalDelivered {
		select {
		case err := <-receiptErrors:
			if err != nil {
				t.Fatal(err)
			}
		case message := <-received:
			messageTypes = append(messageTypes, fmt.Sprint(message["type"]))
			if message["type"] == "message_complete" {
				terminalDelivered = true
			}
		case <-deadline:
			state, stateErr := server.loadIMOutboundDeliveryState(imOutboundStateKey(sessionID))
			var pending int
			_ = db.QueryRow(`SELECT COUNT(*) FROM transcript_delivery_intents WHERE destination=? AND status!='delivered'`, sessionID).Scan(&pending)
			t.Fatalf("transcript IM terminal was not delivered: types=%#v state=%#v stateErr=%v pending=%d", messageTypes, state, stateErr, pending)
		}
	}
	if !reflect.DeepEqual(messageTypes, []string{"content_start", "content_delta", "message_complete"}) {
		t.Fatalf("outbound message types=%#v", messageTypes)
	}
	stream, found, err := repo.GetStreamBySession(context.Background(), "owner-a", sessionID)
	if err != nil || !found || stream.Kind != transcriptstore.StreamKindStandalone {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	state, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, int64(result.Attempt))
	if err != nil || state.Status != "completed" {
		t.Fatalf("runtime=%#v err=%v", state, err)
	}
	for {
		var undelivered int
		if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_delivery_intents
			WHERE stream_uid=? AND destination=? AND status!='delivered'`, stream.UID, sessionID).Scan(&undelivered); err != nil {
			t.Fatal(err)
		} else if undelivered == 0 {
			break
		}
		select {
		case err := <-receiptErrors:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("undelivered transcript IM intents remained")
		}
	}
	if events := transcriptWebEvents(t, store, "owner-a"); len(events) != 0 {
		t.Fatalf("standalone IM leaked into Web events: %#v", events)
	}
}

func TestTranscriptIMMessageNormalizesRecoveryEventsToAdapterContract(t *testing.T) {
	tests := []struct {
		name        string
		eventType   string
		payload     string
		messageType string
		textKey     string
		text        string
	}{
		{name: "assistant snapshot", eventType: "assistant_message", payload: `{"text":"durable final answer"}`, messageType: "content_delta", textKey: "text", text: "durable final answer"},
		{name: "completed terminal", eventType: "runner_finished", payload: `{"status":"completed"}`, messageType: "message_complete"},
		{name: "failed terminal", eventType: "runner_finished", payload: `{"status":"failed","detail":"provider failed"}`, messageType: "error", textKey: "message", text: "provider failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message, err := transcriptIMMessage(transcriptstore.DeliveryClaim{
				Event:               transcriptstore.Event{Type: test.eventType},
				ResolvedPayloadJSON: []byte(test.payload),
			})
			if err != nil {
				t.Fatalf("transcriptIMMessage() error = %v", err)
			}
			if message["type"] != test.messageType {
				t.Fatalf("message = %#v, want type %q", message, test.messageType)
			}
			if test.textKey != "" && message[test.textKey] != test.text {
				t.Fatalf("message = %#v, want %s=%q", message, test.textKey, test.text)
			}
		})
	}
}

func TestTranscriptIMRouteSuppressesLegacyJournalOutbound(t *testing.T) {
	_, repo, _ := newTranscriptWebFixture(t)
	outbound := &recordingSessionOutboundSink{}
	server := newTranscriptIMTestServer(repo, outbound)
	const sessionID = "im:feishu:single-authority"
	server.transcriptIMMu.Lock()
	server.transcriptIMRoutes[sessionID] = "owner-a"
	server.transcriptIMMu.Unlock()

	delivered := server.publishSessionMessage(sessionID, 41, journal.Message{
		"type": "content_delta", "text": "must not use legacy outbound",
	})
	if delivered {
		t.Fatal("legacy journal outbound reported delivery for a transcript-authoritative IM route")
	}
	if events := outbound.eventSnapshot(); len(events) != 0 {
		t.Fatalf("legacy journal outbound competed with transcript delivery: %#v", events)
	}
}

func TestIMRunnerLoopClaimsStandaloneTranscriptWithoutExplicitSessionID(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	providerCalls := make(chan struct{}, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		providerCalls <- struct{}{}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"claimed standalone"}}]}`))
	}))
	defer provider.Close()
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir(), IMOutbound: &recordingSessionOutboundSink{}})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	sessionID, _, err := server.recordInboundLiveSession(inboundLiveSessionRecord{
		Platform: "wechat", ChatID: "loop-no-session", SenderID: "sender-a", Text: "claim me",
		MessageID: "message-loop", ContextToken: "ctx-loop", ClientMessageID: "client-loop", OwnerUserID: "owner-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- server.RunSessionRunnerChatLoop(ctx, SessionRunnerChatOptions{RunnerID: "runner-loop", Endpoint: provider.URL + "/v1/chat/completions", Model: "local-model",
			LeaseTTL: time.Minute, PollInterval: time.Millisecond, MaxAttempts: 1,
			DisableSkillDiscovery: true,
		})
	}()
	select {
	case <-providerCalls:
	case err := <-done:
		cancel()
		t.Fatalf("production runner loop exited before provider call: %v", err)
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("production runner loop never claimed standalone transcript")
	}
	stream, found, err := repo.GetStreamBySession(context.Background(), "owner-a", sessionID)
	if err != nil || !found {
		t.Fatalf("stream found=%t err=%v", found, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		state, stateErr := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, 1)
		if stateErr == nil && state.Status == "completed" {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("runtime=%#v err=%v", state, stateErr)
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestTranscriptRunnerClientMessageIDDoesNotPersistClaimToken(t *testing.T) {
	claim := transcriptstore.RunnerClaim{
		StreamUID: "stream-secret", RunnerID: "runner-secret", Attempt: 3,
		ClaimToken: "plaintext-claim-token-must-not-be-persisted",
	}
	first := transcriptRunnerClientMessageID(claim, "checkpoint")
	second := transcriptRunnerClientMessageID(claim, "checkpoint")
	if first != second || strings.Contains(first, claim.ClaimToken) || len(first) > 128 {
		t.Fatalf("unsafe or unstable client message id=%q", first)
	}
	if first == transcriptRunnerClientMessageID(claim, "runner-finished") {
		t.Fatal("different runner events shared a client message identity")
	}
}

func TestTranscriptIMDeliveryRequiresExternalReceiptAndFencesStaleClaims(t *testing.T) {
	_, repo, db := newTranscriptWebFixture(t)
	const (
		ownerID   = "owner-a"
		sessionID = "im:wechat:chat-a"
		streamID  = "stream-im-a"
	)
	if _, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: streamID, OwnerID: ownerID, ExternalID: sessionID, SessionID: sessionID,
		Kind: transcriptstore.StreamKindStandalone, Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: streamID, OwnerID: ownerID, ClientMessageID: "user-1", PayloadJSON: []byte(`{"text":"work"}`),
	}); err != nil {
		t.Fatal(err)
	}
	runner, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: streamID, OwnerID: ownerID, RunnerID: "runner-a", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !runner.Claimed {
		t.Fatalf("runner=%#v err=%v", runner, err)
	}
	sink := &recordingSessionOutboundSink{}
	server := newTranscriptIMTestServer(repo, sink)
	if configured, err := server.configureTranscriptIMRoute(context.Background(), ownerID, sessionID); err != nil || !configured {
		t.Fatalf("configure route=%t err=%v", configured, err)
	}
	appendEvent := func(id, eventType, payload string) transcriptstore.Event {
		t.Helper()
		event, created, err := repo.AppendRunnerEvent(context.Background(), transcriptstore.AppendEventInput{
			Claim: runner.Claim, ClientMessageID: id, Type: eventType,
			Source: transcriptstore.EventSourcePayload, PayloadJSON: []byte(payload),
			Destinations: []string{sessionID},
		})
		if err != nil || !created {
			t.Fatalf("append %s created=%t err=%v", id, created, err)
		}
		return event
	}
	first := appendEvent("delta-1", "content_delta", `{"text":"first"}`)
	if err := server.drainTranscriptIMDeliveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	events := sink.eventSnapshot()
	if len(events) != 1 || events[0].EventID != first.PublicationSeq || events[0].Message["text"] != "first" {
		t.Fatalf("enqueued events=%#v", events)
	}
	intent, err := repo.GetDeliveryIntent(context.Background(), ownerID, streamID, first.PublicationSeq, sessionID, 1)
	if err != nil || intent.Status != "inflight" || intent.AttemptCount != 1 {
		t.Fatalf("enqueue was treated as receipt: %#v err=%v", intent, err)
	}
	firstReceipt := deliveredTranscriptIMResult(server, sessionID, first.PublicationSeq)
	if err := server.RecordIMOutboundResult(firstReceipt); err != nil {
		t.Fatal(err)
	}
	if err := server.RecordIMOutboundResult(firstReceipt); !errors.Is(err, transcriptstore.ErrDeliveryClaimStale) {
		t.Fatalf("duplicate transcript receipt error=%v", err)
	}
	intent, err = repo.GetDeliveryIntent(context.Background(), ownerID, streamID, first.PublicationSeq, sessionID, 1)
	if err != nil || intent.Status != "delivered" {
		t.Fatalf("receipt did not acknowledge intent: %#v err=%v", intent, err)
	}

	second := appendEvent("delta-2", "content_delta", `{"text":"second"}`)
	terminal := appendEvent("finish-1", "message_complete", `{}`)
	if err := server.drainTranscriptIMDeliveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	failedResult := deliveredTranscriptIMResult(server, sessionID, second.PublicationSeq)
	failedResult.Delivery = adaptercommon.OutboundDeliveryResult{Delivered: false, Attempts: 3, Error: "temporary failure"}
	if err := server.RecordIMOutboundResult(failedResult); err != nil {
		t.Fatal(err)
	}
	intent, err = repo.GetDeliveryIntent(context.Background(), ownerID, streamID, second.PublicationSeq, sessionID, 1)
	if err != nil || intent.Status != "pending" || intent.LastErrorCode != "im_delivery_failed" {
		t.Fatalf("failed receipt=%#v err=%v", intent, err)
	}
	if err := server.drainTranscriptIMDeliveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := sink.eventSnapshot(); len(got) != 2 || got[1].EventID != second.PublicationSeq {
		t.Fatalf("terminal crossed failed delivery gap: %#v", got)
	}
	if _, err := db.Exec(`UPDATE transcript_delivery_intents SET next_attempt_at=NULL
		WHERE stream_uid=? AND publication_seq=? AND destination=?`, streamID, second.PublicationSeq, sessionID); err != nil {
		t.Fatal(err)
	}
	if err := server.drainTranscriptIMDeliveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	intent, err = repo.GetDeliveryIntent(context.Background(), ownerID, streamID, second.PublicationSeq, sessionID, 1)
	if err != nil || intent.Status != "inflight" || intent.AttemptCount != 2 {
		t.Fatalf("retry claim=%#v err=%v", intent, err)
	}

	key := transcriptIMClaimKey{sessionID: sessionID, eventID: second.PublicationSeq}
	server.transcriptIMMu.Lock()
	preRestartClaim := server.transcriptIMClaims[key]
	server.transcriptIMMu.Unlock()
	if _, err := db.Exec(`UPDATE transcript_delivery_intents
		SET lease_expires_at='1970-01-01T00:00:00Z'
		WHERE stream_uid=? AND publication_seq=? AND destination=?`, streamID, second.PublicationSeq, sessionID); err != nil {
		t.Fatal(err)
	}
	restartedSink := &recordingSessionOutboundSink{}
	restarted := newTranscriptIMTestServer(repo, restartedSink)
	if configured, err := restarted.configureTranscriptIMRoute(context.Background(), ownerID, sessionID); err != nil || !configured {
		t.Fatalf("restart configure route=%t err=%v", configured, err)
	}
	if err := restarted.drainTranscriptIMDeliveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	staleResult := adaptercommon.SessionOutboundResult{
		SessionID: sessionID, EventID: second.PublicationSeq, ReceiptToken: preRestartClaim.ClaimToken,
		MessageType: "content_delta", Delivery: adaptercommon.OutboundDeliveryResult{Delivered: true, Attempts: 1},
	}
	if err := restarted.RecordIMOutboundResult(staleResult); !errors.Is(err, transcriptstore.ErrDeliveryClaimStale) {
		t.Fatalf("stale pre-restart receipt error=%v", err)
	}
	if err := restarted.RecordIMOutboundResult(deliveredTranscriptIMResult(restarted, sessionID, second.PublicationSeq)); err != nil {
		t.Fatal(err)
	}
	restartedEvents := restartedSink.eventSnapshot()
	if len(restartedEvents) != 2 || restartedEvents[0].EventID != second.PublicationSeq || restartedEvents[1].EventID != terminal.PublicationSeq {
		t.Fatalf("restart delivery order=%#v", restartedEvents)
	}
	if err := restarted.RecordIMOutboundResult(deliveredTranscriptIMResult(restarted, sessionID, terminal.PublicationSeq)); err != nil {
		t.Fatal(err)
	}
	intent, err = repo.GetDeliveryIntent(context.Background(), ownerID, streamID, second.PublicationSeq, sessionID, 1)
	if err != nil || intent.Status != "delivered" || intent.AttemptCount != 3 {
		t.Fatalf("restart receipt=%#v err=%v", intent, err)
	}

	foreignSink := &recordingSessionOutboundSink{}
	foreign := newTranscriptIMTestServer(repo, foreignSink)
	if configured, err := foreign.configureTranscriptIMRoute(context.Background(), "owner-b", sessionID); err != nil || configured {
		t.Fatalf("foreign route=%t err=%v", configured, err)
	}
	if err := foreign.drainTranscriptIMDeliveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(foreignSink.eventSnapshot()) != 0 {
		t.Fatalf("foreign owner received events=%#v", foreignSink.eventSnapshot())
	}
	if err := server.revokeTranscriptIMRoute(context.Background(), ownerID, sessionID); err != nil {
		t.Fatal(err)
	}
	route, found, err := repo.GetDeliveryRoute(context.Background(), ownerID, streamID, sessionID)
	if err != nil || !found || route.Status != "revoked" {
		t.Fatalf("revoked route=%#v found=%t err=%v", route, found, err)
	}
}

func TestTranscriptIMInvisibleSystemEventSettlesBeforeTerminal(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	received := make(chan adaptercommon.ServerMessage, 2)
	receiptErrors := make(chan error, 4)
	var server *Server
	dispatcher, err := adaptercommon.NewSessionOutboundDispatcher(context.Background(), adaptercommon.SessionOutboundDispatcherOptions{
		FlushInterval: time.Millisecond,
		Handlers: map[string]adaptercommon.SessionOutboundHandler{
			"wechat": func(_ context.Context, _ adaptercommon.IMSessionBinding, message adaptercommon.ServerMessage) (adaptercommon.OutboundReport, error) {
				received <- message
				return adaptercommon.OutboundReport{Platform: "wechat", DeliverySummary: "delivered"}, nil
			},
		},
		OnResult: func(result adaptercommon.SessionOutboundResult) {
			if server != nil {
				receiptErrors <- server.RecordIMOutboundResult(result)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	server = New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir(), IMOutbound: dispatcher})
	sessionID, _, err := server.recordInboundLiveSession(inboundLiveSessionRecord{
		Platform: "wechat", ChatID: "system-noop", SenderID: "sender-a", Text: "run",
		MessageID: "message-system", ContextToken: "ctx-system", ClientMessageID: "client-system", OwnerUserID: "owner-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetStreamBySession(context.Background(), "owner-a", sessionID)
	if err != nil || !found {
		t.Fatalf("stream found=%t err=%v", found, err)
	}
	runner, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-system", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !runner.Claimed {
		t.Fatalf("runner=%#v err=%v", runner, err)
	}
	if _, created, err := repo.AppendRunnerEvent(context.Background(), transcriptstore.AppendEventInput{
		Claim: runner.Claim, ClientMessageID: "system-stop-hook", Type: "system_message",
		Source: transcriptstore.EventSourcePayload, PayloadJSON: []byte(`{"text":"stop hook completed"}`),
		Destinations: []string{sessionID},
	}); err != nil || !created {
		t.Fatalf("system created=%t err=%v", created, err)
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: runner.Claim, ClientMessageID: "system-finish", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`), Destinations: []string{sessionID},
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.drainTranscriptIMRoute(context.Background(), "owner-a", sessionID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-receiptErrors:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("system no-op receipt was not persisted")
	}
	select {
	case message := <-received:
		if message["type"] != "message_complete" {
			t.Fatalf("unexpected external system projection=%#v", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal remained blocked behind system event")
	}
}

func TestTranscriptIMHeartbeatExtendsInFlightClaimBeforeSlowDeliveryCanReclaim(t *testing.T) {
	_, repo, _ := newTranscriptWebFixture(t)
	const ownerID, sessionID, streamID = "owner-a", "im:wechat:slow", "stream-slow"
	if _, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: streamID, OwnerID: ownerID, ExternalID: sessionID, SessionID: sessionID,
		Kind: transcriptstore.StreamKindStandalone, Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: streamID, OwnerID: ownerID, ClientMessageID: "slow-user", PayloadJSON: []byte(`{"text":"slow"}`),
	}); err != nil {
		t.Fatal(err)
	}
	runner, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: streamID, OwnerID: ownerID, RunnerID: "runner-slow", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !runner.Claimed {
		t.Fatalf("runner=%#v err=%v", runner, err)
	}
	server := newTranscriptIMTestServer(repo, &recordingSessionOutboundSink{})
	if configured, err := server.configureTranscriptIMRoute(context.Background(), ownerID, sessionID); err != nil || !configured {
		t.Fatalf("configure=%t err=%v", configured, err)
	}
	event, _, err := repo.AppendRunnerEvent(context.Background(), transcriptstore.AppendEventInput{
		Claim: runner.Claim, ClientMessageID: "slow-delta", Type: "content_delta",
		Source: transcriptstore.EventSourcePayload, PayloadJSON: []byte(`{"text":"slow result"}`), Destinations: []string{sessionID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.drainTranscriptIMRoute(context.Background(), ownerID, sessionID); err != nil {
		t.Fatal(err)
	}
	key := transcriptIMClaimKey{sessionID: sessionID, eventID: event.PublicationSeq}
	server.transcriptIMMu.Lock()
	claim := server.transcriptIMClaims[key]
	claim.ExpiresAt = time.Now().Add(time.Second)
	server.transcriptIMClaims[key] = claim
	server.transcriptIMMu.Unlock()
	before, err := repo.GetDeliveryIntent(context.Background(), ownerID, streamID, event.PublicationSeq, sessionID, 1)
	if err != nil || before.LeaseExpiresAt == nil {
		t.Fatalf("before=%#v err=%v", before, err)
	}
	if err := server.heartbeatTranscriptIMClaims(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := repo.GetDeliveryIntent(context.Background(), ownerID, streamID, event.PublicationSeq, sessionID, 1)
	if err != nil || after.LeaseExpiresAt == nil || !after.LeaseExpiresAt.After(*before.LeaseExpiresAt) {
		t.Fatalf("heartbeat did not extend lease: before=%#v after=%#v err=%v", before, after, err)
	}
}

func TestTranscriptIMQueueSaturationHasSingleDurableSettlement(t *testing.T) {
	workspace, repo, _ := newTranscriptWebFixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var server *Server
	dispatcher, err := adaptercommon.NewSessionOutboundDispatcher(context.Background(), adaptercommon.SessionOutboundDispatcherOptions{
		QueueSize: 1, EnqueueWait: 10 * time.Millisecond, FlushInterval: time.Millisecond,
		Handlers: map[string]adaptercommon.SessionOutboundHandler{
			"wechat": func(_ context.Context, _ adaptercommon.IMSessionBinding, message adaptercommon.ServerMessage) (adaptercommon.OutboundReport, error) {
				if message["message"] == "block" {
					close(started)
					<-release
				}
				return adaptercommon.OutboundReport{Platform: "wechat", DeliverySummary: "delivered"}, nil
			},
		},
		OnResult: func(result adaptercommon.SessionOutboundResult) {
			if server != nil {
				_ = server.RecordIMOutboundResult(result)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	defer close(release)
	server = New(Options{Workspace: workspace, Transcript: repo, FileRoot: t.TempDir(), IMOutbound: dispatcher})
	sessionID, _, err := server.recordInboundLiveSession(inboundLiveSessionRecord{
		Platform: "wechat", ChatID: "queue-full", SenderID: "sender-a", Text: "run",
		MessageID: "message-queue", ContextToken: "ctx-queue", ClientMessageID: "client-queue", OwnerUserID: "owner-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !dispatcher.Enqueue(adaptercommon.SessionOutboundEvent{
		SessionID: sessionID, Message: adaptercommon.ServerMessage{"type": "error", "message": "block"},
	}) {
		t.Fatal("enqueue blocking event")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("blocking delivery did not start")
	}
	if !dispatcher.Enqueue(adaptercommon.SessionOutboundEvent{
		SessionID: sessionID, Message: adaptercommon.ServerMessage{"type": "error", "message": "queued"},
	}) {
		t.Fatal("enqueue queue filler")
	}
	stream, found, err := repo.GetStreamBySession(context.Background(), "owner-a", sessionID)
	if err != nil || !found {
		t.Fatalf("stream found=%t err=%v", found, err)
	}
	runner, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-queue", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !runner.Claimed {
		t.Fatalf("runner=%#v err=%v", runner, err)
	}
	event, _, err := repo.AppendRunnerEvent(context.Background(), transcriptstore.AppendEventInput{
		Claim: runner.Claim, ClientMessageID: "queue-delta", Type: "content_delta",
		Source: transcriptstore.EventSourcePayload, PayloadJSON: []byte(`{"text":"queued transcript"}`),
		Destinations: []string{sessionID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.drainTranscriptIMRoute(context.Background(), "owner-a", sessionID); err != nil {
		t.Fatalf("queue saturation produced conflicting settlements: %v", err)
	}
	intent, err := repo.GetDeliveryIntent(context.Background(), "owner-a", stream.UID, event.PublicationSeq, sessionID, 1)
	if err != nil || intent.Status != "pending" || intent.LastErrorCode != "im_delivery_failed" || intent.AttemptCount != 1 {
		t.Fatalf("queue-full intent=%#v err=%v", intent, err)
	}
}

func TestIMInboundRecoversLegacyProjectionAfterCanonicalAppend(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	root := t.TempDir()
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: root, IMOutbound: &recordingSessionOutboundSink{}})
	if _, err := server.pairingStore.AllowForOwner("wechat", "sender-a", "sender", "owner-a"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	indexPath := filepath.Join(root, "sessions", "index.json")
	server.imProjectionHook = func() {
		if err := os.MkdirAll(filepath.Dir(indexPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(indexPath, []byte("{invalid"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	record := inboundLiveSessionRecord{
		Platform: "wechat", ChatID: "projection-recovery", SenderID: "sender-a", Text: "persist once",
		MessageID: "message-recovery", ContextToken: "ctx-projection-recovery", SourceEventID: "source-recovery", ClientMessageID: "client-recovery", OwnerUserID: "owner-a",
	}
	if _, _, err := server.recordInboundLiveSession(record); err == nil {
		t.Fatal("expected session projection failure after canonical and journal append")
	}
	server.imProjectionHook = nil
	if err := os.WriteFile(indexPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	streamID := transcriptIMStreamUID(imLiveSessionID(record.Platform, record.ChatID))
	var canonicalUsers, stagedUsers int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='user_message_staged'`, streamID).Scan(&stagedUsers); err != nil || stagedUsers != 1 {
		t.Fatalf("staged users=%d err=%v", stagedUsers, err)
	}
	sessionID, eventID, err := server.recordInboundLiveSession(record)
	if err != nil || eventID != 1 {
		t.Fatalf("recovered session=%q event=%d err=%v", sessionID, eventID, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='user_message'`, streamID).Scan(&canonicalUsers); err != nil || canonicalUsers != 1 {
		t.Fatalf("canonical retry users=%d err=%v", canonicalUsers, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='user_message_staged'`, streamID).Scan(&stagedUsers); err != nil || stagedUsers != 0 {
		t.Fatalf("staged retry users=%d err=%v", stagedUsers, err)
	}
	entries, err := server.eventJournal.ReadAll(sessionID)
	if err != nil || len(entries) != 1 || entries[0].ClientMessageID != record.ClientMessageID {
		t.Fatalf("legacy entries=%#v err=%v", entries, err)
	}
	session, found, err := server.sessionStore.Get(sessionID)
	if err != nil || !found || session.MessageCount != 1 {
		t.Fatalf("legacy session=%#v found=%t err=%v", session, found, err)
	}
}

func TestIMInboundDoesNotAdmitBeforeDeliveryRouteIsReady(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir(), IMOutbound: &recordingSessionOutboundSink{}})
	if _, err := server.pairingStore.AllowForOwner("wechat", "sender-route", "route sender", "owner-route"); err != nil {
		t.Fatal(err)
	}
	if err := server.stopTranscriptWebDelivery(context.Background()); err != nil {
		t.Fatal(err)
	}
	record := inboundLiveSessionRecord{
		Platform: "wechat", ChatID: "route-failure", SenderID: "sender-route",
		Text: "do not claim before route", ContextToken: "ctx-route-failure", ClientMessageID: "client-route", OwnerUserID: "owner-route",
		TargetType: "chat", TargetID: "route-failure",
	}
	sessionID := imLiveSessionID(record.Platform, record.ChatID)
	streamUID := transcriptIMStreamUID(sessionID)
	if _, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: streamUID, OwnerID: record.OwnerUserID, ExternalID: sessionID, SessionID: sessionID,
		Kind: transcriptstore.StreamKindStandalone, Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_im_route BEFORE INSERT ON transcript_delivery_routes
		WHEN NEW.destination='im:wechat:route-failure'
		BEGIN SELECT RAISE(ABORT, 'route activation blocked'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.recordInboundLiveSession(record); err == nil || !strings.Contains(err.Error(), "activate IM transcript route") {
		t.Fatalf("route failure error=%v", err)
	}
	stream, err := repo.GetStream(context.Background(), streamUID, record.OwnerUserID)
	if err != nil || stream.InputRevision != 0 {
		t.Fatalf("stream=%#v err=%v", stream, err)
	}
	if next, err := repo.ClaimNextRunner(context.Background(), transcriptstore.ClaimNextRunnerInput{
		RunnerID: "must-not-claim", TTL: time.Minute,
	}); err != nil || next.Claimed {
		t.Fatalf("claim=%#v err=%v", next, err)
	}
	var staged int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='user_message_staged'`, streamUID).Scan(&staged); err != nil || staged != 1 {
		t.Fatalf("staged=%d err=%v", staged, err)
	}
	if _, err := db.Exec(`DROP TRIGGER reject_im_route`); err != nil {
		t.Fatal(err)
	}
	second := record
	second.MessageID = "message-route-b"
	second.ClientMessageID = "client-route-b"
	second.Text = "second input must not pass the staged prefix"
	if _, _, err := server.recordInboundLiveSession(second); !errors.Is(err, transcriptstore.ErrEventConflict) {
		t.Fatalf("out-of-order second input error=%v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='user_message_staged'`, streamUID).Scan(&staged); err != nil || staged != 1 {
		t.Fatalf("out-of-order staged=%d err=%v", staged, err)
	}
	routeEntry, found, err := server.runtimeStore.Get(imOutboundRouteNamespace, imOutboundStateKey(sessionID))
	if err != nil || !found {
		t.Fatalf("persisted first route found=%t err=%v", found, err)
	}
	var routeState imOutboundRouteState
	if err := decodeRuntimeValue(routeEntry.Value, &routeState); err != nil || routeState.MessageID != record.MessageID {
		t.Fatalf("out-of-order input changed route=%#v err=%v", routeState, err)
	}
	if next, err := repo.ClaimNextRunner(context.Background(), transcriptstore.ClaimNextRunnerInput{
		RunnerID: "still-must-not-claim", TTL: time.Minute,
	}); err != nil || next.Claimed {
		t.Fatalf("out-of-order claim=%#v err=%v", next, err)
	}
	restarted := New(Options{Workspace: store, Transcript: repo, FileRoot: server.fileRoot, IMOutbound: &recordingSessionOutboundSink{}})
	if report, err := restarted.RecoverIMOutbound(context.Background()); err != nil || report.Restored != 1 {
		t.Fatalf("recovery report=%#v err=%v", report, err)
	}
	if _, _, err := restarted.recordInboundLiveSession(second); err != nil {
		t.Fatalf("second input after prefix recovery error=%v", err)
	}
	stream, err = repo.GetStream(context.Background(), streamUID, record.OwnerUserID)
	if err != nil || stream.InputRevision != 2 {
		t.Fatalf("recovered stream=%#v err=%v", stream, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='user_message_staged'`, streamUID).Scan(&staged); err != nil || staged != 0 {
		t.Fatalf("recovered staged=%d err=%v", staged, err)
	}
}

func TestIMInboundCutsOverToCanonicalTranscriptAtJournalCapacity(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	root := t.TempDir()
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: root, IMOutbound: &recordingSessionOutboundSink{}})
	if _, err := server.pairingStore.AllowForOwner("wechat", "sender-a", "sender", "owner-a"); err != nil {
		t.Fatal(err)
	}
	server.eventJournal = journal.NewEventJournalWithLimits(
		filepath.Join(root, "bounded-journal"), journal.Limits{MaxEntryBytes: 4096, MaxFileBytes: 4096, MaxEntries: 1},
	)
	first := inboundLiveSessionRecord{
		Platform: "wechat", ChatID: "canonical-cutover", MessageID: "message-a", SenderID: "sender-a",
		Text: "first", ContextToken: "ctx-canonical-cutover", ClientMessageID: "client-a", OwnerUserID: "owner-a", TargetType: "chat", TargetID: "canonical-cutover",
	}
	sessionID, firstJournalID, err := server.recordInboundLiveSession(first)
	if err != nil || firstJournalID != 1 {
		t.Fatalf("first session=%q event=%d err=%v", sessionID, firstJournalID, err)
	}
	second := first
	second.MessageID, second.ClientMessageID, second.Text = "message-b", "client-b", "second"
	if _, secondJournalID, err := server.recordInboundLiveSession(second); err != nil || secondJournalID != 0 {
		t.Fatalf("canonical cutover event=%d err=%v", secondJournalID, err)
	}
	stream, found, err := repo.GetStreamBySession(context.Background(), first.OwnerUserID, sessionID)
	if err != nil || !found || stream.InputRevision != 2 {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	entries, err := server.eventJournal.ReadAllStrict(sessionID)
	if err != nil || len(entries) != 1 || entries[0].ClientMessageID != first.ClientMessageID {
		t.Fatalf("journal entries=%#v err=%v", entries, err)
	}
	restarted := New(Options{Workspace: store, Transcript: repo, FileRoot: root, IMOutbound: &recordingSessionOutboundSink{}})
	restarted.eventJournal = journal.NewEventJournalWithLimits(
		filepath.Join(root, "bounded-journal"), journal.Limits{MaxEntryBytes: 4096, MaxFileBytes: 4096, MaxEntries: 1},
	)
	if report, err := restarted.RecoverIMOutbound(context.Background()); err != nil || report.Restored != 1 {
		t.Fatalf("restart recovery report=%#v err=%v", report, err)
	}
}

func TestInboundTranscriptProjectionRejectsUnknownAndIncompleteShapes(t *testing.T) {
	record := inboundLiveSessionRecord{
		Platform: "wechat", ChatID: "shape", MessageID: "message", SenderID: "sender", Text: "shape",
		TargetType: "chat", TargetID: "shape", OwnerUserID: "owner-shape",
	}
	_, _, _, valid, err := inboundTranscriptPayload(record)
	if err != nil {
		t.Fatal(err)
	}
	unknown := append([]byte(nil), valid[:len(valid)-1]...)
	unknown = append(unknown, []byte(`,"unexpected":true}`)...)
	if _, err := decodeInboundTranscriptProjection(unknown); err == nil {
		t.Fatal("unknown staged payload field was accepted")
	}
	var incomplete map[string]any
	if err := json.Unmarshal(valid, &incomplete); err != nil {
		t.Fatal(err)
	}
	delete(incomplete, "target_id")
	encoded, err := json.Marshal(incomplete)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeInboundTranscriptProjection(encoded); err == nil {
		t.Fatal("incomplete staged payload was accepted")
	}
}

func TestIMMessageDedupIdentityIsStableAndChatScoped(t *testing.T) {
	first, err := imMessageDedupID("wechat", "chat-a", "", "message-1", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := imMessageDedupID("wechat", "chat-a", "", "message-2", "")
	if err != nil || second == first {
		t.Fatalf("same-chat identities first=%q second=%q err=%v", first, second, err)
	}
	foreignChat, err := imMessageDedupID("wechat", "chat-b", "", "message-1", "")
	if err != nil || foreignChat == first {
		t.Fatalf("cross-chat identities first=%q foreign=%q err=%v", first, foreignChat, err)
	}
	if _, err := imMessageDedupID("wechat", "chat-a", "", "", ""); err == nil {
		t.Fatal("chat-only fallback was accepted")
	}
}

func TestIMMessageToolRejectsMissingStableIdentityBeforeSideEffects(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir(), IMOutbound: &recordingSessionOutboundSink{}})
	allowPairingForTest(t, server, "feishu", "identity-sender")
	_, err := server.executeIMMessageTool(context.Background(), map[string]any{
		"platform": "feishu", "chatId": "identity-chat", "senderId": "identity-sender", "text": "missing identity",
	})
	if err == nil || !strings.Contains(err.Error(), "stable") {
		t.Fatalf("missing identity error=%v", err)
	}
	if tasks, err := server.taskStore.List(); err != nil || len(tasks) != 0 {
		t.Fatalf("tasks=%#v err=%v", tasks, err)
	}
	var streams int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_streams WHERE session_id='im:feishu:identity-chat'`).Scan(&streams); err != nil || streams != 0 {
		t.Fatalf("streams=%d err=%v", streams, err)
	}
}

func TestIMInboundPanicFailsFollowersAndAllowsRepair(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir(), IMOutbound: &recordingSessionOutboundSink{}})
	allowPairingForTest(t, server, "feishu", "panic-sender")
	entered := make(chan struct{})
	release := make(chan struct{})
	server.imProjectionHook = func() { close(entered); <-release; panic("projection panic") }
	inbound := adapterfeishu.InboundEvent{
		EventID: "panic-event", MessageID: "panic-message", ChatID: "panic-chat", SenderOpenID: "panic-sender",
		MessageType: "text", Text: "panic and retry", DedupID: "panic-dedup", TaskTitle: "panic and retry",
	}
	leaderPanic := make(chan any, 1)
	go func() {
		defer func() { leaderPanic <- recover() }()
		_, _ = server.HandleFeishuInbound(context.Background(), inbound)
	}()
	<-entered
	followerErr := make(chan error, 1)
	go func() { _, err := server.HandleFeishuInbound(context.Background(), inbound); followerErr <- err }()
	select {
	case err := <-followerErr:
		t.Fatalf("follower returned before leader panic: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if recovered := <-leaderPanic; recovered != "projection panic" {
		t.Fatalf("recovered=%#v", recovered)
	}
	if err := <-followerErr; !errors.Is(err, adaptercommon.ErrMessageDedupPanic) {
		t.Fatalf("follower error=%v", err)
	}
	server.imProjectionHook = nil
	if result, err := server.HandleFeishuInbound(context.Background(), inbound); err != nil || result["deduplicated"] != false {
		t.Fatalf("repair result=%#v err=%v", result, err)
	}
}

func TestIMMessageToolFollowersWaitAndFailedLeaderCanRepair(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	root := t.TempDir()
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: root, IMOutbound: &recordingSessionOutboundSink{}})
	allowPairingForTest(t, server, "feishu", "tool-sender")
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	server.imProjectionHook = func() { once.Do(func() { close(entered); <-release }) }
	input := map[string]any{
		"platform": "feishu", "chatId": "tool-chat", "messageId": "tool-message", "messageType": "text",
		"senderId": "tool-sender", "text": "repair tool admission", "sourceEventId": "tool-event",
		"clientMessageId": "feishu:message:tool-message",
	}
	errorsCh := make(chan error, 2)
	go func() { _, err := server.executeIMMessageTool(context.Background(), input); errorsCh <- err }()
	<-entered
	indexPath := filepath.Join(root, "sessions", "index.json")
	if err := os.MkdirAll(filepath.Dir(indexPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, []byte("{invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	go func() { _, err := server.executeIMMessageTool(context.Background(), input); errorsCh <- err }()
	select {
	case err := <-errorsCh:
		t.Fatalf("tool follower returned before leader: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	for range 2 {
		if err := <-errorsCh; err == nil {
			t.Fatal("failed tool admission returned success")
		}
	}
	server.imProjectionHook = nil
	if err := os.WriteFile(indexPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := server.executeIMMessageTool(context.Background(), input)
	if err != nil || result.(map[string]any)["deduplicated"] != false {
		t.Fatalf("repair result=%#v err=%v", result, err)
	}
	conflict := maps.Clone(input)
	conflict["text"] = "conflicting tool payload"
	if _, err := server.executeIMMessageTool(context.Background(), conflict); !errors.Is(err, adaptercommon.ErrMessageDedupConflict) {
		t.Fatalf("tool conflict error=%v", err)
	}
}

func TestStagedIMRecoveryRejectsConflictingDurableJournal(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir(), IMOutbound: &recordingSessionOutboundSink{}})
	record := inboundLiveSessionRecord{
		Platform: "wechat", ChatID: "journal-conflict", MessageID: "message", SenderID: "sender",
		Text: "canonical text", ContextToken: "ctx-journal-conflict", ClientMessageID: "client", OwnerUserID: "owner",
		TargetType: "chat", TargetID: "journal-conflict", TaskTitle: "canonical task",
	}
	sessionID := imLiveSessionID(record.Platform, record.ChatID)
	streamUID := transcriptIMStreamUID(sessionID)
	if _, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: streamUID, OwnerID: record.OwnerUserID, ExternalID: sessionID, SessionID: sessionID,
		Kind: transcriptstore.StreamKindStandalone, Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	_, _, _, payload, err := inboundTranscriptPayload(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.StageUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: streamUID, OwnerID: record.OwnerUserID, ClientMessageID: record.ClientMessageID, PayloadJSON: payload,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.eventJournal.Append(sessionID, journal.Message{
		"type": "im_message", "role": "user", "platform": record.Platform, "chatId": record.ChatID,
		"ownerUserId": record.OwnerUserID, "messageId": record.MessageID, "senderId": record.SenderID,
		"text": "tampered text", "taskTitle": record.TaskTitle,
	}, journal.Metadata{ClientMessageID: record.ClientMessageID}); err != nil {
		t.Fatal(err)
	}
	binding := adaptercommon.IMSessionBinding{
		SessionID: sessionID, Platform: record.Platform, ChatID: record.ChatID, MessageID: record.MessageID,
		SenderID: record.SenderID, ContextToken: record.ContextToken, TargetType: record.TargetType, TargetID: record.TargetID, OwnerUserID: record.OwnerUserID,
	}
	if _, _, err := server.reconcileStagedIMInputs(context.Background(), binding); !errors.Is(err, transcriptstore.ErrEventConflict) {
		t.Fatalf("conflicting journal error=%v", err)
	}
	stream, err := repo.GetStream(context.Background(), streamUID, record.OwnerUserID)
	if err != nil || stream.InputRevision != 0 {
		t.Fatalf("stream=%#v err=%v", stream, err)
	}
}

func TestRollbackUnprojectedInboundTaskPreservesTaskOnJournalReadFailure(t *testing.T) {
	server := New(Options{FileRoot: t.TempDir()})
	sessionID, clientMessageID := "im:wechat:rollback", "rollback-client"
	task, err := server.ensureInboundTask(sessionID, clientMessageID, "rollback task")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := server.eventJournal.Append(sessionID, journal.Message{
		"type": "im_message", "role": "user", "platform": "wechat", "chatId": "rollback",
		"ownerUserId": "local", "messageId": "rollback-message", "senderId": "rollback-sender",
		"text": "rollback", "taskId": task.ID, "taskTitle": task.Title,
	}, journal.Metadata{ClientMessageID: clientMessageID})
	if err != nil || entry == nil {
		t.Fatalf("entry=%#v err=%v", entry, err)
	}
	path := filepath.Join(server.eventJournal.Root(), "session-events", url.QueryEscape(sessionID)+".jsonl")
	valid, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(valid, []byte("{broken\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := server.rollbackUnprojectedInboundTask(sessionID, clientMessageID, task.ID); err == nil {
		t.Fatal("journal read failure was ignored")
	}
	if persisted, found, err := server.taskStore.Get(task.ID); err != nil || !found || persisted.ID != task.ID {
		t.Fatalf("task=%#v found=%t err=%v", persisted, found, err)
	}
	if err := os.WriteFile(path, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	if repaired, err := server.ensureInboundTask(sessionID, clientMessageID, task.Title); err != nil || repaired.ID != task.ID {
		t.Fatalf("repaired task=%#v err=%v", repaired, err)
	}
}

func TestExistingIMJournalProjectionRequiresTypedFieldsAndTaskProvenance(t *testing.T) {
	server := New(Options{FileRoot: t.TempDir()})
	payload := inboundTranscriptProjection{
		Platform: "wechat", ChatID: "typed", MessageID: "42", SenderID: "sender", Text: "7",
		TargetType: "chat", TargetID: "typed", TaskTitle: "typed task",
	}
	base := journal.Message{
		"type": "im_message", "role": "user", "platform": "wechat", "chatId": "typed",
		"ownerUserId": "owner", "messageId": "42", "senderId": "sender", "text": "7", "taskTitle": "typed task",
	}
	numeric := maps.Clone(base)
	numeric["text"] = float64(7)
	if err := server.validateExistingIMJournalProjection(numeric, payload, "owner", "im:wechat:typed", "client"); !errors.Is(err, transcriptstore.ErrEventConflict) {
		t.Fatalf("numeric string impersonation error=%v", err)
	}
	task, err := server.taskStore.Create("typed task")
	if err != nil {
		t.Fatal(err)
	}
	wrongTask := maps.Clone(base)
	wrongTask["taskId"] = task.ID
	if err := server.validateExistingIMJournalProjection(wrongTask, payload, "owner", "im:wechat:typed", "client"); !errors.Is(err, transcriptstore.ErrEventConflict) {
		t.Fatalf("unbound same-title task error=%v", err)
	}
}

func TestIMInboundSharedProjectionRepairRejectsOwnerAndPayloadConflicts(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	outbound := &recordingSessionOutboundSink{}
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir(), IMOutbound: outbound})
	record := inboundLiveSessionRecord{
		Platform: "wechat", ChatID: "shared-repair", SenderID: "sender-a", Text: "persist once",
		MessageID: "message-shared", ContextToken: "ctx-shared-repair", MessageType: "text", SourceEventID: "source-shared", ClientMessageID: "client-shared", OwnerUserID: "owner-a",
		TargetType: "user", TargetID: "sender-a", Downloads: []map[string]any{{"id": "download-a"}},
	}
	sessionID, _, err := server.recordInboundLiveSession(record)
	if err != nil {
		t.Fatal(err)
	}
	session, found, err := server.sessionStore.Get(sessionID)
	if err != nil || !found {
		t.Fatalf("session found=%t err=%v", found, err)
	}
	session.MessageCount = 0
	session.LastRole = ""
	session.LastUserMessageAt = time.Time{}
	if err := server.sessionStore.Save(session); err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.recordInboundLiveSession(record); err != nil {
		t.Fatalf("repair shared projection: %v", err)
	}
	session, _, err = server.sessionStore.Get(sessionID)
	if err != nil || session.MessageCount != 1 || session.LastRole != "user" {
		t.Fatalf("repaired session=%#v err=%v", session, err)
	}

	routeBefore, found, err := server.runtimeStore.Get(imOutboundRouteNamespace, imOutboundStateKey(sessionID))
	if err != nil || !found {
		t.Fatalf("route before conflict found=%t err=%v", found, err)
	}
	bindingsBefore := len(outbound.bindingSnapshot())
	conflicts := []inboundLiveSessionRecord{record, record, record, record, record}
	conflicts[0].Text = "different payload"
	conflicts[1].SenderID = "sender-b"
	conflicts[2].TargetID = "sender-b"
	conflicts[3].MessageType = "richText"
	conflicts[4].Downloads = []map[string]any{{"id": "download-b"}}
	for index, conflict := range conflicts {
		if _, _, err := server.recordInboundLiveSession(conflict); !errors.Is(err, transcriptstore.ErrEventConflict) {
			t.Fatalf("payload conflict %d error=%v", index, err)
		}
	}
	foreignOwner := record
	foreignOwner.OwnerUserID = "owner-b"
	if _, _, err := server.recordInboundLiveSession(foreignOwner); !errors.Is(err, transcriptstore.ErrOwnerMismatch) {
		t.Fatalf("owner conflict error=%v", err)
	}
	routeAfter, found, err := server.runtimeStore.Get(imOutboundRouteNamespace, imOutboundStateKey(sessionID))
	if err != nil || !found || !reflect.DeepEqual(routeAfter.Value, routeBefore.Value) || len(outbound.bindingSnapshot()) != bindingsBefore {
		t.Fatalf("rejected conflict changed route: before=%#v after=%#v bindings=%d err=%v", routeBefore.Value, routeAfter.Value, len(outbound.bindingSnapshot()), err)
	}
	var streams, canonicalUsers, deliveryIntents int
	for query, target := range map[string]*int{
		`SELECT COUNT(*) FROM transcript_streams WHERE session_id=?`: &streams,
		`SELECT COUNT(*) FROM transcript_events event JOIN transcript_streams stream ON stream.stream_uid=event.stream_uid
			WHERE stream.session_id=? AND event.event_type='user_message'`: &canonicalUsers,
		`SELECT COUNT(*) FROM transcript_delivery_intents intent JOIN transcript_streams stream ON stream.stream_uid=intent.stream_uid
			WHERE stream.session_id=?`: &deliveryIntents,
	} {
		if err := db.QueryRow(query, sessionID).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := server.eventJournal.ReadAll(sessionID)
	if err != nil || streams != 1 || canonicalUsers != 1 || deliveryIntents != 0 || len(entries) != 1 {
		t.Fatalf("streams=%d canonical=%d intents=%d entries=%d err=%v", streams, canonicalUsers, deliveryIntents, len(entries), err)
	}
}

func TestIMInboundCreatedProjectionDoesNotRescanLongJournal(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	root := t.TempDir()
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: root, IMOutbound: &recordingSessionOutboundSink{}})
	first := inboundLiveSessionRecord{
		Platform: "wechat", ChatID: "long-journal", SenderID: "sender", Text: "first",
		MessageID: "message-first", ContextToken: "ctx-long-journal", ClientMessageID: "client-first", OwnerUserID: "owner-a",
	}
	sessionID, _, err := server.recordInboundLiveSession(first)
	if err != nil {
		t.Fatal(err)
	}
	for index := range 256 {
		if _, err := server.eventJournal.Append(sessionID, journal.Message{
			"type": "system_message", "role": "system", "text": fmt.Sprintf("history-%d", index),
		}, journal.Metadata{}); err != nil {
			t.Fatal(err)
		}
		if err := server.sessionStore.AppendMessage(sessionID, "system"); err != nil {
			t.Fatal(err)
		}
	}
	journalPath := filepath.Join(server.eventJournal.Root(), "session-events", url.QueryEscape(sessionID)+".jsonl")
	if err := os.Chmod(journalPath, 0o200); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(journalPath, 0o600) })
	second := first
	second.Text = "second"
	second.MessageID = "message-second"
	second.ClientMessageID = "client-second"
	if _, _, err := server.recordInboundLiveSession(second); err != nil {
		t.Fatalf("created projection rescanned durable history: %v", err)
	}
}

func newTranscriptIMTestServer(
	repo *transcriptstore.Repository,
	sink adaptercommon.SessionOutboundSink,
) *Server {
	return &Server{
		transcriptStore: repo, imOutbound: sink,
		transcriptIMClaims: map[transcriptIMClaimKey]transcriptstore.DeliveryClaim{},
		transcriptIMRoutes: map[string]string{},
	}
}

func deliveredTranscriptIMResult(server *Server, sessionID string, eventID int64) adaptercommon.SessionOutboundResult {
	server.transcriptIMMu.Lock()
	claim := server.transcriptIMClaims[transcriptIMClaimKey{sessionID: sessionID, eventID: eventID}]
	server.transcriptIMMu.Unlock()
	return adaptercommon.SessionOutboundResult{
		SessionID: sessionID, EventID: eventID, ReceiptToken: claim.ClaimToken, MessageType: "content_delta",
		Delivery: adaptercommon.OutboundDeliveryResult{Delivered: true, Attempts: 1},
	}
}
