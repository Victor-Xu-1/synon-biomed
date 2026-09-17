package common

import "testing"

func TestParsePermitCallbackData(t *testing.T) {
	tests := []struct {
		name string
		data string
		want PermissionDecision
		ok   bool
	}{
		{name: "allow once", data: "permit:abcde:yes", want: PermissionDecision{RequestID: "abcde", Allowed: true}, ok: true},
		{name: "allow always", data: "permit:abcde:always", want: PermissionDecision{RequestID: "abcde", Allowed: true, Rule: "always"}, ok: true},
		{name: "deny", data: "permit:abcde:no", want: PermissionDecision{RequestID: "abcde", Allowed: false}, ok: true},
		{name: "other", data: "other:abcde:yes", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParsePermitCallbackData(tt.data)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if !ok {
				return
			}
			if got != tt.want {
				t.Fatalf("decision = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParsePermissionCommandShortcuts(t *testing.T) {
	pending := []string{"req-1"}

	got, ok := ParsePermissionCommand("允许一次", pending)
	if !ok || got.RequestID != "req-1" || !got.Allowed || got.Rule != "" {
		t.Fatalf("allow shortcut = %+v %v", got, ok)
	}

	got, ok = ParsePermissionCommand("/always req-2", nil)
	if !ok || got.RequestID != "req-2" || !got.Allowed || got.Rule != "always" {
		t.Fatalf("always command = %+v %v", got, ok)
	}

	got, ok = ParsePermissionCommand("deny", pending)
	if !ok || got.RequestID != "req-1" || got.Allowed {
		t.Fatalf("deny shortcut = %+v %v", got, ok)
	}
}
