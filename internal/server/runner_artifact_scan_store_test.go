package server

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestArtifactScanStoreDiskComparisonMatchesExistingSemantics(t *testing.T) {
	store, err := newRunnerArtifactScanStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	directory := store.directory
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
		if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("scan scratch not cleaned: %v", err)
		}
	}()
	for _, pair := range []struct{ left, right runnerCrossArtifactSnapshot }{
		{runnerCrossArtifactSnapshot{"data.csv", "entity,value\na,1\nb,2\n"}, runnerCrossArtifactSnapshot{"report.md", "|entity|value|\n|---|---|\n|a|8|\n|b|2|\n"}},
		{runnerCrossArtifactSnapshot{"data.csv", "dose,cohort,value\n1.234,A,10\n1.236,B,20\n"}, runnerCrossArtifactSnapshot{"report.md", "|dose|cohort|value|\n|---|---|---|\n|1.23|A|90|\n|1.24|B|20|\n"}},
		{runnerCrossArtifactSnapshot{"data.csv", "entity,value\na,1\na,2\n"}, runnerCrossArtifactSnapshot{"report.md", "|metric|a|\n|---|---|\n|value|9|\n"}},
	} {
		left, err := store.readTables(pair.left.name, strings.NewReader(pair.left.text))
		if err != nil {
			t.Fatal(err)
		}
		right, err := store.readTables(pair.right.name, strings.NewReader(pair.right.text))
		if err != nil {
			t.Fatal(err)
		}
		memoryLeft, memoryRight := runnerCrossArtifactTables(pair.left)[0], runnerCrossArtifactTables(pair.right)[0]
		got, err := inspectRunnerCrossArtifactTables(store, left[0], right[0])
		want := legacyCompareRunnerCrossArtifactTables(memoryLeft, memoryRight)
		if err != nil || !sameStringSet(got, want) {
			t.Fatalf("comparison=%v want=%v err=%v", got, want, err)
		}
		got, err = inspectRunnerCrossArtifactTransposed(store, left[0], right[0])
		want = legacyCompareRunnerCrossArtifactTransposedTables(memoryLeft, memoryRight)
		if err != nil || !sameStringSet(got, want) {
			t.Fatalf("transposed=%v want=%v err=%v", got, want, err)
		}
	}
	snapshot := runnerCrossArtifactSnapshot{"rank.csv", "entity,score_neutral,rank_neutral\na,1.0000000000000,1\nb,1.0000000000005,1\nc,1.000000000002,3\nd,0,4\ne,invalid,9\n"}
	tables, err := store.readTables(snapshot.name, strings.NewReader(snapshot.text))
	if err != nil {
		t.Fatal(err)
	}
	got, err := inspectRunnerCrossArtifactRanks(store, tables[0])
	want := legacyRunnerCrossArtifactRankFailures(runnerCrossArtifactTables(snapshot)[0])
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("ranks=%v want=%v err=%v", got, want, err)
	}
}

func TestArtifactScanStoreCancellationAndCorruptionCannotPass(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := newRunnerArtifactScanStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tables, err := store.readTables("a.csv", strings.NewReader("entity,value\na,1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.exec(`UPDATE scan_rows SET cells='{' WHERE scope=?`, tables[0].scanScope); err != nil {
		t.Fatal(err)
	}
	if err := tables[0].eachRow(ctx, func(int, []string) error { return nil }); err == nil {
		t.Fatal("corrupt derived row silently passed")
	}
	cancel()
	if _, err := store.readTables("b.csv", strings.NewReader("entity,value\nb,2\n")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled scan=%v", err)
	}
}
