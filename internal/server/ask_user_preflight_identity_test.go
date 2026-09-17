package server

import "testing"

func TestAskUserOpaqueIdentityCaseIsolation(t *testing.T) {
	for _, pair := range [][2]string{
		{"https://host.example/EngineA", "https://host.example/enginea"},
		{"https://host.example/engine?revision=ReleaseA", "https://host.example/engine?revision=releasea"},
		{"registry.example/engine:ReleaseA", "registry.example/engine:releasea"},
		{"engine:v1RC", "engine:v1rc"},
		{"Engine v1Beta", "Engine v1beta"},
		{"/opt/EngineA", "/opt/enginea"},
	} {
		t.Run(pair[0], func(t *testing.T) {
			if askUserImplementationIdentityMatches(pair[0], pair[1]) {
				t.Fatal("case-sensitive identities matched")
			}
			resources := &askUserResourceProfile{CPU: "unresolved", Memory: "unresolved", GPU: "unresolved"}
			authorities := map[string]askUserEvidenceAuthority{
				"tool-call:upper": {Class: askUserEvidenceCompletedTool, Implementation: pair[0], Resources: resources, Ordinal: 2},
			}
			questions := []askUserQuestion{{Options: []askUserQuestionOption{{Label: "candidate", Metadata: map[string]any{
				"implementation": pair[1], "decision_evidence": []string{"tool-call:upper"},
			}}}}}
			got := normalizeAskUserEvidenceAuthorities(questions, authorities)[0].Options[0].Metadata
			if containsAskUserEvidenceReference(stringArrayValue(got["decision_evidence"]), "tool-call:upper") {
				t.Fatalf("explicit reference to another identity survived: %#v", got)
			}
			if reference, _ := askUserImplementationEvidenceAuthority(pair[1], authorities); reference != "" {
				t.Fatalf("wrong identity automatically bound: %s", reference)
			}
			authorities["tool-call:lower"] = askUserEvidenceAuthority{
				Class: askUserEvidenceCompletedTool, Implementation: pair[1], Resources: resources, Ordinal: 1,
			}
			if reference, _ := askUserImplementationEvidenceAuthority(pair[1], authorities); reference != "tool-call:lower" {
				t.Fatalf("newer wrong-case receipt displaced exact receipt: %s", reference)
			}
			available := askUserAvailablePreflightIdentities(authorities)
			if len(available) != 2 {
				t.Fatalf("distinct identities collapsed: %#v", available)
			}
			seen := map[string]bool{}
			for _, value := range available {
				seen[stringValue(mapValue(value)["implementation"])] = true
			}
			if !seen[pair[0]] || !seen[pair[1]] {
				t.Fatalf("available identity spelling changed: %#v", available)
			}
		})
	}
}

func TestAskUserDisplayNameCaseCompatibilityUsesSameDeduplicationRule(t *testing.T) {
	if !askUserImplementationIdentityMatches("Engine Alpha: first description", "engine alpha: another description") {
		t.Fatal("plain display names lost case compatibility")
	}
	resources := &askUserResourceProfile{CPU: "unresolved"}
	authorities := map[string]askUserEvidenceAuthority{
		"tool-call:old": {Class: askUserEvidenceCompletedTool, Implementation: "Engine Alpha", Resources: resources, Ordinal: 1},
		"tool-call:new": {Class: askUserEvidenceCompletedTool, Implementation: "engine alpha: latest description", Resources: resources, Ordinal: 2},
	}
	available := askUserAvailablePreflightIdentities(authorities)
	if len(available) != 1 || mapValue(available[0])["evidence_ref"] != "tool-call:new" {
		t.Fatalf("comparison and deduplication disagree for plain display names: %#v", available)
	}
}
