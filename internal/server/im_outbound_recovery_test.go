package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adaptercommon "synon-go/internal/adapters/common"
	eventjournal "synon-go/internal/persistence/journal"
	secretstore "synon-go/internal/persistence/secrets"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestIMOutboundRecoveryPersistsEncryptedRouteAndReplaysOnlyUncommittedRun(t *testing.T) {
	root := t.TempDir()
	initialSink := &recordingSessionOutboundSink{}
	initial := New(Options{FileRoot: root, IMOutbound: initialSink})
	if _, err := initial.pairingStore.Allow("wechat", "wx-recovery-1", "recovery user"); err != nil {
		t.Fatalf("allow recovery pairing: %v", err)
	}
	sessionID, _, err := initial.recordInboundLiveSession(inboundLiveSessionRecord{
		Platform: "wechat", ChatID: "wx-recovery-1", SenderID: "wx-recovery-1",
		MessageID: "message-1", ContextToken: "context-token-must-stay-encrypted",
		TargetType: "user", TargetID: "wx-recovery-1", OwnerUserID: secretstore.DefaultUserID,
		Text: "recover this session", ClientMessageID: "client-1",
	})
	if err != nil {
		t.Fatalf("record inbound session: %v", err)
	}
	journal := eventjournal.NewEventJournal(root)
	for _, message := range []eventjournal.Message{
		{"type": "runner_checkpoint", "text": "runner chat started"},
		{"type": "content_delta", "text": "durable answer"},
		{"type": "runner_finished", "status": "completed"},
	} {
		if _, err := journal.Append(sessionID, message, eventjournal.Metadata{}); err != nil {
			t.Fatalf("append recovery journal: %v", err)
		}
	}

	assertIMRouteTokenEncrypted(t, root, "context-token-must-stay-encrypted")
	assertInternalIMRouteSecretHidden(t, initial, sessionID)

	recoveredSink := &recordingSessionOutboundSink{}
	restarted := New(Options{FileRoot: root, IMOutbound: recoveredSink})
	report, err := restarted.RecoverIMOutbound(context.Background())
	if err != nil {
		t.Fatalf("RecoverIMOutbound(): %v", err)
	}
	if report.Routes != 1 || report.Restored != 1 || report.Skipped != 0 || report.Replayed != 3 {
		t.Fatalf("recovery report = %#v", report)
	}
	bindings := recoveredSink.bindingSnapshot()
	if len(bindings) != 1 || bindings[0].SessionID != sessionID || bindings[0].ContextToken != "context-token-must-stay-encrypted" {
		t.Fatalf("restored bindings = %#v", bindings)
	}
	if events := recoveredSink.eventSnapshot(); len(events) != 3 || events[0].EventID != 2 || events[2].EventID != 4 {
		t.Fatalf("replayed events = %#v", events)
	}

	if err := restarted.RecordIMOutboundResult(adaptercommon.SessionOutboundResult{
		SessionID: sessionID, EventID: 4, MessageType: "message_complete",
		Delivery: adaptercommon.OutboundDeliveryResult{Delivered: true, Attempts: 1},
	}); err != nil {
		t.Fatalf("commit outbound terminal event: %v", err)
	}
	committedSink := &recordingSessionOutboundSink{}
	committedRestart := New(Options{FileRoot: root, IMOutbound: committedSink})
	committedReport, err := committedRestart.RecoverIMOutbound(context.Background())
	if err != nil {
		t.Fatalf("recover committed route: %v", err)
	}
	if committedReport.Restored != 1 || committedReport.Replayed != 0 || len(committedSink.eventSnapshot()) != 0 {
		t.Fatalf("committed recovery = %#v events=%#v", committedReport, committedSink.eventSnapshot())
	}

	for _, message := range []eventjournal.Message{
		{"type": "runner_checkpoint", "text": "runner chat started"},
		{"type": "content_delta", "text": "retry after restart"},
		{"type": "runner_finished", "status": "failed", "text": "redacted failure"},
	} {
		if _, err := journal.Append(sessionID, message, eventjournal.Metadata{}); err != nil {
			t.Fatalf("append failed recovery run: %v", err)
		}
	}
	if err := committedRestart.RecordIMOutboundResult(adaptercommon.SessionOutboundResult{
		SessionID: sessionID, EventID: 7, MessageType: "error",
		Delivery: adaptercommon.OutboundDeliveryResult{Delivered: false, Attempts: 3, Error: "temporary channel failure"},
	}); err != nil {
		t.Fatalf("record failed terminal event: %v", err)
	}
	retrySink := &recordingSessionOutboundSink{}
	retryRestart := New(Options{FileRoot: root, IMOutbound: retrySink})
	retryReport, err := retryRestart.RecoverIMOutbound(context.Background())
	if err != nil {
		t.Fatalf("recover failed run: %v", err)
	}
	if retryReport.Restored != 1 || retryReport.Replayed != 3 {
		t.Fatalf("retry recovery = %#v", retryReport)
	}
	if events := retrySink.eventSnapshot(); len(events) != 3 || events[0].EventID != 5 || events[2].EventID != 7 {
		t.Fatalf("retry replay events = %#v", events)
	}
	bindings = retrySink.bindingSnapshot()
	if len(bindings) != 1 {
		t.Fatalf("retry bindings = %#v", bindings)
	}
	if err := retryRestart.AuthorizeIMOutbound(bindings[0]); err != nil {
		t.Fatalf("paired route authorization: %v", err)
	}
	if _, err := retryRestart.executePairingTool(context.Background(), "pairing_revoke", map[string]any{
		"platform": "wechat", "userId": "wx-recovery-1",
	}); err != nil {
		t.Fatalf("revoke pairing and route: %v", err)
	}
	if err := retryRestart.AuthorizeIMOutbound(bindings[0]); err == nil {
		t.Fatal("revoked route remained authorized")
	}
	if unbound := retrySink.unboundSnapshot(); len(unbound) != 1 || unbound[0] != sessionID {
		t.Fatalf("unbound routes = %#v", unbound)
	}
	routes, err := retryRestart.runtimeStore.List(imOutboundRouteNamespace)
	if err != nil || len(routes) != 0 {
		t.Fatalf("routes after revoke = %#v err=%v", routes, err)
	}
	secretID := internalIMRouteSecretID(sessionID, secretstore.DefaultUserID)
	if _, found, err := retryRestart.secretStore.ResolveForUser(secretID, secretstore.DefaultUserID); err != nil || found {
		t.Fatalf("encrypted route token after revoke: found=%t err=%v", found, err)
	}
	postRevoke := New(Options{FileRoot: root, IMOutbound: &recordingSessionOutboundSink{}})
	postRevokeReport, err := postRevoke.RecoverIMOutbound(context.Background())
	if err != nil || postRevokeReport.Routes != 0 {
		t.Fatalf("post-revoke recovery = %#v err=%v", postRevokeReport, err)
	}
}

