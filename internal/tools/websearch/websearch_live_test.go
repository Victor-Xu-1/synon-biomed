package websearch

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveDefaultBackendsReachNetwork is an opt-in live verification that the
// default Go HTTP backends (mojeek, wikipedia, crossref, duckduckgo-lite) can
// return usable links from the real network. Run with:
//
//	SYNON_LIVE_WEBSEARCH=1 go test ./internal/tools/websearch -run TestLiveDefaultBackendsReachNetwork -count=1 -timeout 120s
func TestLiveDefaultBackendsReachNetwork(t *testing.T) {
	if strings.TrimSpace(os.Getenv("SYNON_LIVE_WEBSEARCH")) != "1" {
		t.Skip("live network verification is opt-in")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := Search(ctx, Input{
		Query:      "sickle cell disease base editing clinical trial 2025",
		MaxResults: 8,
	}, Options{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("Search error = %v", err)
	}
	if out.Failure != nil {
		t.Fatalf("Search failure = %#v diagnostics=%#v", out.Failure, out.Diagnostics)
	}
	if len(out.Sources) == 0 {
		t.Fatalf("Search returned no sources: %#v", out.Diagnostics)
	}
	t.Logf("sources=%d first=%s duration=%.1fs backends=%v", len(out.Sources), out.Sources[0].URL, out.DurationSeconds, out.Diagnostics["httpBackends"])
}

func TestLiveLRRK2SearchReturnsUsableEvidence(t *testing.T) {
	if strings.TrimSpace(os.Getenv("SYNON_LIVE_WEBSEARCH")) != "1" {
		t.Skip("live network verification is opt-in")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := Search(ctx, Input{
		Query:      "LRRK2 degrader PROTAC clinical trial Parkinson 2024 2025",
		MaxResults: 8,
	}, Options{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("Search error = %v", err)
	}
	if out.Failure != nil || len(out.Sources) == 0 {
		t.Fatalf("LRRK2 search failure=%#v sources=%d diagnostics=%#v", out.Failure, len(out.Sources), out.Diagnostics)
	}
	t.Logf("sources=%d first=%s duration=%.1fs backends=%v", len(out.Sources), out.Sources[0].URL, out.DurationSeconds, out.Diagnostics["httpBackends"])
}

func TestLiveMultilingualAndPharmaceuticalSearchReturnsUsableEvidence(t *testing.T) {
	if strings.TrimSpace(os.Getenv("SYNON_LIVE_WEBSEARCH")) != "1" {
		t.Skip("live network verification is opt-in")
	}
	queries := []string{
		"万古霉素 成人严重MRSA感染 个体化给药 指南",
		"weak base oral drug salt polymorph screening case study",
	}
	for _, query := range queries {
		t.Run(query, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			out, err := Search(ctx, Input{Query: query, MaxResults: 8}, Options{Timeout: 30 * time.Second})
			if err != nil {
				t.Fatalf("Search error = %v", err)
			}
			if out.Failure != nil || len(out.Sources) == 0 {
				t.Fatalf("search failure=%#v sources=%d diagnostics=%#v", out.Failure, len(out.Sources), out.Diagnostics)
			}
			t.Logf("sources=%d first=%s backends=%v", len(out.Sources), out.Sources[0].URL, out.Diagnostics["httpBackends"])
		})
	}
}

func TestLiveBroadDiscoveryReturnsMoreThanLegacyTwentyResultCeiling(t *testing.T) {
	if strings.TrimSpace(os.Getenv("SYNON_LIVE_WEBSEARCH")) != "1" {
		t.Skip("live network verification is opt-in")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	out, err := Search(ctx, Input{
		Query:      "oral drug formulation salt polymorph bioavailability clinical development",
		MaxResults: 50,
	}, Options{Timeout: 45 * time.Second})
	if err != nil {
		t.Fatalf("Search error = %v", err)
	}
	if out.Failure != nil {
		t.Fatalf("Search failure = %#v diagnostics=%#v", out.Failure, out.Diagnostics)
	}
	if len(out.Sources) <= 20 {
		t.Fatalf("broad discovery returned %d sources; legacy ceiling was not removed: %#v", len(out.Sources), out.Diagnostics)
	}
	seen := map[string]bool{}
	for _, source := range out.Sources {
		if source.CanonicalURL == "" || seen[source.CanonicalURL] {
			t.Fatalf("broad discovery returned empty or duplicate canonical URL: %#v", source)
		}
		seen[source.CanonicalURL] = true
	}
	t.Logf("sources=%d first=%s backends=%v", len(out.Sources), out.Sources[0].URL, out.Diagnostics["httpBackends"])
}

func TestLiveExactClinicalTrialIdentifierReturnsUsableEvidence(t *testing.T) {
	if strings.TrimSpace(os.Getenv("SYNON_LIVE_WEBSEARCH")) != "1" {
		t.Skip("live network verification is opt-in")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := Search(ctx, Input{
		Query:      "NCT03600883 ClinicalTrials",
		MaxResults: 20,
	}, Options{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("Search error = %v", err)
	}
	if out.Failure != nil || len(out.Sources) == 0 {
		t.Fatalf("clinical-trial identifier search failure=%#v sources=%d diagnostics=%#v", out.Failure, len(out.Sources), out.Diagnostics)
	}
	found := false
	for _, source := range out.Sources {
		if strings.Contains(strings.ToUpper(source.Title+" "+source.URL+" "+source.Snippet), "NCT03600883") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("clinical-trial identifier was absent from returned evidence: %#v", out.Sources)
	}
}
