package workspace

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestContactEmailDecisionRedactsSupersededAddresses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	first, err := store.RecordContactEmailDecision(RecordContactEmailDecisionInput{
		UserID: "user-a", Decision: ContactEmailDecisionAllowed, Email: "first@example.test",
		NoticeVersion: "notice-v1", NoticeText: "notice",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.RecordContactEmailDecision(RecordContactEmailDecisionInput{
		UserID: "user-a", Decision: ContactEmailDecisionAllowed, Email: "second@example.test",
		NoticeVersion: "notice-v1", NoticeText: "notice",
	})
	if err != nil {
		t.Fatal(err)
	}
	latest, found, err := store.LatestContactEmailDecision("user-a")
	if err != nil || !found || latest.ID != second.ID || latest.Email != "second@example.test" {
		t.Fatalf("latest = %#v, found=%v, err=%v", latest, found, err)
	}
	var firstEmail sql.NullString
	if err := store.db.QueryRow(`SELECT email FROM contact_email_decisions WHERE id = ?`, first.ID).Scan(&firstEmail); err != nil {
		t.Fatal(err)
	}
	if firstEmail.Valid {
		t.Fatalf("superseded address remained in history: %q", firstEmail.String)
	}

	revoked, err := store.RecordContactEmailDecision(RecordContactEmailDecisionInput{
		UserID: "user-a", Decision: ContactEmailDecisionRevoked,
		NoticeVersion: "notice-v1", NoticeText: "notice",
	})
	if err != nil {
		t.Fatal(err)
	}
	latest, found, err = store.LatestContactEmailDecision("user-a")
	if err != nil || !found || latest.ID != revoked.ID || latest.Decision != ContactEmailDecisionRevoked || latest.Email != "" {
		t.Fatalf("revoked latest = %#v, found=%v, err=%v", latest, found, err)
	}
	var retained int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM contact_email_decisions WHERE email IS NOT NULL`).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if retained != 0 {
		t.Fatalf("retained contact email rows = %d", retained)
	}
	if _, err := store.RecordContactEmailDecision(RecordContactEmailDecisionInput{
		UserID: "user-a", Decision: ContactEmailDecisionAllowed, Email: "bad\n@example.test",
		NoticeVersion: "notice-v1", NoticeText: "notice",
	}); err == nil {
		t.Fatal("control character in contact email was accepted")
	}
}

func TestArchivedContactEmailDecisionsActivateAndRedactHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open(sqliteDriver, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE legacy_v11_contact_email_decisions (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL, decision TEXT NOT NULL,
			email TEXT, notice_version TEXT NOT NULL, notice_text TEXT NOT NULL,
			created_at INTEGER NOT NULL
		);
		INSERT INTO legacy_v11_contact_email_decisions VALUES
			('contact-old', 'legacy-user', 'allowed', 'old@example.test', 'v1', 'notice', 1700000000000),
			('contact-new', 'legacy-user', 'allowed', 'current@example.test', 'v1', 'notice', 1700000001000)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	latest, found, err := store.LatestContactEmailDecision("local")
	if err != nil || !found || latest.ID != "contact-new" || latest.Email != "current@example.test" {
		t.Fatalf("activated latest = %#v, found=%v, err=%v", latest, found, err)
	}
	var oldEmail sql.NullString
	if err := store.db.QueryRow(`SELECT email FROM contact_email_decisions WHERE id = 'contact-old'`).Scan(&oldEmail); err != nil {
		t.Fatal(err)
	}
	if oldEmail.Valid {
		t.Fatalf("archived historical email was not redacted: %q", oldEmail.String)
	}
	var activated int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM contact_email_decisions WHERE user_id = 'local'`).Scan(&activated); err != nil {
		t.Fatal(err)
	}
	if activated != 2 {
		t.Fatalf("activated decisions = %d", activated)
	}
}
