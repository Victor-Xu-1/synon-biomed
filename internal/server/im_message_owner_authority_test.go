package server

import (
	"context"
	"database/sql"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestIMMessageToolRequiresTrustedRunnerOwner(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	sink := &recordingSessionOutboundSink{}
	server := New(Options{
		Workspace: store, Transcript: repo, FileRoot: t.TempDir(), IMOutbound: sink,
		SynonLinkAuth: SynonLinkAuthOptions{Username: "operator", Password: "secret", UserID: "operator"},
	})
	if _, err := server.pairingStore.AllowForOwner("wechat", "sender-b", "owner B sender", "owner-b"); err != nil {
		t.Fatal(err)
	}
	ownerA := claimIMToolTestRunner(t, repo, "owner-a", "owner-a-runner")
	ownerB := claimIMToolTestRunner(t, repo, "owner-b", "owner-b-runner")
	input := map[string]any{
		"platform": "wechat", "chatId": "owner-chat", "messageId": "owner-message",
		"senderId": "sender-b", "contextToken": "ctx-owner-b", "text": "must stay with owner B",
	}

	assertUnauthorized := func(ctx context.Context) {
		t.Helper()
		if _, err := server.executeToolResult(ctx, "im_message", input); err == nil || err.Error() != "im_message caller owner is not authorized" {
			t.Fatalf("unauthorized error=%v", err)
		}
		assertIMMessageToolNoEffects(t, server, repo, db, sink, "im:wechat:owner-chat")
	}
	assertUnauthorized(withTranscriptRunnerChatRun(context.Background(), &sessionRunnerChatRun{Transcript: ownerA}))
	assertUnauthorized(context.Background())

	output, err := server.executeToolResult(
		withTranscriptRunnerChatRun(context.Background(), &sessionRunnerChatRun{Transcript: ownerB}),
		"im_message", input,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := output.(map[string]any)
	if !ok || result["deduplicated"] != false || result["sessionId"] != "im:wechat:owner-chat" {
		t.Fatalf("matching owner result=%#v", output)
	}
	if tasks, err := server.taskStore.List(); err != nil || len(tasks) != 1 {
		t.Fatalf("matching owner tasks=%#v err=%v", tasks, err)
	}

	localStore, localRepo, _ := newTranscriptWebFixture(t)
	local := New(Options{
		Workspace: localStore, Transcript: localRepo, FileRoot: t.TempDir(), IMOutbound: &recordingSessionOutboundSink{},
	})
	if _, err := local.pairingStore.Allow("wechat", "local-sender", "local sender"); err != nil {
		t.Fatal(err)
	}
	localOutput, err := local.executeToolResult(context.Background(), "im_message", map[string]any{
		"platform": "wechat", "chatId": "local-chat", "messageId": "local-message",
		"senderId": "local-sender", "contextToken": "ctx-local", "text": "local compatibility",
	})
	if err != nil {
		t.Fatal(err)
	}
	localResult, ok := localOutput.(map[string]any)
	if !ok || localResult["sessionId"] != "im:wechat:local-chat" || localResult["deduplicated"] != false {
		t.Fatalf("local compatibility result=%#v", localOutput)
	}
	if stream, found, err := localRepo.GetStreamBySession(context.Background(), "local", "im:wechat:local-chat"); err != nil || !found || stream.OwnerID != "local" {
		t.Fatalf("local compatibility stream=%#v found=%t err=%v", stream, found, err)
	}
}

func claimIMToolTestRunner(
	t *testing.T,
	repo *transcriptstore.Repository,
	ownerID, streamUID string,
) *transcriptRunnerAuthority {
	t.Helper()
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: streamUID, OwnerID: ownerID, ExternalID: streamUID, SessionID: streamUID,
		Kind: transcriptstore.StreamKindStandalone, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: ownerID, ClientMessageID: streamUID + "-input",
		PayloadJSON: []byte(`{"text":"authorize tool owner"}`),
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: ownerID, RunnerID: streamUID + "-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	return &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim}
}

func assertIMMessageToolNoEffects(
	t *testing.T,
	server *Server,
	repo *transcriptstore.Repository,
	db *sql.DB,
	sink *recordingSessionOutboundSink,
	sessionID string,
) {
	t.Helper()
	if tasks, err := server.taskStore.List(); err != nil || len(tasks) != 0 {
		t.Fatalf("unauthorized tasks=%#v err=%v", tasks, err)
	}
	if session, found, err := server.sessionStore.Get(sessionID); err != nil || found {
		t.Fatalf("unauthorized session=%#v found=%t err=%v", session, found, err)
	}
	if entries, err := server.eventJournal.ReadAllStrict(sessionID); err != nil || len(entries) != 0 {
		t.Fatalf("unauthorized journal=%#v err=%v", entries, err)
	}
	for _, ownerID := range []string{"owner-a", "owner-b"} {
		if stream, found, err := repo.GetStreamBySession(context.Background(), ownerID, sessionID); err != nil || found {
			t.Fatalf("unauthorized stream owner=%s stream=%#v found=%t err=%v", ownerID, stream, found, err)
		}
	}
	if route, found, err := server.runtimeStore.Get(imOutboundRouteNamespace, imOutboundStateKey(sessionID)); err != nil || found {
		t.Fatalf("unauthorized route=%#v found=%t err=%v", route, found, err)
	}
	if bindings := sink.bindingSnapshot(); len(bindings) != 0 {
		t.Fatalf("unauthorized bindings=%#v", bindings)
	}
	var deliveries int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_delivery_intents WHERE destination=?`, sessionID).Scan(&deliveries); err != nil || deliveries != 0 {
		t.Fatalf("unauthorized deliveries=%d err=%v", deliveries, err)
	}
	var attempts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_runner_attempts AS attempt
		JOIN transcript_streams AS stream ON stream.stream_uid=attempt.stream_uid
		WHERE stream.session_id=?`, sessionID).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("unauthorized runner attempts=%d err=%v", attempts, err)
	}
}
