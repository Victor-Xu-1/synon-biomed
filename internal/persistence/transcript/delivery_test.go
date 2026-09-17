package transcript

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestRepositoryDeliveryClaimsPreservePerStreamOrderAndSessionParallelism(t *testing.T) {
	repo, _, _ := newTranscriptRepository(t)
	now := time.Date(2026, 7, 22, 16, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	seedDeliveryStream(t, repo, "stream-a", "owner-a", "im:route-a", 2)
	seedDeliveryStream(t, repo, "stream-b", "owner-a", "im:route-a", 1)
	seedDeliveryStream(t, repo, "stream-c", "owner-b", "im:route-a", 1)

	first := mustClaimDelivery(t, repo, ClaimDeliveryInput{
		OwnerID: "owner-a", Destination: "im:route-a", WorkerID: "worker-1", TTL: time.Minute,
	})
	if first.StreamUID != "stream-a" || first.PublicationSeq != 1 || first.AttemptCount != 1 || first.Event.ClientMessageID != "user-1" {
		t.Fatalf("first claim=%#v", first)
	}
	second := mustClaimDelivery(t, repo, ClaimDeliveryInput{
		OwnerID: "owner-a", Destination: "im:route-a", WorkerID: "worker-2", TTL: time.Minute,
	})
	if second.StreamUID != "stream-b" || second.PublicationSeq != 1 {
		t.Fatalf("parallel claim=%#v", second)
	}
	if result, err := repo.AcknowledgeDelivery(context.Background(), AcknowledgeDeliveryInput{Claim: first}); err != nil || !result.Applied || result.Status != "delivered" {
		t.Fatalf("ack=%#v err=%v", result, err)
	}
	if result, err := repo.AcknowledgeDelivery(context.Background(), AcknowledgeDeliveryInput{Claim: first}); err != nil || result.Applied || result.Status != "delivered" {
		t.Fatalf("idempotent ack=%#v err=%v", result, err)
	}
	third := mustClaimDelivery(t, repo, ClaimDeliveryInput{
		OwnerID: "owner-a", Destination: "im:route-a", WorkerID: "worker-3", TTL: time.Minute,
	})
	if third.StreamUID != "stream-a" || third.PublicationSeq != 2 || third.Event.ClientMessageID != "user-2" {
		t.Fatalf("ordered claim=%#v", third)
	}
	if result, err := repo.ClaimNextDelivery(context.Background(), ClaimDeliveryInput{
		OwnerID: "owner-b", Destination: "im:route-a", WorkerID: "worker-b", TTL: time.Minute,
	}); err != nil || !result.Claimed || result.Claim.StreamUID != "stream-c" {
		t.Fatalf("owner-b claim=%#v err=%v", result, err)
	}
}

func TestRepositoryWebDeliveryPrioritizesNewestStreamWithoutReorderingIt(t *testing.T) {
	repo, _, _ := newTranscriptRepository(t)
	now := time.Date(2026, 8, 16, 13, 30, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	seedDeliveryStream(t, repo, "stream-web-history", "owner-web", "ws", 3)
	now = now.Add(time.Minute)
	seedDeliveryStream(t, repo, "stream-web-active", "owner-web", "ws", 2)

	first := mustClaimDelivery(t, repo, ClaimDeliveryInput{
		OwnerID: "owner-web", Destination: "ws", WorkerID: "worker-1", TTL: time.Minute,
	})
	if first.StreamUID != "stream-web-active" || first.PublicationSeq != 1 {
		t.Fatalf("first Web claim=%#v", first)
	}
	if result, err := repo.AcknowledgeDelivery(context.Background(), AcknowledgeDeliveryInput{Claim: first}); err != nil || !result.Applied {
		t.Fatalf("first Web ack=%#v err=%v", result, err)
	}
	second := mustClaimDelivery(t, repo, ClaimDeliveryInput{
		OwnerID: "owner-web", Destination: "ws", WorkerID: "worker-2", TTL: time.Minute,
	})
	if second.StreamUID != "stream-web-active" || second.PublicationSeq != 2 {
		t.Fatalf("second Web claim=%#v", second)
	}
	if result, err := repo.AcknowledgeDelivery(context.Background(), AcknowledgeDeliveryInput{Claim: second}); err != nil || !result.Applied {
		t.Fatalf("second Web ack=%#v err=%v", result, err)
	}
	third := mustClaimDelivery(t, repo, ClaimDeliveryInput{
		OwnerID: "owner-web", Destination: "ws", WorkerID: "worker-3", TTL: time.Minute,
	})
	if third.StreamUID != "stream-web-history" || third.PublicationSeq != 1 {
		t.Fatalf("historical Web claim after active stream=%#v", third)
	}
}

func TestRepositoryDeliveryLeaseRetryAndRestartFenceStaleWorkers(t *testing.T) {
	repo, db, dsn := newTranscriptRepository(t)
	now := time.Date(2026, 7, 22, 17, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	seedDeliveryStream(t, repo, "stream-retry", "owner-a", "im:route-a", 2)

	first := mustClaimDelivery(t, repo, ClaimDeliveryInput{
		OwnerID: "owner-a", Destination: "im:route-a", WorkerID: "worker-1", TTL: time.Minute,
	})
	var digest string
	if err := db.QueryRow(`SELECT hex(claim_token_sha256) FROM transcript_delivery_intents
		WHERE stream_uid='stream-retry' AND publication_seq=1 AND destination='im:route-a'`).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if digest == "" || digest == first.ClaimToken {
		t.Fatalf("delivery token was not stored as a digest: %q", digest)
	}

	now = now.Add(2 * time.Minute)
	reopenedDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedDB.Close()
	reopened := NewRepository(reopenedDB)
	reopened.now = func() time.Time { return now }
	second := mustClaimDelivery(t, reopened, ClaimDeliveryInput{
		OwnerID: "owner-a", Destination: "im:route-a", WorkerID: "worker-2", TTL: time.Minute,
	})
	if second.AttemptCount != 2 || second.ClaimToken == first.ClaimToken {
		t.Fatalf("reclaimed delivery=%#v", second)
	}
	if _, err := reopened.AcknowledgeDelivery(context.Background(), AcknowledgeDeliveryInput{Claim: first}); !errors.Is(err, ErrDeliveryClaimStale) {
		t.Fatalf("old acknowledgement error=%v", err)
	}
	failed, err := reopened.FailDelivery(context.Background(), FailDeliveryInput{
		Claim: second, ErrorCode: "provider_timeout", RetryAfter: time.Minute, MaxAttempts: 3,
	})
	if err != nil || !failed.Applied || failed.Status != "pending" {
		t.Fatalf("retry transition=%#v err=%v", failed, err)
	}
	if result, err := reopened.ClaimNextDelivery(context.Background(), ClaimDeliveryInput{
		OwnerID: "owner-a", Destination: "im:route-a", WorkerID: "worker-3", TTL: time.Minute,
	}); err != nil || result.Claimed {
		t.Fatalf("early retry claim=%#v err=%v", result, err)
	}
	now = now.Add(time.Minute)
	third := mustClaimDelivery(t, reopened, ClaimDeliveryInput{
		OwnerID: "owner-a", Destination: "im:route-a", WorkerID: "worker-3", TTL: time.Minute,
	})
	if third.AttemptCount != 3 {
		t.Fatalf("third claim=%#v", third)
	}
	terminal, err := reopened.FailDelivery(context.Background(), FailDeliveryInput{
		Claim: third, ErrorCode: "provider_rejected", MaxAttempts: 3,
	})
	if err != nil || !terminal.Applied || terminal.Status != "failed" {
		t.Fatalf("terminal transition=%#v err=%v", terminal, err)
	}
	if result, err := reopened.FailDelivery(context.Background(), FailDeliveryInput{
		Claim: third, ErrorCode: "provider_rejected", MaxAttempts: 3,
	}); err != nil || result.Applied || result.Status != "failed" {
		t.Fatalf("idempotent terminal transition=%#v err=%v", result, err)
	}
	if _, err := reopened.FailDelivery(context.Background(), FailDeliveryInput{
		Claim: third, ErrorCode: "different_error", MaxAttempts: 3,
	}); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("conflicting terminal transition error=%v", err)
	}
	if result, err := reopened.ClaimNextDelivery(context.Background(), ClaimDeliveryInput{
		OwnerID: "owner-a", Destination: "im:route-a", WorkerID: "worker-4", TTL: time.Minute,
	}); err != nil || result.Claimed {
		t.Fatalf("blocked successor claim=%#v err=%v", result, err)
	}
	intent, err := reopened.GetDeliveryIntent(context.Background(), "owner-a", "stream-retry", 1, "im:route-a", 1)
	if err != nil || intent.Status != "failed" || intent.AttemptCount != 3 || intent.LastErrorCode != "provider_rejected" ||
		intent.LastRetryAfter != 0 || intent.LastMaxAttempts != 3 {
		t.Fatalf("intent=%#v err=%v", intent, err)
	}
}

func TestRepositoryRecoverFailedDeliveryIntentsIsOwnerScopedAuditedAndBounded(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	now := time.Date(2026, 7, 23, 2, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	seedDeliveryStream(t, repo, "stream-recover", "owner-a", "im:route-a", 2)

	first := mustClaimDelivery(t, repo, ClaimDeliveryInput{
		OwnerID: "owner-a", Destination: "im:route-a", WorkerID: "worker-1", TTL: time.Minute,
	})
	failed, err := repo.FailDelivery(context.Background(), FailDeliveryInput{
		Claim: first, ErrorCode: "provider_timeout", RetryAfter: 3 * time.Second, MaxAttempts: 1,
	})
	if err != nil || !failed.Applied || failed.Status != "failed" {
		t.Fatalf("fail=%#v err=%v", failed, err)
	}
	if recovered, err := repo.RecoverFailedDeliveryIntents(context.Background(), "owner-b", "im:route-a", 10); err != nil || recovered != 0 {
		t.Fatalf("foreign recovery=%d err=%v", recovered, err)
	}
	if recovered, err := repo.RecoverFailedDeliveryIntents(context.Background(), "owner-a", "im:route-a", 10); err != nil || recovered != 1 {
		t.Fatalf("recovery=%d err=%v", recovered, err)
	}
	intent, err := repo.GetDeliveryIntent(context.Background(), "owner-a", "stream-recover", 1, "im:route-a", 1)
	if err != nil || intent.Status != "pending" || intent.AttemptCount != 1 || intent.LastErrorCode != "provider_timeout" ||
		intent.LastRetryAfter != 3*time.Second || intent.LastMaxAttempts != 1 || intent.WorkerID != "" || intent.LeaseExpiresAt != nil {
		t.Fatalf("recovered intent=%#v err=%v", intent, err)
	}
	if _, err := repo.AcknowledgeDelivery(context.Background(), AcknowledgeDeliveryInput{Claim: first}); !errors.Is(err, ErrDeliveryClaimStale) {
		t.Fatalf("stale acknowledgement error=%v", err)
	}
	second := mustClaimDelivery(t, repo, ClaimDeliveryInput{
		OwnerID: "owner-a", Destination: "im:route-a", WorkerID: "worker-2", TTL: time.Minute,
	})
	if second.AttemptCount != 2 || second.PublicationSeq != 1 {
		t.Fatalf("recovered claim=%#v", second)
	}
	if result, err := repo.AcknowledgeDelivery(context.Background(), AcknowledgeDeliveryInput{Claim: second}); err != nil || !result.Applied {
		t.Fatalf("ack=%#v err=%v", result, err)
	}
	third := mustClaimDelivery(t, repo, ClaimDeliveryInput{
		OwnerID: "owner-a", Destination: "im:route-a", WorkerID: "worker-3", TTL: time.Minute,
	})
	if third.PublicationSeq != 2 {
		t.Fatalf("successor claim=%#v", third)
	}

	seedDeliveryStream(t, repo, "stream-recovery-cap", "owner-a", "im:route-a", 1)
	if _, err := db.Exec(`UPDATE transcript_delivery_intents
		SET status='failed',attempt_count=?,last_error_code='permanent_failure',last_max_attempts=5
		WHERE stream_uid='stream-recovery-cap'`, maxDeliveryAttempts); err != nil {
		t.Fatal(err)
	}
	if recovered, err := repo.RecoverFailedDeliveryIntents(context.Background(), "owner-a", "im:route-a", 10); err != nil || recovered != 0 {
		t.Fatalf("bounded recovery=%d err=%v", recovered, err)
	}
}

func TestRepositoryRecoverRetryableWebProjectionDeliveryIntentsIsOneShotAndSelective(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	seedDeliveryStream(t, repo, "stream-auto-stale", "owner-stale", "ws", 1)
	seedDeliveryStream(t, repo, "stream-auto-legacy", "owner-legacy", "ws", 1)
	seedDeliveryStream(t, repo, "stream-auto-mapping", "owner-mapping", "ws", 1)

	fail := func(ownerID, streamUID, errorCode string) DeliveryClaim {
		t.Helper()
		claim := mustClaimDelivery(t, repo, ClaimDeliveryInput{
			OwnerID: ownerID, Destination: "ws", WorkerID: "failure-fixture", TTL: time.Minute,
		})
		if claim.StreamUID != streamUID {
			t.Fatalf("claim stream=%q want=%q", claim.StreamUID, streamUID)
		}
		result, err := repo.FailDelivery(context.Background(), FailDeliveryInput{
			Claim: claim, ErrorCode: errorCode, MaxAttempts: 1,
		})
		if err != nil || result.Status != "failed" {
			t.Fatalf("fail stream=%q result=%#v err=%v", streamUID, result, err)
		}
		return claim
	}
	fail("owner-legacy", "stream-auto-legacy", "projection_failed")
	fail("owner-mapping", "stream-auto-mapping", "mapping_unresolved")
	fail("owner-stale", "stream-auto-stale", "projection_stale")

	if recovered, err := repo.RecoverRetryableWebProjectionDeliveryIntents(context.Background(), 10); err != nil || recovered != 0 {
		t.Fatalf("recovery before projection ready=%d err=%v", recovered, err)
	}
	if _, err := db.Exec(`UPDATE transcript_delivery_intents
		SET attempt_count=?,last_max_attempts=? WHERE stream_uid='stream-auto-stale'`,
		maxDeliveryAttempts, maxDeliveryAttempts); err != nil {
		t.Fatal(err)
	}
	markProjectionReady := func(streamUID string) {
		t.Helper()
		const zeroSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
		if _, err := db.Exec(`INSERT INTO transcript_web_projection_state(
			stream_uid,branch_id,branch_generation,projector_version,projection_revision,
			through_publication_seq,source_revision,message_count,visible_message_count,
			message_artifact_reference_count,projector_state_json,projector_state_sha256,
			source_chain_sha256,status,last_error_code,updated_at
		) SELECT state.stream_uid,state.active_branch_id,state.generation,?,1,
			head.through_publication_seq,head.source_revision,0,0,0,'{}',?,?,'ready','',?
			FROM transcript_branch_state state
			JOIN transcript_branch_heads head ON head.stream_uid=state.stream_uid
				AND head.branch_id=state.active_branch_id
			WHERE state.stream_uid=?`, TranscriptWebProjectorVersion, zeroSHA256, zeroSHA256,
			time.Now().UTC(), streamUID); err != nil {
			t.Fatal(err)
		}
	}
	markProjectionReady("stream-auto-stale")
	markProjectionReady("stream-auto-legacy")

	recovered, err := repo.RecoverRetryableWebProjectionDeliveryIntents(context.Background(), 10)
	if err != nil || recovered != 2 {
		t.Fatalf("recovered=%d err=%v", recovered, err)
	}
	for _, fixture := range []struct {
		ownerID, streamUID string
		attempt            int
	}{
		{ownerID: "owner-stale", streamUID: "stream-auto-stale", attempt: maxDeliveryAttempts},
		{ownerID: "owner-legacy", streamUID: "stream-auto-legacy", attempt: 1},
	} {
		intent, err := repo.GetDeliveryIntent(context.Background(), fixture.ownerID, fixture.streamUID, 1, "ws", 1)
		if err != nil || intent.Status != "pending" || intent.AttemptCount != fixture.attempt || intent.WorkerID != "" || intent.LeaseExpiresAt != nil {
			t.Fatalf("recovered intent stream=%q intent=%#v err=%v", fixture.streamUID, intent, err)
		}
	}
	mapping, err := repo.GetDeliveryIntent(context.Background(), "owner-mapping", "stream-auto-mapping", 1, "ws", 1)
	if err != nil || mapping.Status != "failed" {
		t.Fatalf("mapping intent=%#v err=%v", mapping, err)
	}

	retry := mustClaimDelivery(t, repo, ClaimDeliveryInput{
		OwnerID: "owner-stale", Destination: "ws", WorkerID: "retry-fixture", TTL: time.Minute,
	})
	if retry.StreamUID != "stream-auto-stale" || retry.AttemptCount != maxDeliveryAttempts+1 {
		t.Fatalf("retry claim=%#v", retry)
	}
	if result, err := repo.FailDelivery(context.Background(), FailDeliveryInput{
		Claim: retry, ErrorCode: "projection_stale", MaxAttempts: maxDeliveryAttempts,
	}); err != nil || result.Status != "failed" {
		t.Fatalf("retry failure=%#v err=%v", result, err)
	}
	if recovered, err := repo.RecoverRetryableWebProjectionDeliveryIntents(context.Background(), 10); err != nil || recovered != 0 {
		t.Fatalf("second automatic recovery=%d err=%v", recovered, err)
	}
}

func TestRepositoryDeliveryHeartbeatExtendsOnlyTheLiveClaim(t *testing.T) {
	repo, _, dsn := newTranscriptRepository(t)
	now := time.Date(2026, 7, 23, 1, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	seedDeliveryStream(t, repo, "stream-delivery-heartbeat", "owner-a", "im:route-a", 1)
	claim := mustClaimDelivery(t, repo, ClaimDeliveryInput{
		OwnerID: "owner-a", Destination: "im:route-a", WorkerID: "worker-a", TTL: time.Minute,
	})
	now = now.Add(30 * time.Second)
	renewed, err := repo.HeartbeatDelivery(context.Background(), HeartbeatDeliveryInput{Claim: claim, TTL: 2 * time.Minute})
	if err != nil || !renewed.Renewed || !renewed.ExpiresAt.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("heartbeat=%#v err=%v", renewed, err)
	}

	reopenedDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedDB.Close()
	reopened := NewRepository(reopenedDB)
	reopened.now = func() time.Time { return now }
	tampered := claim
	tampered.ClaimToken = "not-the-delivery-token"
	if result, err := reopened.HeartbeatDelivery(context.Background(), HeartbeatDeliveryInput{Claim: tampered, TTL: time.Minute}); !errors.Is(err, ErrDeliveryClaimStale) || result.Renewed {
		t.Fatalf("tampered heartbeat=%#v err=%v", result, err)
	}
	if _, err := reopened.RevokeDeliveryRoute(context.Background(), claim.OwnerID, claim.StreamUID, claim.Destination, claim.RouteGeneration); err != nil {
		t.Fatal(err)
	}
	if result, err := reopened.HeartbeatDelivery(context.Background(), HeartbeatDeliveryInput{Claim: claim, TTL: time.Minute}); !errors.Is(err, ErrDeliveryClaimStale) || result.Renewed {
		t.Fatalf("revoked heartbeat=%#v err=%v", result, err)
	}
}

func TestRepositoryDeliveryDestinationsAckAndRevokeIndependently(t *testing.T) {
	repo, _, _ := newTranscriptRepository(t)
	now := time.Date(2026, 7, 22, 18, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	if _, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "stream-destinations", OwnerID: "owner-a", ExternalID: "external", Kind: StreamKindStandalone, Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if route, activated, err := repo.ActivateDeliveryRoute(context.Background(), "owner-a", "stream-destinations", "im:route-a"); err != nil || !activated || route.Generation != 1 {
		t.Fatalf("activate route=%#v activated=%t err=%v", route, activated, err)
	}
	if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: "stream-destinations", OwnerID: "owner-a", ClientMessageID: "user-1",
		PayloadJSON: []byte(`{"text":"deliver"}`), Destinations: []string{"ws", "im:route-a"},
	}); err != nil || !created {
		t.Fatalf("append created=%t err=%v", created, err)
	}
	ws := mustClaimDelivery(t, repo, ClaimDeliveryInput{OwnerID: "owner-a", Destination: "ws", WorkerID: "ws-1", TTL: time.Minute})
	im := mustClaimDelivery(t, repo, ClaimDeliveryInput{OwnerID: "owner-a", Destination: "im:route-a", WorkerID: "im-1", TTL: time.Minute})
	if _, err := repo.AcknowledgeDelivery(context.Background(), AcknowledgeDeliveryInput{Claim: ws}); err != nil {
		t.Fatal(err)
	}
	if affected, err := repo.RevokeDeliveryRoute(context.Background(), "owner-a", "stream-destinations", "im:route-a", 1); err != nil || affected != 1 {
		t.Fatalf("revoke affected=%d err=%v", affected, err)
	}
	if _, err := repo.AcknowledgeDelivery(context.Background(), AcknowledgeDeliveryInput{Claim: im}); !errors.Is(err, ErrDeliveryClaimStale) {
		t.Fatalf("revoked acknowledgement error=%v", err)
	}
	if _, _, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: "stream-destinations", OwnerID: "owner-a", ClientMessageID: "user-after-revoke",
		PayloadJSON: []byte(`{"text":"must not deliver"}`), Destinations: []string{"im:route-a"},
	}); !errors.Is(err, ErrDeliveryRouteInactive) {
		t.Fatalf("append after route revoke error=%v", err)
	}
	route, activated, err := repo.ActivateDeliveryRoute(context.Background(), "owner-a", "stream-destinations", "im:route-a")
	if err != nil || !activated || route.Generation != 2 || route.Status != "active" {
		t.Fatalf("reactivate route=%#v activated=%t err=%v", route, activated, err)
	}
	if _, activated, err := repo.ActivateDeliveryRoute(context.Background(), "owner-a", "stream-destinations", "im:route-a"); err != nil || activated {
		t.Fatalf("idempotent activation activated=%t err=%v", activated, err)
	}
	if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: "stream-destinations", OwnerID: "owner-a", ClientMessageID: "user-after-reactivate",
		PayloadJSON: []byte(`{"text":"deliver generation two"}`), Destinations: []string{"im:route-a"},
	}); err != nil || !created {
		t.Fatalf("append after reactivation created=%t err=%v", created, err)
	}
	if intent, err := repo.GetDeliveryIntent(context.Background(), "owner-a", "stream-destinations", 2, "im:route-a", 2); err != nil || intent.RouteGeneration != 2 {
		t.Fatalf("generation two intent=%#v err=%v", intent, err)
	}
	if _, err := repo.RevokeDeliveryRoute(context.Background(), "owner-a", "stream-destinations", "im:route-a", 1); !errors.Is(err, ErrDeliveryRouteStale) {
		t.Fatalf("stale revoke error=%v", err)
	}
	wsIntent, err := repo.GetDeliveryIntent(context.Background(), "owner-a", "stream-destinations", 1, "ws", 1)
	if err != nil || wsIntent.Status != "delivered" {
		t.Fatalf("ws intent=%#v err=%v", wsIntent, err)
	}
	imIntent, err := repo.GetDeliveryIntent(context.Background(), "owner-a", "stream-destinations", 1, "im:route-a", 1)
	if err != nil || imIntent.Status != "revoked" {
		t.Fatalf("im intent=%#v err=%v", imIntent, err)
	}
	if _, err := repo.RevokeDeliveryRoute(context.Background(), "owner-b", "stream-destinations", "im:route-a", 1); !errors.Is(err, ErrOwnerMismatch) {
		t.Fatalf("foreign revoke error=%v", err)
	}
}

