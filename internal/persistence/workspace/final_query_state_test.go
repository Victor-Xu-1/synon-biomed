package workspace

import (
	"math"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerificationSnapshotRejectsCrossRootReferencesAndRollsBack(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(CreateProjectInput{ID: "other-project", UserID: "owner", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	for _, input := range []CreateFrameInput{
		{ID: "root-a", ProjectID: "project", AgentName: "OPERON", Status: "completed", ConversationType: "agent"},
		{ID: "reviewer-a", ProjectID: "project", ParentFrameID: "root-a", AgentName: "REVIEWER", Status: "completed", ConversationType: "delegate"},
		{ID: "root-b", ProjectID: "project", AgentName: "OPERON", Status: "completed", ConversationType: "agent"},
		{ID: "root-c", ProjectID: "other-project", AgentName: "OPERON", Status: "completed", ConversationType: "agent"},
	} {
		if _, err := store.CreateFrame(input); err != nil {
			t.Fatal(err)
		}
	}
	_, foreignVersion, err := store.SaveArtifactVersion(SaveArtifactVersionInput{
		ArtifactID: "other-artifact", ProjectID: "other-project", Name: "other.txt",
		Kind: "text/plain", Content: []byte("other"), CreatedBy: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	claimsA := []SessionClaim{{ID: "claim-a", FrameID: "root-a", ClaimText: "Durable claim", Source: "agent"}}
	checksA := []VerificationCheck{{ID: "check-a", ClaimID: stringPointer("claim-a"), Verdict: "pass", Status: "open"}}
	if err := store.ReplaceVerificationSnapshot("root-a", VerificationSnapshot{Checks: &checksA, Claims: &claimsA}); err != nil {
		t.Fatal(err)
	}
	claimsB := []SessionClaim{{ID: "claim-b", FrameID: "root-b", ClaimText: "Other root", Source: "agent"}}
	if err := store.ReplaceVerificationSnapshot("root-b", VerificationSnapshot{Claims: &claimsB}); err != nil {
		t.Fatal(err)
	}

	for name, invalid := range map[string]VerificationCheck{
		"claim":    {ID: "invalid-claim", ClaimID: stringPointer("claim-b"), Verdict: "fail"},
		"reviewer": {ID: "invalid-reviewer", ReviewerFrameID: stringPointer("root-b"), Verdict: "fail"},
		"artifact": {ID: "invalid-artifact", ArtifactVersionID: &foreignVersion.ID, Verdict: "fail"},
	} {
		t.Run(name, func(t *testing.T) {
			checks := []VerificationCheck{invalid}
			if err := store.ReplaceVerificationSnapshot("root-a", VerificationSnapshot{Checks: &checks}); err == nil {
				t.Fatal("cross-root verification reference was accepted")
			}
			stored, err := store.ListVerificationChecks("root-a", "")
			if err != nil {
				t.Fatal(err)
			}
			if len(stored) != 1 || stored[0].ID != "check-a" {
				t.Fatalf("failed replacement mutated snapshot: %#v", stored)
			}
		})
	}

	oversized := []SessionClaim{{ID: "too-large", FrameID: "root-a", ClaimText: strings.Repeat("x", maxVerificationTextBytes+1), Source: "agent"}}
	if err := store.ReplaceVerificationSnapshot("root-a", VerificationSnapshot{Claims: &oversized}); err == nil {
		t.Fatal("oversized verification text was accepted")
	}
}

func TestTokenClassDecoderRejectsAmbiguousAndOverflowingUsage(t *testing.T) {
	if _, err := decodeTokenClassUsage(`{"main":{"input":1}} {"other":{"input":2}}`); err == nil {
		t.Fatal("multiple JSON values were accepted")
	}
	target := TokenClassUsage{Input: math.MaxInt64, Cost: math.MaxFloat64}
	if err := addTokenUsage(&target, TokenClassUsage{Input: 1}); err == nil {
		t.Fatal("integer token overflow was accepted")
	}
	target = TokenClassUsage{Cost: math.MaxFloat64}
	if err := addTokenUsage(&target, TokenClassUsage{Cost: math.MaxFloat64}); err == nil {
		t.Fatal("floating token overflow was accepted")
	}
}

func stringPointer(value string) *string {
	return &value
}