func TestIMOutboundTerminalResultCannotAdvancePastAnOlderFailedEvent(t *testing.T) {
	root := t.TempDir()
	server := New(Options{FileRoot: root})
	sessionID := "im:wechat:delivery-gap"
	record := func(eventID int64, messageType string, delivered bool) {
		t.Helper()
		if err := server.RecordIMOutboundResult(adaptercommon.SessionOutboundResult{
			SessionID: sessionID, EventID: eventID, MessageType: messageType,
			Delivery: adaptercommon.OutboundDeliveryResult{Delivered: delivered, Attempts: 1, Error: map[bool]string{true: "", false: "temporary failure"}[delivered]},
		}); err != nil {
			t.Fatal(err)
		}
	}

	record(2, "content_delta", false)
	record(4, "message_complete", true)
	state, err := server.loadIMOutboundDeliveryState(imOutboundStateKey(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if state.CommittedEventID != 0 || state.LastFailedEventID != 2 || state.Status != "failed" {
		t.Fatalf("terminal skipped failed event: %#v", state)
	}
	server = New(Options{FileRoot: root})
	state, err = server.loadIMOutboundDeliveryState(imOutboundStateKey(sessionID))
	if err != nil || state.CommittedEventID != 0 || state.LastFailedEventID != 2 || state.Status != "failed" {
		t.Fatalf("restart lost failed-event fence: %#v err=%v", state, err)
	}

	record(3, "content_delta", true)
	state, err = server.loadIMOutboundDeliveryState(imOutboundStateKey(sessionID))
	if err != nil || state.LastFailedEventID != 2 {
		t.Fatalf("newer success cleared older failure: %#v err=%v", state, err)
	}
	record(2, "content_delta", true)
	state, err = server.loadIMOutboundDeliveryState(imOutboundStateKey(sessionID))
	if err != nil || state.LastFailedEventID != 0 || state.CommittedEventID != 0 {
		t.Fatalf("retried event did not clear its failure: %#v err=%v", state, err)
	}
	record(4, "message_complete", true)
	state, err = server.loadIMOutboundDeliveryState(imOutboundStateKey(sessionID))
	if err != nil || state.CommittedEventID != 4 || state.LastFailedEventID != 0 || state.Status != "committed" {
		t.Fatalf("contiguous retry did not commit terminal: %#v err=%v", state, err)
	}
	record(2, "content_delta", false)
	state, err = server.loadIMOutboundDeliveryState(imOutboundStateKey(sessionID))
	if err != nil || state.CommittedEventID != 4 || state.LastFailedEventID != 0 || state.Status != "committed" {
		t.Fatalf("stale failure regressed committed cursor: %#v err=%v", state, err)
	}
}

func TestIMOutboundRecoveryRequeuesBoundedFailedTranscriptIntent(t *testing.T) {
	workspace, repo, db := newTranscriptWebFixture(t)
	root := t.TempDir()
	initialSink := &recordingSessionOutboundSink{}
	initial := New(Options{Workspace: workspace, Transcript: repo, FileRoot: root, IMOutbound: initialSink})
	if err := initial.stopTranscriptWebDelivery(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := initial.pairingStore.Allow("wechat", "sender-recover", "recovery user"); err != nil {
		t.Fatal(err)
	}
	sessionID, _, err := initial.recordInboundLiveSession(inboundLiveSessionRecord{
		Platform: "wechat", ChatID: "failed-recovery", SenderID: "sender-recover", Text: "recover failed intent",
		MessageID: "message-recover", ContextToken: "ctx-failed-recovery", ClientMessageID: "client-recover", OwnerUserID: secretstore.DefaultUserID,
	})
	if err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetStreamBySession(context.Background(), secretstore.DefaultUserID, sessionID)
	if err != nil || !found {
		t.Fatalf("stream found=%t err=%v", found, err)
	}
	runner, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-recover", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !runner.Claimed {
		t.Fatalf("runner=%#v err=%v", runner, err)
	}
	event, _, err := repo.AppendRunnerEvent(context.Background(), transcriptstore.AppendEventInput{
		Claim: runner.Claim, ClientMessageID: "recover-delta", Type: "content_delta",
		Source: transcriptstore.EventSourcePayload, PayloadJSON: []byte(`{"text":"retry me"}`), Destinations: []string{sessionID},
	})
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= transcriptIMMaxAttempts; attempt++ {
		if err := initial.drainTranscriptIMRoute(context.Background(), stream.OwnerID, sessionID); err != nil {
			t.Fatal(err)
		}
		result := deliveredTranscriptIMResult(initial, sessionID, event.PublicationSeq)
		result.Delivery = adaptercommon.OutboundDeliveryResult{Delivered: false, Attempts: attempt, Error: "temporary route failure"}
		if err := initial.RecordIMOutboundResult(result); err != nil {
			t.Fatal(err)
		}
		if attempt < transcriptIMMaxAttempts {
			if _, err := db.Exec(`UPDATE transcript_delivery_intents SET next_attempt_at=NULL
				WHERE stream_uid=? AND publication_seq=? AND destination=?`, stream.UID, event.PublicationSeq, sessionID); err != nil {
				t.Fatal(err)
			}
		}
	}
	intent, err := repo.GetDeliveryIntent(context.Background(), stream.OwnerID, stream.UID, event.PublicationSeq, sessionID, 1)
	if err != nil || intent.Status != "failed" || intent.AttemptCount != transcriptIMMaxAttempts {
		t.Fatalf("failed intent=%#v err=%v", intent, err)
	}
	if _, err := initial.eventJournal.Append(sessionID, eventjournal.Message{
		"type": "content_delta", "text": "legacy projection must not replay",
	}, eventjournal.Metadata{}); err != nil {
		t.Fatal(err)
	}
	restartedSink := &recordingSessionOutboundSink{}
	restarted := New(Options{Workspace: workspace, Transcript: repo, FileRoot: root, IMOutbound: restartedSink})
	if err := restarted.stopTranscriptWebDelivery(context.Background()); err != nil {
		t.Fatal(err)
	}
	report, err := restarted.RecoverIMOutbound(context.Background())
	if err != nil || report.Restored != 1 {
		t.Fatalf("recovery report=%#v err=%v", report, err)
	}
	events := restartedSink.eventSnapshot()
	if len(events) != 1 || events[0].EventID != event.PublicationSeq {
		t.Fatalf("recovered events=%#v", events)
	}
	intent, err = repo.GetDeliveryIntent(context.Background(), stream.OwnerID, stream.UID, event.PublicationSeq, sessionID, 1)
	if err != nil || intent.Status != "inflight" || intent.AttemptCount != transcriptIMMaxAttempts+1 {
		t.Fatalf("recovered intent=%#v err=%v", intent, err)
	}
}

func TestIMOutboundRecoveryReusesLegacyStreamAndAdmitsStagedProjection(t *testing.T) {
	workspace, repo, db := newTranscriptWebFixture(t)
	root := t.TempDir()
	initial := New(Options{Workspace: workspace, Transcript: repo, FileRoot: root, IMOutbound: &recordingSessionOutboundSink{}})
	ownerID := "legacy-owner"
	if _, err := initial.pairingStore.AllowForOwner("wechat", "legacy-sender", "legacy owner", ownerID); err != nil {
		t.Fatal(err)
	}
	sessionID := imLiveSessionID("wechat", "legacy-chat")
	legacyHash := sha256.Sum256([]byte(ownerID + "\x00" + sessionID))
	legacyUID := "im-" + hex.EncodeToString(legacyHash[:16])
	if _, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: legacyUID, OwnerID: ownerID, ExternalID: sessionID, SessionID: sessionID,
		Kind: transcriptstore.StreamKindStandalone, Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	record := inboundLiveSessionRecord{
		Platform: "wechat", ChatID: "legacy-chat", MessageID: "legacy-message", SenderID: "legacy-sender",
		Text: "recover staged legacy stream", ContextToken: "ctx-legacy-recovery", ClientMessageID: "legacy-client", OwnerUserID: ownerID,
		TargetType: "chat", TargetID: "legacy-chat",
	}
	_, _, _, payload, err := inboundTranscriptPayload(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.StageUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: legacyUID, OwnerID: ownerID, ClientMessageID: record.ClientMessageID, PayloadJSON: payload,
	}); err != nil {
		t.Fatal(err)
	}
	binding := adaptercommon.IMSessionBinding{
		SessionID: sessionID, Platform: "wechat", ChatID: record.ChatID, SenderID: record.SenderID,
		MessageID: record.MessageID, ContextToken: "ctx-legacy-recovery", TargetType: record.TargetType, TargetID: record.TargetID, OwnerUserID: ownerID,
	}
	if err := initial.persistIMOutboundBindingState(binding); err != nil {
		t.Fatal(err)
	}

	restartedSink := &recordingSessionOutboundSink{}
	restarted := New(Options{Workspace: workspace, Transcript: repo, FileRoot: root, IMOutbound: restartedSink})
	report, err := restarted.RecoverIMOutbound(context.Background())
	if err != nil || report.Restored != 1 || report.Skipped != 0 {
		t.Fatalf("recovery report=%#v err=%v", report, err)
	}
	stream, found, err := repo.GetStreamBySession(context.Background(), ownerID, sessionID)
	if err != nil || !found || stream.UID != legacyUID || stream.InputRevision != 1 {
		t.Fatalf("legacy stream=%#v found=%t err=%v", stream, found, err)
	}
	var staged, admitted, streamCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='user_message_staged'`, legacyUID).Scan(&staged); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='user_message'`, legacyUID).Scan(&admitted); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_streams WHERE session_id=?`, sessionID).Scan(&streamCount); err != nil {
		t.Fatal(err)
	}
	if staged != 0 || admitted != 1 || streamCount != 1 {
		t.Fatalf("staged=%d admitted=%d streams=%d", staged, admitted, streamCount)
	}
	entries, err := restarted.eventJournal.ReadAllStrict(sessionID)
	if err != nil || len(entries) != 1 || entries[0].ClientMessageID != record.ClientMessageID {
		t.Fatalf("journal=%#v err=%v", entries, err)
	}
	if _, _, err := restarted.recordInboundLiveSession(record); err != nil {
		t.Fatalf("idempotent legacy inbound: %v", err)
	}
	foreign := record
	foreign.OwnerUserID = "foreign-owner"
	if _, _, err := restarted.recordInboundLiveSession(foreign); !errors.Is(err, transcriptstore.ErrOwnerMismatch) {
		t.Fatalf("foreign owner error=%v", err)
	}
}

func assertIMRouteTokenEncrypted(t *testing.T, root string, token string) {
	t.Helper()
	for _, path := range []string{
		filepath.Join(root, "runtime-state.sqlite"),
		filepath.Join(root, "secrets", "vault.enc"),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if bytes.Contains(raw, []byte(token)) {
			t.Fatalf("%s contains plaintext IM route token", path)
		}
	}
}

func assertInternalIMRouteSecretHidden(t *testing.T, srv *Server, sessionID string) {
	t.Helper()
	id := internalIMRouteSecretID(sessionID, secretstore.DefaultUserID)
	request := httptest.NewRequest(http.MethodGet, "/api/go/secrets", nil)
	response := httptest.NewRecorder()
	srv.handleSecrets(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), id) || strings.Contains(response.Body.String(), internalIMRouteSecretProvider) {
		t.Fatalf("public secret list exposed internal route: status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodDelete, "/api/go/secrets/"+id, nil)
	response = httptest.NewRecorder()
	srv.handleSecret(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("internal route secret delete status=%d body=%s", response.Code, response.Body.String())
	}
	if _, found, err := srv.secretStore.ResolveForUser(id, secretstore.DefaultUserID); err != nil || !found {
		t.Fatalf("internal route secret changed through public API: found=%t err=%v", found, err)
	}
}