func TestRepositoryDeliveryClaimHasOneWinnerPerStream(t *testing.T) {
	repo, _, dsn := newTranscriptRepository(t)
	seedDeliveryStream(t, repo, "stream-race", "owner-a", "im:route-a", 2)

	const workers = 16
	start := make(chan struct{})
	results := make(chan ClaimDeliveryResult, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			db, err := sql.Open("sqlite", dsn)
			if err != nil {
				errs <- err
				return
			}
			defer db.Close()
			<-start
			result, err := NewRepository(db).ClaimNextDelivery(context.Background(), ClaimDeliveryInput{
				OwnerID: "owner-a", Destination: "im:route-a", WorkerID: "worker-" + strconv.Itoa(index), TTL: time.Minute,
			})
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}(index)
	}
	close(start)
	group.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	winners := 0
	for result := range results {
		if result.Claimed {
			winners++
			if result.Claim.StreamUID != "stream-race" || result.Claim.PublicationSeq != 1 {
				t.Fatalf("winner=%#v", result)
			}
		}
	}
	if winners != 1 {
		t.Fatalf("delivery claim winners=%d, want 1", winners)
	}
}

func TestRepositoryDeliveryRouteGenerationSurvivesRestartAndFencesOwners(t *testing.T) {
	repo, _, dsn := newTranscriptRepository(t)
	if _, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "stream-route-restart", OwnerID: "owner-a", ExternalID: "external-route-restart",
		Kind: StreamKindStandalone, Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	first, activated, err := repo.ActivateDeliveryRoute(context.Background(), "owner-a", "stream-route-restart", "im:route-a")
	if err != nil || !activated || first.Generation != 1 {
		t.Fatalf("first route=%#v activated=%t err=%v", first, activated, err)
	}
	if _, err := repo.RevokeDeliveryRoute(context.Background(), "owner-a", first.StreamUID, first.Destination, first.Generation); err != nil {
		t.Fatal(err)
	}

	reopenedDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedDB.Close()
	reopened := NewRepository(reopenedDB)
	if _, _, err := reopened.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: first.StreamUID, OwnerID: "owner-a", ClientMessageID: "blocked-after-restart",
		PayloadJSON: []byte(`{"text":"blocked"}`), Destinations: []string{first.Destination},
	}); !errors.Is(err, ErrDeliveryRouteInactive) {
		t.Fatalf("append on restarted revoked route error=%v", err)
	}
	if _, _, err := reopened.ActivateDeliveryRoute(context.Background(), "owner-b", first.StreamUID, first.Destination); !errors.Is(err, ErrOwnerMismatch) {
		t.Fatalf("foreign activation error=%v", err)
	}
	second, activated, err := reopened.ActivateDeliveryRoute(context.Background(), "owner-a", first.StreamUID, first.Destination)
	if err != nil || !activated || second.Generation != 2 {
		t.Fatalf("second route=%#v activated=%t err=%v", second, activated, err)
	}
	if _, created, err := reopened.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: first.StreamUID, OwnerID: "owner-a", ClientMessageID: "allowed-after-restart",
		PayloadJSON: []byte(`{"text":"allowed"}`), Destinations: []string{first.Destination},
	}); err != nil || !created {
		t.Fatalf("append generation two created=%t err=%v", created, err)
	}
	if intent, err := reopened.GetDeliveryIntent(context.Background(), "owner-a", first.StreamUID, 1, first.Destination, 2); err != nil || intent.RouteGeneration != 2 {
		t.Fatalf("restarted intent=%#v err=%v", intent, err)
	}
}

