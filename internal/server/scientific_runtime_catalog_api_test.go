package server

import "testing"

func TestScientificRuntimeCatalogPublishesRegisteredPackages(t *testing.T) {
	s := &Server{}
	options := s.scientificRuntimeWarmupSelectionPayload(nil, false)["options"].([]map[string]any)
	definitions := scientificRuntimeWarmupDefinitions()
	if len(options) != len(definitions) {
		t.Fatal("catalog missing entries")
	}
	for index, definition := range definitions {
		request, _, err := definition.BuildRequest()
		if err != nil {
			t.Fatalf("%s: %v", definition.ID, err)
		}
		packages := options[index]["packages"].([]map[string]string)
		if len(packages) == 0 || len(packages) != len(request.Packages) {
			t.Fatalf("%s: incomplete packages", definition.ID)
		}
		for i, pkg := range request.Packages {
			if packages[i]["spec"] != pkg.Spec || packages[i]["manager"] != string(pkg.Manager) {
				t.Fatalf("%s: package identity mismatch", definition.ID)
			}
		}
	}
}
