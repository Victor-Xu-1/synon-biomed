package common

import "testing"

func TestIsPairedUsesAllowedUsersAndPairedUsers(t *testing.T) {
	closed := map[string]PlatformPairingConfig{
		"wechat": {
			AllowedUsers: []string{},
			PairedUsers:  []PairedUser{},
		},
	}
	if IsPaired("wechat", "12345", closed) {
		t.Fatal("empty allowed users and paired users should stay closed")
	}

	config := map[string]PlatformPairingConfig{
		"wechat": {
			AllowedUsers: []string{"111", "222"},
			PairedUsers: []PairedUser{
				{UserID: "444", DisplayName: "Paired"},
			},
		},
	}
	for _, userID := range []string{"111", "222", "444"} {
		if !IsPaired("wechat", userID, config) {
			t.Fatalf("%s should be paired", userID)
		}
	}
	if IsPaired("wechat", "333", config) {
		t.Fatal("333 should not be paired")
	}
}
