package server

import (
	"testing"
	"time"
)

func TestWebAuthTransactionWrongCookieDoesNotConsumeValidTransaction(t *testing.T) {
	store := newWebAuthTransactionStore()
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }

	transaction, err := store.Create("wechat", "https://biomed.example/callback", "/#/", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Consume(transaction.State, "attacker-cookie"); err == nil {
		t.Fatal("invalid cookie was accepted")
	}
	if _, err := store.Consume(transaction.State, transaction.CookieToken); err != nil {
		t.Fatalf("valid transaction was consumed by an invalid callback: %v", err)
	}
	if _, err := store.Consume(transaction.State, transaction.CookieToken); err == nil {
		t.Fatal("transaction was reusable after a successful consume")
	}
}
