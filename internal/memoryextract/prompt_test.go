package memoryextract

import (
	"fmt"
	"strings"
	"testing"

	"synon-go/internal/memorypolicy"
	workspace "synon-go/internal/persistence/workspace"
)

func TestWorkspaceForkedExtractPromptUsesExactManifestCategoriesAndSchema(t *testing.T) {
	rows := make([]ManifestRow, 0, 51)
	rows = append(rows,
		ManifestRow{ID: "mem_profile", Text: "# Profile\nStable <memory>preference</memory>", EntityKey: "profile"},
		ManifestRow{ID: "mem_project", Text: strings.Repeat("x", 130), EntityKey: "project:p", CategoryName: "Methods"},
	)
	for index := len(rows); index < 51; index++ {
		rows = append(rows, ManifestRow{ID: fmt.Sprintf("mem_%02d", index), Text: "fact", EntityKey: "project:p"})
	}
	prompt, err := BuildExtractPrompt(PromptOptions{
		Mode: "forked", MaxPerKind: 5, Manifest: rows,
		Categories:        []Category{{Name: "Methods", Guidance: "Stable method choices."}},
		FinalResponseText: "<memory>final</memory> response",
		ForkMarker:        "---fr-12345678---",
	})
	if err != nil {
		t.Fatal(err)
	}
	checks := []string{
		"Maximum entries in each operation array: 5.",
		"Produce changes using the append, replace, and remove fields.",
		"---fr-12345678---\nfinal response\n---fr-12345678---",
		"## User-defined categories\n\nThe user has defined these memory categories (name — when to use it):\n- Methods — Stable method choices.",
		"- [mem_profile · profile] Profile Stable preference",
		"- [mem_project · project:p · Methods] " + strings.Repeat("x", 119) + "…",
		"- …1 more not shown",
		"Respond with ONLY a JSON object matching this schema (no prose, no code fence, no tool call):",
		"Do not call any tool. If nothing qualifies, respond with `{}`.",
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Fatalf("prompt missing %q\n%s", check, prompt)
		}
	}
	if strings.Contains(prompt, "through one emit_memories tool call") {
		t.Fatal("forked prompt retained tool-call instruction")
	}
	if _, err := EmitSchema(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceManifestMutableIDsExcludeProfile(t *testing.T) {
	rows := ManifestRows([]workspace.Memory{
		{ID: "profile", Body: "user preference"},
		{ID: "project", Body: "project fact", SubjectProjectID: "p"},
		{ID: "artifact", Body: "artifact fact", SubjectArtifactID: "a"},
	})
	known := KnownMutableManifestIDs(rows)
	if _, exists := known["profile"]; exists {
		t.Fatal("profile row became extractor-mutable")
	}
	if _, exists := known["project"]; !exists {
		t.Fatal("project row missing from extractor mutable ids")
	}
	if _, exists := known["artifact"]; !exists {
		t.Fatal("artifact row missing from extractor mutable ids")
	}
}

func TestWorkspaceForkedExtractPromptCapsFinalResponseAtEightThousandUTF16Units(t *testing.T) {
	final := strings.Repeat("🙂", 5000)
	prompt, err := BuildExtractPrompt(PromptOptions{Mode: "forked", FinalResponseText: final, ForkMarker: "---fr-00000000---"})
	if err != nil {
		t.Fatal(err)
	}
	between := strings.Split(prompt, "---fr-00000000---\n")
	if len(between) < 2 {
		t.Fatalf("marker not found in prompt")
	}
	quoted := strings.TrimSuffix(between[1], "\n")
	if memorypolicy.UTF16Length(quoted) != memorypolicy.ExtractionTranscriptMaxUTF16Units {
		t.Fatalf("quoted final response length = %d", memorypolicy.UTF16Length(quoted))
	}
}

func TestWorkspaceForkedExtractPromptPreservesFinalResponseOuterWhitespace(t *testing.T) {
	prompt, err := BuildExtractPrompt(PromptOptions{
		Mode: "forked", FinalResponseText: "  final response  ", ForkMarker: "---fr-whitespace---",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "---fr-whitespace---\n  final response  \n---fr-whitespace---") {
		t.Fatalf("forked prompt changed final-response outer whitespace:\n%s", prompt)
	}
}

func TestWorkspaceCompactExtractPromptUsesFullRulesAndToolSchemaInstruction(t *testing.T) {
	prompt, err := BuildExtractPrompt(PromptOptions{Mode: "compact", Manifest: []ManifestRow{{ID: "mem", Text: "fact", EntityKey: "project:p"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []string{
		"## Memory", "## Memory privacy and quality", "through one emit_memories tool call", "## Existing memory facts",
	} {
		if !strings.Contains(prompt, check) {
			t.Fatalf("compact prompt missing %q", check)
		}
	}
	if strings.Contains(prompt, "Respond with ONLY a JSON object matching this schema") {
		t.Fatal("compact prompt incorrectly requested forked JSON response")
	}
}

func TestWorkspaceExtractPromptPreservesExplicitNonPositiveOperationCaps(t *testing.T) {
	for _, maxPerKind := range []int{0, -1} {
		prompt, err := BuildExtractPrompt(PromptOptions{Mode: "compact", MaxPerKind: maxPerKind})
		if err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("Maximum entries in each operation array: %d.", maxPerKind)
		if !strings.Contains(prompt, want) {
			t.Fatalf("max %d prompt missing %q", maxPerKind, want)
		}
	}
}

func TestWorkspaceExtractPromptSanitizerPreservesOrdinarySpacing(t *testing.T) {
	if got := sanitizeExtractInline("## Heading\n[Memory] alpha  beta"); got != "Heading alpha  beta" {
		t.Fatalf("extract prompt sanitizer = %q", got)
	}
}

func TestExtractModesSharePrivacyAndKeepOutputContractsSeparate(t *testing.T) {
	for _, mode := range []string{"compact", "forked"} {
		prompt, err := BuildExtractPrompt(PromptOptions{Mode: mode, MaxPerKind: 3})
		if err != nil {
			t.Fatal(err)
		}
		for _, obligation := range []string{"Never store passwords", "User-authored", "stated", "observed", "inferred"} {
			if !strings.Contains(strings.ToLower(prompt), strings.ToLower(obligation)) {
				t.Errorf("%s prompt missing %q", mode, obligation)
			}
		}
		if strings.Contains(prompt, "{{MAX}}") {
			t.Fatal("unresolved operation-limit placeholder")
		}
	}
	if _, err := BuildExtractPrompt(PromptOptions{Mode: "unsupported"}); err == nil {
		t.Fatal("unknown extraction mode accepted")
	}
}

func TestEmitSchemaMatchesOperationParserContract(t *testing.T) {
	schema, err := EmitSchema()
	if err != nil {
		t.Fatal(err)
	}
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Fatal("operation schema is not a bounded object")
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || len(properties) != 3 {
		t.Fatal("operation fields changed")
	}
	for _, operation := range []string{"append", "replace", "remove"} {
		property, ok := properties[operation].(map[string]any)
		if !ok || property["type"] != "array" {
			t.Fatalf("%s is not an array contract", operation)
		}
	}
}
