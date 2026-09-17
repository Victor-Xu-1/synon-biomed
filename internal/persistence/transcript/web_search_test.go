package transcript

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSearchVisibleWebMessagesScopesToOwnerReadyActiveVisibleProjection(t *testing.T) {
	repository, database, _ := newTranscriptRepository(t)
	visible := seedTranscriptSearchFixture(t, repository, database, "owner-search", "visible", "ready", true,
		"Needle evidence is visible at 100% confidence.")
	seedTranscriptSearchFixture(t, repository, database, "owner-search", "hidden", "ready", false,
		"Needle evidence must remain hidden.")
	seedTranscriptSearchFixture(t, repository, database, "owner-search", "building", "building", true,
		"Needle evidence is not ready.")
	seedTranscriptSearchFixture(t, repository, database, "foreign-owner", "foreign", "ready", true,
		"Needle evidence belongs to another owner.")

	hits, err := repository.SearchVisibleWebMessages(context.Background(), SearchVisibleWebMessagesInput{
		OwnerID: "owner-search", Query: "needle", CandidateLimit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].RootFrameID != visible.rootFrameID ||
		hits[0].MessageID != "message-visible" {
		t.Fatalf("owner-scoped visible hits=%#v", hits)
	}

	escaped, err := repository.SearchVisibleWebMessages(context.Background(), SearchVisibleWebMessagesInput{
		OwnerID: "owner-search", Query: "100%", CandidateLimit: 20,
	})
	if err != nil || len(escaped) != 1 || escaped[0].RootFrameID != visible.rootFrameID {
		t.Fatalf("literal wildcard hits=%#v err=%v", escaped, err)
	}
	empty, err := repository.SearchVisibleWebMessages(context.Background(), SearchVisibleWebMessagesInput{
		OwnerID: "owner-search", Query: "   ", CandidateLimit: 20,
	})
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty query hits=%#v err=%v", empty, err)
	}
	assertVisibleWebMessageSearchQueryPlan(t, database)
}

func assertVisibleWebMessageSearchQueryPlan(t *testing.T, database *sql.DB) {
	t.Helper()
	rows, err := database.Query("EXPLAIN QUERY PLAN "+searchVisibleWebMessagesSQL, "owner-search", "%needle%", 20)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	ownerBounded := false
	messageIndexed := false
	details := make([]string, 0, 12)
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
		if strings.Contains(detail, "transcript_streams") && strings.Contains(detail, "owner_id=?") {
			ownerBounded = true
		}
		if strings.Contains(detail, "message") && strings.Contains(detail, "USING INDEX") {
			messageIndexed = true
		}
		if strings.Contains(detail, "SCAN message") {
			t.Fatalf("message search escaped the owner-bounded index path: %s", detail)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !ownerBounded || !messageIndexed {
		t.Fatalf("search query plan lacks bounded owner/message indexes: %v", details)
	}
}

func seedTranscriptSearchFixture(
	t *testing.T,
	repository *Repository,
	database *sql.DB,
	ownerID string,
	suffix string,
	status string,
	visible bool,
	text string,
) transcriptWebWorkFrameFixture {
	t.Helper()
	fixture := seedTranscriptWebWorkFrame(
		t, repository, database, ownerID, "session-"+suffix, "stream-"+suffix, 1,
	)
	if _, created, err := repository.AppendUserEvent(context.Background(), AppendUserEventInput{
		StreamUID: fixture.streamUID, OwnerID: fixture.ownerID, ClientMessageID: "event-" + suffix,
		PayloadJSON: []byte(fmt.Sprintf(`{"text":%q}`, text)), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append search fixture event created=%t err=%v", created, err)
	}
	visibleCount := 0
	if visible {
		visibleCount = 1
	}
	seedTranscriptWebWorkState(t, database, fixture, status, visibleCount)
	messageJSON := []byte(fmt.Sprintf(
		`{"id":"message-%s","msg_id":"message-%s","type":"text","created_at":1787557580781,"content":{"content":%q}}`,
		suffix, suffix, text,
	))
	digest := sha256.Sum256(messageJSON)
	visibleValue := 0
	var visibleIndex any
	if visible {
		visibleValue = 1
		visibleIndex = 0
	}
	if _, err := database.Exec(`INSERT INTO transcript_web_messages(
		stream_uid,branch_id,ordinal,message_id,client_message_id,visible,visible_index,
		message_json,message_sha256,first_publication_seq,last_publication_seq,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		fixture.streamUID, fixture.branchID, 1, "message-"+suffix, "client-"+suffix,
		visibleValue, visibleIndex, string(messageJSON), hex.EncodeToString(digest[:]), 1, 1,
		time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		t.Fatal(err)
	}
	return fixture
}
