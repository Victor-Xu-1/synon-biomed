package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestRunnerTextEncodingRetainsMultibyteBoundaries(t *testing.T) {
	for offset := 0; offset < 4; offset++ {
		prefix := strings.Repeat("x", (64<<10)-offset)
		for _, suffix := range []string{"中文😀tail", "\xef\xbf\xbd"} {
			if err := validateRunnerTextEncoding(context.Background(), strings.NewReader(prefix+suffix)); err != nil {
				t.Fatal(err)
			}
		}
		for _, suffix := range []string{"\xe4", "\x80\x80", "\xf0\x9f\x98", "\xff"} {
			if err := validateRunnerTextEncoding(context.Background(), strings.NewReader(prefix+suffix)); !errors.Is(err, errAgentSavedArtifactTextInvalid) {
				t.Fatalf("invalid suffix=%q offset=%d err=%v", suffix, offset, err)
			}
		}
	}
	sentinel := errors.New("text read failure")
	if err := validateRunnerTextEncoding(context.Background(), io.MultiReader(strings.NewReader("valid"), sourceEvidenceFailReader{sentinel})); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := validateRunnerTextEncoding(ctx, strings.NewReader("valid")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestRunnerMarkdownStreamingKeepsTableOrderAndRows(t *testing.T) {
	for _, content := range []string{
		"# Report\n\n| a | b |\n|---|---|\n|1|2|\n|3|4|\ntext\n| c | d |\n|---|---|\n|5|6|",
		"a | b\r\n---|---\r\n\r\nc|d\r\n---|---\r\ne|f\r\n",
		"a|b\n---|---\n1|2\na|b|c\n---|---|---\n3|4|5\n",
	} {
		want := runnerMarkdownArtifactTables(runnerCrossArtifactSnapshot{name: "report.md", text: content})
		var got []runnerCrossArtifactTable
		err := visitRunnerMarkdownRows(context.Background(), strings.NewReader(content), func(ordinal, row int, headers, values []string) error {
			if ordinal == len(got) {
				got = append(got, runnerCrossArtifactTable{source: "report.md", ordinal: ordinal, headers: headers})
			}
			if row != len(got[ordinal].rows)+2 {
				t.Fatalf("wrong row=%d", row)
			}
			got[ordinal].rows = append(got[ordinal].rows, values)
			return nil
		})
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("streamed tables=%#v want=%#v err=%v", got, want, err)
		}
	}
}

func TestArtifactReferenceNormalizationStreamsChunkBoundaries(t *testing.T) {
	const reference = "{{artifact:123e4567-e89b-12d3-a456-426614174000}}"
	resolve := func(string) (agentSavedArtifactReferenceResolution, bool, error) {
		return agentSavedArtifactReferenceResolution{projectID: "p", relativePath: "data/table.csv"}, true, nil
	}
	for delta := -60; delta < 60; delta++ {
		input := strings.Repeat("x", (64<<10)+delta) + reference + "中文" + reference
		want, _, err := normalizeAgentSavedArtifactReferenceText(input, "report.md", "p", resolve)
		if err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		changed, err := streamAgentSavedArtifactReferences(context.Background(), strings.NewReader(input), &got, "report.md", "p", resolve)
		if err != nil || !changed || got.String() != want {
			t.Fatalf("delta=%d changed=%t err=%v", delta, changed, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := streamAgentSavedArtifactReferences(ctx, strings.NewReader(reference), io.Discard, "report.md", "p", resolve); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
