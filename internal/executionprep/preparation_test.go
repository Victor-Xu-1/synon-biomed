package executionprep

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestPreparationBoundsAndUncertaintyAreExplicit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Analyze(ctx, Request{Language: "bash", Source: "echo safe"}, nil); err == nil {
		t.Fatal("cancelled preparation continued")
	}
	if _, err := Analyze(context.Background(), Request{Language: "bash", Source: strings.Repeat("x", MaxSourceBytes+1)}, nil); err == nil {
		t.Fatal("unbounded input accepted")
	}
	invalid, err := Analyze(context.Background(), Request{Language: "bash", Source: "if broken"}, nil)
	if err != nil || len(invalid.Unresolved) == 0 || len(invalid.Requirements) != 0 {
		t.Fatalf("invalid source is not a verified plan: %#v %v", invalid, err)
	}
	count := 0
	native := func(context.Context, string, string) ([]Fact, error) {
		count++
		return []Fact{{Kind: "source", Name: "python", Args: []string{fmt.Sprintf("nested-%d", count)}}}, nil
	}
	bounded, err := Analyze(context.Background(), Request{Language: "python", Source: "entry"}, native)
	if err != nil || count > maxDepth+1 || len(bounded.Unresolved) != 1 || bounded.Unresolved[0] != "analysis_budget" {
		t.Fatalf("nested source unbounded: %#v %v calls=%d", bounded, err, count)
	}
}

func TestPreparationDoesNotRoutePrivateOrAuthenticatedURLsToPublicDownload(t *testing.T) {
	for _, target := range []string{"http://localhost./file", "http://127.0.0.1/file", "http://[::ffff:127.0.0.1]/file", "http://10.1.2.3/file", "https://name:password@example.org/file"} {
		result, err := Analyze(context.Background(), Request{Language: "bash", Source: "curl -o file '" + target + "'"}, nil)
		if err != nil || len(result.Requirements) != 0 {
			t.Fatalf("private transfer routed to public tool: %#v %v", result, err)
		}
	}
}

func TestPreparationPreservesWitnessedEffectsOnPartialParserFailure(t *testing.T) {
	result, err := Analyze(context.Background(), Request{Language: "r", Source: "input"},
		func(context.Context, string, string) ([]Fact, error) {
			return []Fact{{Kind: "call", Name: "install.packages"}}, fmt.Errorf("parser interrupted")
		})
	if err != nil || len(result.Unresolved) != 1 || len(result.Requirements) != 1 || result.Requirements[0].Effect != PackageMutation {
		t.Fatalf("partial evidence discarded: %#v %v", result, err)
	}
}
