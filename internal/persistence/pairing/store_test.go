package pairing

import (
	"path/filepath"
	"testing"
)

func TestStorePersistsPairingAllowAndRevoke(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pairing.json")
	store := New(path)

	user, err := store.Allow("wechat", "12345", "Victor")
	if err != nil {
		t.Fatalf("Allow() error = %v", err)
	}
	if user.Platform != "wechat" || user.UserID != "12345" || user.DisplayName != "Victor" || user.PairedAt.IsZero() {
		t.Fatalf("allowed user = %+v", user)
	}
	if paired, err := store.IsPaired("wechat", "12345"); err != nil || !paired {
		t.Fatalf("IsPaired() = %v %v", paired, err)
	}

	reloaded := New(path)
	users, err := reloaded.List("wechat")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(users) != 1 || users[0].UserID != "12345" {
		t.Fatalf("reloaded users = %+v", users)
	}

	revoked, err := reloaded.Revoke("wechat", "12345")
	if err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if !revoked {
		t.Fatal("Revoke() should report true for an existing paired user")
	}
	if paired, err := reloaded.IsPaired("wechat", "12345"); err != nil || paired {
		t.Fatalf("IsPaired() after revoke = %v %v", paired, err)
	}
}

func TestStorePersistsImmutableOwnerAuthority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pairing.json")
	store := New(path)
	user, err := store.AllowForOwner("wechat", "12345", "Victor", "owner-a")
	if err != nil || user.OwnerUserID != "owner-a" {
		t.Fatalf("user=%#v err=%v", user, err)
	}
	if _, err := store.AllowForOwner("wechat", "12345", "Victor", "owner-b"); err == nil {
		t.Fatal("pairing owner was reassigned")
	}
	user, found, err := New(path).Get("wechat", "12345")
	if err != nil || !found || user.OwnerUserID != "owner-a" {
		t.Fatalf("reopened user=%#v found=%t err=%v", user, found, err)
	}
}
