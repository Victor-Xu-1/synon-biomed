package compute

import (
	"testing"
)

func TestNormalizeBYOCEgressPolicy(t *testing.T) {
	policy, err := NormalizeBYOCEgressPolicy(map[string]any{
		"mode": "allowlist", "mirror": true,
		"additional": []any{" API.EXAMPLE.COM ", "*.data.example.com", "api.example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	additional := policy["additional"].([]string)
	if policy["mode"] != "allowlist" || policy["mirror"] != true ||
		len(additional) != 2 || additional[0] != "api.example.com" || additional[1] != "*.data.example.com" {
		t.Fatalf("policy=%#v", policy)
	}
	for _, invalid := range []map[string]any{
		{"mode": "open"},
		{"mode": "blocked", "mirror": true},
		{"mode": "allowlist", "mirror": "yes", "additional": []any{}},
		{"mode": "allowlist", "mirror": false, "additional": []any{"https://api.example.com"}},
		{"mode": "allowlist", "mirror": false, "additional": []any{"localhost"}},
	} {
		if _, err := NormalizeBYOCEgressPolicy(invalid); err == nil {
			t.Fatalf("invalid policy accepted: %#v", invalid)
		}
	}
}

func TestValidateBYOCNames(t *testing.T) {
	for _, name := range []string{"synonbiomed-default", "team.compute_1"} {
		if err := ValidateBYOCAppName(name); err != nil {
			t.Fatalf("app %q: %v", name, err)
		}
	}
	for _, name := range []string{"Bad", "-bad", ""} {
		if err := ValidateBYOCAppName(name); err == nil {
			t.Fatalf("invalid app accepted: %q", name)
		}
	}
	for _, name := range []string{"main", "Research-1", ""} {
		if err := ValidateModalEnvironment(name); err != nil {
			t.Fatalf("environment %q: %v", name, err)
		}
	}
	for _, name := range []string{"-bad", "bad-", "contains space"} {
		if err := ValidateModalEnvironment(name); err == nil {
			t.Fatalf("invalid environment accepted: %q", name)
		}
	}
}
