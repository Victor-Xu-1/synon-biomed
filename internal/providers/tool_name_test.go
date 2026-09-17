package providers

import "testing"

func TestNormalizeModelToolNameCanonicalizesRetiredProgressAlias(t *testing.T) {
	name, err := normalizeModelToolName("update_step")
	if err != nil {
		t.Fatal(err)
	}
	if name != "update_step_status" {
		t.Fatalf("normalized tool name = %q", name)
	}
}
