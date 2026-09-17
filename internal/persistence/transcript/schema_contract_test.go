package transcript

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestHistoryClassificationSchemaDoesNotInventClaudeSemanticCaps(t *testing.T) {
	ddl := strings.ToLower(strings.Join(HistoryClassificationV27Statements(), "\n"))
	for _, forbidden := range []string{
		"candidate_count <= 256",
		"length(tool_use_id) <= 512",
		"length(state_json) <= 65536",
		"length(evidence_json) <= 262144",
	} {
		if strings.Contains(ddl, forbidden) {
			t.Fatalf("history classification schema contains an unsupported semantic cap %q", forbidden)
		}
	}
}

func TestHistoryBackfillSchemaDoesNotInventClaudeSemanticCaps(t *testing.T) {
	ddl := strings.ToLower(strings.Join(HistoryBackfillV29Statements(), "\n"))
	for _, forbidden := range []string{
		"candidate_count <=", "length(tool_use_id) <=", "length(prompt_json) <=",
		"length(pending_json) <=", "length(result_json) <=", "legacy_ordinal <=",
	} {
		if strings.Contains(ddl, forbidden) {
			t.Fatalf("history backfill schema contains an unsupported semantic cap %q", forbidden)
		}
	}
}

func TestSchemaContractInstallsWithoutMigrationNumberAndSupportsRepository(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "contract.db"))+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(transcriptExternalAuthorityFixture); err != nil {
		t.Fatal(err)
	}
	statements := SchemaContractStatements()
	if SchemaContractID != "synon.transcript.v23" || ArtifactAssociationContractID != "synon.transcript.artifacts.v24" ||
		BranchLineageContractID != "synon.transcript.branches.v25" ||
		HistoryClassificationContractID != "synon.transcript.history-classification.v27" ||
		HistoryBackfillContractID != "synon.transcript.history-backfill.v29" ||
		HistoryCutoverContractID != "synon.transcript.history-cutover.v30" ||
		HistoryActivationContractID != "synon.transcript.history-activation.v31" ||
		HistoryPayloadGenesisContractID != "synon.transcript.payload-genesis.v32" ||
		HistoryOrdinaryCutoverContractID != "synon.transcript.history-ordinary-cutover.v33" ||
		TypedHistoryBootstrapContractID != "synon.transcript.typed-history-bootstrap.v35" ||
		WebReadModelContractID != "synon.transcript.web-read-model.v38" ||
		len(statements) != 128 || SchemaContractSHA256() != "ae46c62640d78f79166d3977a4bd87fdcd904fbded6f4b4ef61d8cc28a1695cb" {
		t.Fatalf("contract id=%q statements=%d sha=%q", SchemaContractID, len(statements), SchemaContractSHA256())
	}
	for index, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("statement %d: %v", index, err)
		}
	}
	statements[0] = "mutated by caller"
	if SchemaContractStatements()[0] == statements[0] {
		t.Fatal("schema contract statements were not defensively copied")
	}
	repo := NewRepository(db)
	if _, err := repo.CreateStream(context.Background(), CreateStreamInput{
		UID: "stream-contract", OwnerID: "owner-a", ExternalID: "standalone-contract",
		Kind: StreamKindStandalone, Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: "stream-contract", OwnerID: "owner-a", ClientMessageID: "user-1",
		PayloadJSON: []byte(`{"text":"work"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append created=%t err=%v", created, err)
	}
	claim, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-contract", OwnerID: "owner-a", RunnerID: "runner-a",
		TTL: time.Minute, ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
}

func TestSchemaContractFencesCrossAttemptEventsAndCascadesStreamAuthority(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	now := time.Date(2026, 7, 22, 22, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	seedTranscriptInput(t, repo, "stream-schema-fence", "owner-a")
	first, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: "stream-schema-fence", OwnerID: "owner-a", RunnerID: "runner-a", TTL: time.Minute,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !first.Claimed {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	firstEvent, created, err := repo.AppendRunnerEvent(context.Background(), AppendEventInput{
		Claim: first.Claim, ClientMessageID: "assistant-1", Type: "assistant_message", Source: EventSourcePayload,
		PayloadJSON: []byte(`{"text":"first"}`), Destinations: []string{"ws"},
	})
	if err != nil || !created {
		t.Fatalf("first event=%#v created=%t err=%v", firstEvent, created, err)
	}
	var baseBranchID string
	if err := db.QueryRow(`SELECT active_branch_id FROM transcript_branch_state WHERE stream_uid=?`, first.Claim.StreamUID).Scan(&baseBranchID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO transcript_branches(
			stream_uid,branch_id,parent_branch_id,fork_event_id,fork_point,kind,
			client_mutation_id,request_sha256,source_message_id,created_at,updated_at
		) VALUES(?, 'br_1234abcd', ?, ?, 1, 'edit', 'mutation-child', ?, 'message-source', ?, ?)`,
		first.Claim.StreamUID, baseBranchID, firstEvent.EventID, make([]byte, 32), now, now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO transcript_branch_events(stream_uid,branch_id,ordinal,event_id)
		VALUES(?, 'br_1234abcd', 1, ?)`, first.Claim.StreamUID, firstEvent.EventID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		UPDATE transcript_branch_state SET active_branch_id='br_1234abcd',generation=2,updated_at=?
		WHERE stream_uid=?`, now, first.Claim.StreamUID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM transcript_branches WHERE stream_uid=? AND branch_id=?`, first.Claim.StreamUID, baseBranchID); err == nil {
		t.Fatal("parent branch deletion succeeded while child lineage remained")
	}
	now = now.Add(2 * time.Minute)
	if _, created, err := repo.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: first.Claim.StreamUID, OwnerID: first.Claim.OwnerID, ClientMessageID: "schema-new-task",
		PayloadJSON: []byte(`{"text":"new task"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("new task input created=%t err=%v", created, err)
	}
	second, err := repo.ClaimRunner(context.Background(), ClaimRunnerInput{
		StreamUID: first.Claim.StreamUID, OwnerID: first.Claim.OwnerID, RunnerID: "runner-b", TTL: time.Minute,
		ResumeSource: ResumeSourceFresh,
	})
	if err != nil || !second.Claimed || second.Claim.Attempt != 2 {
		t.Fatalf("second claim=%#v err=%v", second, err)
	}
	statements := []struct {
		name string
		sql  string
		args []any
	}{
		{
			name: "checkpoint",
			sql: `INSERT INTO transcript_runner_checkpoints(
				stream_uid,checkpoint_sequence,runner_attempt,event_id,phase,resumable,created_at
			) VALUES(?,?,?,?, 'executing',1,?)`,
			args: []any{first.Claim.StreamUID, 1, second.Claim.Attempt, firstEvent.EventID, now},
		},
		{
			name: "receipt",
			sql: `INSERT INTO transcript_runner_receipts(stream_uid,attempt,event_id,status,finished_at)
				VALUES(?,?,?,'failed',?)`,
			args: []any{first.Claim.StreamUID, second.Claim.Attempt, firstEvent.EventID, now},
		},
		{
			name: "artifact reference",
			sql: `INSERT INTO transcript_artifact_refs(
				stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,availability,created_at
			) VALUES(?,?,?,0,'artifact-a','version-a1','cited','available',?)`,
			args: []any{first.Claim.StreamUID, second.Claim.Attempt, firstEvent.EventID, now},
		},
	}
	for _, statement := range statements {
		t.Run(statement.name, func(t *testing.T) {
			if _, err := db.Exec(statement.sql, statement.args...); err == nil {
				t.Fatal("cross-attempt authority was accepted")
			}
		})
	}
	if _, err := db.Exec(`DELETE FROM transcript_streams WHERE stream_uid=?`, first.Claim.StreamUID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{
		"transcript_runner_attempts", "transcript_events", "transcript_delivery_routes", "transcript_delivery_intents",
		"transcript_branches", "transcript_branch_state", "transcript_branch_events",
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("table %s count=%d err=%v", table, count, err)
		}
	}
}
