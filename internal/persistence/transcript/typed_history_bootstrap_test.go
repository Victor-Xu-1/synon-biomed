package transcript

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestTypedHistoryBootstrapPublishesOneVerifiedPayloadAuthority(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	prepareHistoryInventoryFixture(t, db)
	seedHistoryInventoryFrame(t, db, "owner-a", "project-a", "frame-rich-bootstrap", "completed")
	now := time.Now().UTC().Truncate(time.Microsecond)
	fixtures := []struct {
		id, eventType, payload string
	}{
		{"rich-tool", "assistant_message", `{"role":"assistant","content":[{"type":"text","text":"Inspecting."},{"type":"tool_use","id":"call-1","name":"read_file","input":{"path":"result.txt"}}]}`},
		{"rich-result", "user_message", `{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"42","is_error":false}]}`},
	}
	for index, fixture := range fixtures {
		if _, err := db.Exec(`INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
			VALUES(?,?,?,?,?,?)`, fixture.id, "frame-rich-bootstrap", index+1, fixture.eventType, fixture.payload,
			now.Add(time.Duration(index)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	report, err := repo.ReconcileNoStreamFrameHistories(context.Background(), ReconcileNoStreamFrameHistoriesInput{Limit: 10})
	if err != nil || report.PayloadCreated != 1 || report.Deferred != 0 || report.Census.PayloadActive != 1 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	var sourceRows, historyEvents, branchEvents, textBlocks, toolUses, toolResults int
	var sourceDigest, historyDigest, materializedDigest []byte
	if err := db.QueryRow(`SELECT source_row_count,history_event_count,branch_event_count,
		text_block_count,tool_use_count,tool_result_count,source_snapshot_sha256,history_sha256,
		materialized_sha256 FROM transcript_typed_history_bootstrap_receipts
		WHERE session_id='frame-rich-bootstrap' AND status='active'`).Scan(
		&sourceRows, &historyEvents, &branchEvents, &textBlocks, &toolUses, &toolResults,
		&sourceDigest, &historyDigest, &materializedDigest,
	); err != nil {
		t.Fatal(err)
	}
	if sourceRows != 2 || historyEvents != 2 || branchEvents != 2 || textBlocks != 1 ||
		toolUses != 1 || toolResults != 1 || len(sourceDigest) != 32 || len(historyDigest) != 32 ||
		len(materializedDigest) != 32 {
		t.Fatalf("receipt rows=%d events=%d branches=%d text=%d uses=%d results=%d digests=%d/%d/%d",
			sourceRows, historyEvents, branchEvents, textBlocks, toolUses, toolResults,
			len(sourceDigest), len(historyDigest), len(materializedDigest))
	}
	rows, err := db.Query(`SELECT event_id,publication_seq,client_message_id,event_type,payload_json,
		runner_attempt,frame_event_id FROM transcript_events WHERE stream_uid='frame:frame-rich-bootstrap'
		ORDER BY event_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for index, fixture := range fixtures {
		if !rows.Next() {
			t.Fatalf("missing event %d", index+1)
		}
		var eventID, publication int
		var clientID, eventType, payload string
		var attempt sql.NullInt64
		var frameEvent sql.NullString
		if err := rows.Scan(&eventID, &publication, &clientID, &eventType, &payload, &attempt, &frameEvent); err != nil {
			t.Fatal(err)
		}
		wantType := "history_assistant_message"
		if index == 1 {
			wantType = "history_user_message"
		}
		if eventID != index+1 || publication != index+1 || clientID != "payload-genesis:"+fixture.id ||
			eventType != wantType || payload != fixture.payload || attempt.Valid || frameEvent.Valid {
			t.Fatalf("event %d id=%d publication=%d client=%q type=%q payload=%q attempt=%#v frame=%#v",
				index, eventID, publication, clientID, eventType, payload, attempt, frameEvent)
		}
	}
	if rows.Next() {
		t.Fatal("bootstrap appended an extra event")
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM transcript_delivery_intents`,
		`SELECT COUNT(*) FROM transcript_runner_attempts WHERE stream_uid='frame:frame-rich-bootstrap'`,
		`SELECT COUNT(*) FROM transcript_artifact_refs WHERE stream_uid='frame:frame-rich-bootstrap'`,
	} {
		var count int
		if err := db.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("query=%s count=%d err=%v", query, count, err)
		}
	}
	if _, err := db.Exec(`UPDATE transcript_typed_history_bootstrap_receipts SET text_block_count=2
		WHERE session_id='frame-rich-bootstrap'`); err == nil {
		t.Fatal("typed bootstrap receipt was mutable")
	}
	if err := repo.ValidateContract(context.Background()); err != nil {
		t.Fatalf("contract: %v", err)
	}
}

func TestTypedHistoryBootstrapRejectsUnprovenShapesWithoutPartialAuthority(t *testing.T) {
	cases := []struct {
		name   string
		events []struct{ eventType, payload string }
	}{
		{name: "ask user", events: []struct{ eventType, payload string }{
			{"assistant_message", `{"role":"assistant","content":[{"type":"tool_use","id":"ask-1","name":"ask_user","input":{"questions":[]}}]}`},
			{"user_message", `{"role":"user","content":[{"type":"tool_result","tool_use_id":"ask-1","content":"yes"}]}`},
		}},
		{name: "ask user question alias", events: []struct{ eventType, payload string }{
			{"assistant_message", `{"role":"assistant","content":[{"type":"tool_use","id":"ask-1","name":"AskUserQuestion","input":{"questions":[]}}]}`},
			{"user_message", `{"role":"user","content":[{"type":"tool_result","tool_use_id":"ask-1","content":"yes"}]}`},
		}},
		{name: "ask user snake alias", events: []struct{ eventType, payload string }{
			{"assistant_message", `{"role":"assistant","content":[{"type":"tool_use","id":"ask-1","name":"ask_user_question","input":{"questions":[]}}]}`},
			{"user_message", `{"role":"user","content":[{"type":"tool_result","tool_use_id":"ask-1","content":"yes"}]}`},
		}},
		{name: "padded role", events: []struct{ eventType, payload string }{
			{"assistant_message", `{"role":" assistant ","content":[{"type":"tool_use","id":"call-1","name":"read_file","input":{}}]}`},
			{"user_message", `{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"x"}]}`},
		}},
		{name: "padded block type", events: []struct{ eventType, payload string }{
			{"assistant_message", `{"role":"assistant","content":[{"type":" tool_use ","id":"call-1","name":"read_file","input":{}}]}`},
			{"user_message", `{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"x"}]}`},
		}},
		{name: "padded use id", events: []struct{ eventType, payload string }{
			{"assistant_message", `{"role":"assistant","content":[{"type":"tool_use","id":" call-1 ","name":"read_file","input":{}}]}`},
			{"user_message", `{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"x"}]}`},
		}},
		{name: "padded result id", events: []struct{ eventType, payload string }{
			{"assistant_message", `{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"read_file","input":{}}]}`},
			{"user_message", `{"role":"user","content":[{"type":"tool_result","tool_use_id":" call-1 ","content":"x"}]}`},
		}},
		{name: "padded tool name", events: []struct{ eventType, payload string }{
			{"assistant_message", `{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":" read_file ","input":{}}]}`},
			{"user_message", `{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"x"}]}`},
		}},
		{name: "top level ask user alias", events: []struct{ eventType, payload string }{
			{"assistant_message", `{"role":"assistant","toolName":"AskUserQuestion","content":[{"type":"tool_use","id":"call-1","name":"read_file","input":{}}]}`},
			{"user_message", `{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"x"}]}`},
		}},
		{name: "model tool ask user alias", events: []struct{ eventType, payload string }{
			{"assistant_message", `{"role":"assistant","modelToolCalls":[{"name":"ask_user_question"}],"content":[{"type":"tool_use","id":"call-1","name":"read_file","input":{}}]}`},
			{"user_message", `{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"x"}]}`},
		}},
		{name: "orphan result", events: []struct{ eventType, payload string }{
			{"user_message", `{"role":"user","content":[{"type":"tool_result","tool_use_id":"missing","content":"x"}]}`},
		}},
		{name: "result before use", events: []struct{ eventType, payload string }{
			{"user_message", `{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"x"}]}`},
			{"assistant_message", `{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"read_file","input":{}}]}`},
		}},
		{name: "raw tool", events: []struct{ eventType, payload string }{
			{"tool_use", `{"id":"call-1","name":"read_file","input":{}}`},
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			repo, db, _ := newTranscriptRepository(t)
			prepareHistoryInventoryFixture(t, db)
			frameID := "frame-reject-" + stringsForTestID(test.name)
			seedHistoryInventoryFrame(t, db, "owner-a", "project-a", frameID, "completed")
			for index, event := range test.events {
				if _, err := db.Exec(`INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
					VALUES(?,?,?,?,?,?)`, frameID+"-"+string(rune('a'+index)), frameID, index+1,
					event.eventType, event.payload, time.Now().UTC().Add(time.Duration(index)*time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			report, err := repo.ReconcileNoStreamFrameHistories(context.Background(), ReconcileNoStreamFrameHistoriesInput{Limit: 10})
			if err != nil || report.Deferred != 1 || report.PayloadCreated != 0 {
				t.Fatalf("report=%#v err=%v", report, err)
			}
			for _, query := range []string{
				`SELECT COUNT(*) FROM transcript_streams WHERE session_id=?`,
				`SELECT COUNT(*) FROM transcript_payload_genesis_receipts WHERE session_id=?`,
				`SELECT COUNT(*) FROM transcript_typed_history_bootstrap_receipts WHERE session_id=?`,
				`SELECT COUNT(*) FROM transcript_frame_authority WHERE session_id=?`,
			} {
				var count int
				if err := db.QueryRow(query, frameID).Scan(&count); err != nil || count != 0 {
					t.Fatalf("query=%s count=%d err=%v", query, count, err)
				}
			}
		})
	}
}

func TestTypedHistoryBootstrapReceiptDeletionFollowsAuthorityLifecycle(t *testing.T) {
	for _, scope := range []string{"frame", "root", "project"} {
		t.Run(scope, func(t *testing.T) {
			repo, db, _ := newTranscriptRepository(t)
			prepareHistoryInventoryFixture(t, db)
			frameID := "frame-delete-" + scope
			seedHistoryInventoryFrame(t, db, "owner-a", "project-a", frameID, "completed")
			if _, err := db.Exec(`INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at) VALUES
				(?, ?, 1, 'assistant_message',
				'{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"read_file","input":{}}]}',CURRENT_TIMESTAMP),
				(?, ?, 2, 'user_message',
				'{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"ok"}]}',CURRENT_TIMESTAMP)`,
				frameID+"-tool", frameID, frameID+"-result", frameID); err != nil {
				t.Fatal(err)
			}
			if report, err := repo.ReconcileNoStreamFrameHistories(context.Background(), ReconcileNoStreamFrameHistoriesInput{Limit: 10}); err != nil || report.PayloadCreated != 1 {
				t.Fatalf("report=%#v err=%v", report, err)
			}
			if _, err := db.Exec(`DELETE FROM transcript_typed_history_bootstrap_receipts WHERE stream_uid=?`, "frame:"+frameID); err == nil {
				t.Fatal("active typed bootstrap receipt deletion succeeded")
			}
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			var deleted int64
			switch scope {
			case "frame":
				deleted, err = DeleteFrameStreamsTx(context.Background(), tx, "owner-a", "project-a", frameID)
			case "root":
				deleted, err = DeleteRootStreamsTx(context.Background(), tx, "owner-a", "project-a", frameID)
			case "project":
				deleted, err = DeleteProjectStreamsTx(context.Background(), tx, "owner-a", "project-a")
			}
			if err != nil || deleted != 1 {
				_ = tx.Rollback()
				t.Fatalf("deleted=%d err=%v", deleted, err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{
				"transcript_typed_history_bootstrap_receipts", "transcript_payload_genesis_receipts",
				"transcript_frame_authority", "transcript_streams",
			} {
				var count int
				if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE `+
					map[string]string{
						"transcript_typed_history_bootstrap_receipts": "stream_uid=?",
						"transcript_payload_genesis_receipts":         "stream_uid=?",
						"transcript_frame_authority":                  "active_stream_uid=?",
						"transcript_streams":                          "stream_uid=?",
					}[table], "frame:"+frameID).Scan(&count); err != nil || count != 0 {
					t.Fatalf("table=%s count=%d err=%v", table, count, err)
				}
			}
		})
	}
}

func TestTypedHistoryBootstrapRollsBackBeforeAuthorityPublication(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	prepareHistoryInventoryFixture(t, db)
	seedHistoryInventoryFrame(t, db, "owner-a", "project-a", "frame-bootstrap-rollback", "completed")
	if _, err := db.Exec(`INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at) VALUES
		('rollback-tool','frame-bootstrap-rollback',1,'assistant_message',
		'{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"read_file","input":{}}]}',CURRENT_TIMESTAMP),
		('rollback-result','frame-bootstrap-rollback',2,'user_message',
		'{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"ok"}]}',CURRENT_TIMESTAMP);
		CREATE TRIGGER fail_typed_bootstrap_receipt BEFORE INSERT ON transcript_typed_history_bootstrap_receipts
		BEGIN SELECT RAISE(ABORT,'injected typed bootstrap failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReconcileNoStreamFrameHistories(context.Background(), ReconcileNoStreamFrameHistoriesInput{Limit: 10}); err == nil {
		t.Fatal("injected typed bootstrap failure succeeded")
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM transcript_streams WHERE session_id='frame-bootstrap-rollback'`,
		`SELECT COUNT(*) FROM transcript_events WHERE stream_uid='frame:frame-bootstrap-rollback'`,
		`SELECT COUNT(*) FROM transcript_payload_genesis_receipts WHERE session_id='frame-bootstrap-rollback'`,
		`SELECT COUNT(*) FROM transcript_typed_history_bootstrap_receipts WHERE session_id='frame-bootstrap-rollback'`,
		`SELECT COUNT(*) FROM transcript_frame_authority WHERE session_id='frame-bootstrap-rollback'`,
	} {
		var count int
		if err := db.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("query=%s count=%d err=%v", query, count, err)
		}
	}
}

func stringsForTestID(value string) string {
	result := make([]byte, 0, len(value))
	for index := 0; index < len(value); index++ {
		if value[index] >= 'a' && value[index] <= 'z' {
			result = append(result, value[index])
		} else {
			result = append(result, '-')
		}
	}
	return string(result)
}
