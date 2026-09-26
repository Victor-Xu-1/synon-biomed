package server

import "testing"

func TestScientificRuntimeCatalogPublishesRegisteredPackages(t *testing.T) {
	s := &Server{}
	options := s.scientificRuntimeWarmupSelectionPayload(nil, false)["options"].([]map[string]any)
	definitions := scientificRuntimeWarmupDefinitions()
	if len(options) != len(definitions)+2 {
		t.Fatal("catalog missing entries")
	}
	byID := make(map[string]map[string]any, len(options))
	for _, option := range options {
		byID[option["id"].(string)] = option
	}
	for _, definition := range definitions {
		request, _, err := definition.BuildRequest()
		if err != nil {
			t.Fatalf("%s: %v", definition.ID, err)
		}
		packages := byID[definition.ID]["packages"].([]map[string]string)
		if len(packages) == 0 || len(packages) != len(request.Packages) {
			t.Fatalf("%s: incomplete packages", definition.ID)
		}
		for i, pkg := range request.Packages {
			if packages[i]["spec"] != pkg.Spec || packages[i]["manager"] != string(pkg.Manager) {
				t.Fatalf("%s: package identity mismatch", definition.ID)
			}
		}
	}
	for _, id := range []string{managedPythonScientificRuntimeID, managedRScientificRuntimeID} {
		option, found := byID[id]
		if !found || option["kind"] != "core" || option["required"] != true || option["selected"] != true {
			t.Fatalf("required runtime option=%#v", option)
		}
	}
}
