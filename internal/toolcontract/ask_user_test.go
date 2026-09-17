package toolcontract

import "testing"

func TestCanonicalAskUserUsesExactAliases(t *testing.T) {
	for _, name := range []string{AskUser, "AskUserQuestion", "ask_user_question"} {
		if got, ok := CanonicalAskUser(name); !ok || got != AskUser {
			t.Fatalf("CanonicalAskUser(%q) = %q, %t", name, got, ok)
		}
	}
	for _, name := range []string{
		"", " ask_user", "ask_user ", "\task_user\n", "ASK_USER",
		"Askuserquestion", "ASK_USER_QUESTION", "ask-user", "\ufeffask_user", "aſk_user",
	} {
		if got, ok := CanonicalAskUser(name); ok || got != "" {
			t.Fatalf("CanonicalAskUser(%q) = %q, %t; want rejected", name, got, ok)
		}
	}
}