func TestRepositoryCanonicalWebRouteCannotBeRevoked(t *testing.T) {
	repo, _, _ := newTranscriptRepository(t)
	if _, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "stream-web-route", OwnerID: "owner-a", ExternalID: "external-web-route",
		Kind: StreamKindStandalone, Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RevokeDeliveryRoute(context.Background(), "owner-a", "stream-web-route", "ws", 1); !errors.Is(err, ErrDeliveryRouteImmutable) {
		t.Fatalf("web route revoke error=%v", err)
	}
	if _, err := repo.RevokeDeliveryRoute(context.Background(), "owner-b", "stream-web-route", "ws", 1); !errors.Is(err, ErrOwnerMismatch) {
		t.Fatalf("foreign web route revoke error=%v", err)
	}
	if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: "stream-web-route", OwnerID: "owner-a", ClientMessageID: "user-1",
		PayloadJSON: []byte(`{"text":"still deliver"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append after rejected revoke created=%t err=%v", created, err)
	}
	if intent, err := repo.GetDeliveryIntent(context.Background(), "owner-a", "stream-web-route", 1, "ws", 1); err != nil || intent.Status != "pending" {
		t.Fatalf("web intent=%#v err=%v", intent, err)
	}
}

func TestRepositorySettlementSeparatesRunnableFromExplicitFailureRecovery(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	seedDeliveryStream(t, repo, "stream-unsettled", "owner-a", "ws", 2)
	assertUnsettled := func(ownerID string, want bool) {
		t.Helper()
		got, err := repo.HasUnsettledDelivery(context.Background(), ownerID, "ws")
		if err != nil || got != want {
			t.Fatalf("unsettled owner=%q got=%t want=%t err=%v", ownerID, got, want, err)
		}
	}
	assertUnsettled("owner-a", true)
	assertUnsettled("owner-b", false)

	claim := mustClaimDelivery(t, repo, ClaimDeliveryInput{
		OwnerID: "owner-a", Destination: "ws", WorkerID: "worker", TTL: time.Minute,
	})
	assertUnsettled("owner-a", true)
	if result, err := repo.FailDelivery(context.Background(), FailDeliveryInput{
		Claim: claim, ErrorCode: "projection_failed", MaxAttempts: 1,
	}); err != nil || result.Status != "failed" {
		t.Fatalf("fail=%#v err=%v", result, err)
	}
	assertUnsettled("owner-a", true)
	state, err := repo.DeliverySettlementState(context.Background(), "owner-a", "ws")
	if err != nil || state.Runnable || !state.ExplicitlyRecoverable || state.Poisoned {
		t.Fatalf("failed settlement=%#v err=%v", state, err)
	}
	if recovered, err := repo.RecoverFailedDeliveryIntents(context.Background(), "owner-a", "ws", 1); err != nil || recovered != 1 {
		t.Fatalf("recovered=%d err=%v", recovered, err)
	}
	claim = mustClaimDelivery(t, repo, ClaimDeliveryInput{
		OwnerID: "owner-a", Destination: "ws", WorkerID: "worker-retry", TTL: time.Minute,
	})
	if _, err := repo.AcknowledgeDelivery(context.Background(), AcknowledgeDeliveryInput{Claim: claim}); err != nil {
		t.Fatal(err)
	}
	claim = mustClaimDelivery(t, repo, ClaimDeliveryInput{
		OwnerID: "owner-a", Destination: "ws", WorkerID: "worker-follower", TTL: time.Minute,
	})
	if claim.PublicationSeq != 2 {
		t.Fatalf("follower publication=%d want=2", claim.PublicationSeq)
	}
	if _, err := repo.AcknowledgeDelivery(context.Background(), AcknowledgeDeliveryInput{Claim: claim}); err != nil {
		t.Fatal(err)
	}
	assertUnsettled("owner-a", false)

	seedDeliveryStream(t, repo, "stream-poisoned", "owner-a", "ws", 1)
	if _, err := db.Exec(`UPDATE transcript_delivery_intents SET status='failed',attempt_count=?,last_max_attempts=?
		WHERE stream_uid='stream-poisoned'`, maxDeliveryAttempts, maxDeliveryAttempts); err != nil {
		t.Fatal(err)
	}
	state, err = repo.DeliverySettlementState(context.Background(), "owner-a", "ws")
	if err != nil || state.Runnable || state.ExplicitlyRecoverable || !state.Poisoned {
		t.Fatalf("poisoned settlement=%#v err=%v", state, err)
	}
	assertUnsettled("owner-a", true)
	if recovered, err := repo.RecoverFailedDeliveryIntents(context.Background(), "owner-a", "ws", 10); err != nil || recovered != 0 {
		t.Fatalf("poison recovery=%d err=%v", recovered, err)
	}
}

func seedDeliveryStream(t *testing.T, repo *Repository, streamUID, ownerID, destination string, events int) {
	t.Helper()
	if _, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: streamUID, OwnerID: ownerID, ExternalID: "external-" + streamUID, Kind: StreamKindStandalone, Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if destination != "ws" {
		if _, _, err := repo.ActivateDeliveryRoute(context.Background(), ownerID, streamUID, destination); err != nil {
			t.Fatal(err)
		}
	}
	for index := 1; index <= events; index++ {
		if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
			StreamUID: streamUID, OwnerID: ownerID, ClientMessageID: "user-" + strconv.Itoa(index),
			PayloadJSON: []byte(`{"text":"deliver"}`), Destinations: []string{destination},
		}); err != nil || !created {
			t.Fatalf("append %d created=%t err=%v", index, created, err)
		}
	}
}

func mustClaimDelivery(t *testing.T, repo *Repository, input ClaimDeliveryInput) DeliveryClaim {
	t.Helper()
	result, err := repo.ClaimNextDelivery(context.Background(), input)
	if err != nil || !result.Claimed {
		t.Fatalf("claim=%#v err=%v", result, err)
	}
	return result.Claim
}
