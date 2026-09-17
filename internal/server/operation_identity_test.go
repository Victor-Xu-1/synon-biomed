package server

import "testing"

func TestStableServerOperationIDIgnoresTransportMetadata(t *testing.T) {
	first, err := stableServerOperationID("environment", "owner\x00root", stableAuthorityInput(map[string]any{
		"mode": "create", "name": "analysis", "human_description": "first", "background": false,
		"packages": []any{"numpy"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	second, err := stableServerOperationID("environment", "owner\x00root", stableAuthorityInput(map[string]any{
		"mode": "create", "name": "analysis", "human_description": "rewritten", "background": true,
		"call_id": "new-call", "packages": []any{"numpy"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || first != second {
		t.Fatalf("transport metadata changed operation identity: first=%q second=%q", first, second)
	}
	changed, err := stableServerOperationID("environment", "owner\x00root", stableAuthorityInput(map[string]any{
		"mode": "create", "name": "analysis", "packages": []any{"scipy"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatalf("authority change reused operation identity: %q", changed)
	}
	described, err := stableServerOperationID("environment", "owner\x00root", stableAuthorityInput(map[string]any{
		"mode": "create", "name": "analysis", "packages": []any{"numpy"}, "description": "material scientific variant",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if described == first {
		t.Fatalf("material description was discarded from operation identity: %q", described)
	}
}
