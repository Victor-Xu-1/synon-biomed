package server

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestArtifactPatternStreamMatchesWholeInputOffsets(t *testing.T) {
	random := rand.New(rand.NewSource(51))
	pieces := []string{"文", "\n", " ", "\t", "outputs/alpha.csv", "reports/test.json", "numpy MISSING", "pandas MISSING", "prefix", "\r\n", "MISSING", "_"}
	for trial := 0; trial < 300; trial++ {
		var source strings.Builder
		for n := 0; n < 30; n++ {
			source.WriteString(pieces[random.Intn(len(pieces))])
		}
		for _, anchored := range []bool{false, true} {
			pattern := runnerCrossArtifactPathPattern
			if anchored {
				pattern = runnerCrossArtifactMissingPackagePattern
			}
			var actual [][]int
			err := scanRunnerArtifactPattern(context.Background(), strings.NewReader(source.String()), pattern, anchored, func(indices []int64) error {
				converted := make([]int, len(indices))
				for index, value := range indices {
					converted[index] = int(value)
				}
				actual = append(actual, converted)
				return nil
			})
			want := pattern.FindAllStringSubmatchIndex(source.String(), -1)
			if err != nil || !reflect.DeepEqual(actual, want) {
				t.Fatalf("pattern=%s input=%q actual=%v want=%v err=%v", pattern, source.String(), actual, want, err)
			}
		}
	}
}

type artifactScanBrokenReader struct {
	*strings.Reader
	boundary int64
	cause    error
}

func (reader artifactScanBrokenReader) Read(buffer []byte) (int, error) {
	position, err := reader.Reader.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	if position >= reader.boundary {
		return 0, reader.cause
	}
	if remaining := reader.boundary - position; int64(len(buffer)) > remaining {
		buffer = buffer[:int(remaining)]
	}
	return reader.Reader.Read(buffer)
}

func TestArtifactScanPropagatesStorageErrorsAndCancellation(t *testing.T) {
	cause := errors.New("storage became unavailable")
	broken := func(text string) artifactScanBrokenReader {
		return artifactScanBrokenReader{strings.NewReader(text), 5, cause}
	}
	if err := scanRunnerArtifactPattern(context.Background(), broken("outputs/result.csv"), runnerCrossArtifactPathPattern, false, func([]int64) error { return nil }); !errors.Is(err, cause) {
		t.Fatalf("pattern error=%v", err)
	}
	if _, err := scanRunnerMachineValidation(context.Background(), broken(`{"passed":true}`), "validation.json"); !errors.Is(err, cause) {
		t.Fatalf("machine error=%v", err)
	}
	if _, err := probeRunnerArtifactContract(context.Background(), broken(`{"schema":"synon.artifact-table-contract.v1"}`)); !errors.Is(err, cause) {
		t.Fatalf("contract error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scanRunnerMachineValidation(ctx, strings.NewReader(`{"passed":true}`), "validation.json"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}

func TestMachineValidationStreamDuplicateFields(t *testing.T) {
	for _, test := range []struct {
		body     string
		failures int
	}{
		{`{"passed":false,"passed":true}`, 0},
		{`{"passed":true,"passed":false}`, 2},
		{`{"checks":{"ok":false},"checks":{}}`, 1},
		{`{"passed":true,"errors":["bad"],"errors":[]}`, 0},
		{`{"errors":[{"passed":true}],"errors":[]}`, 1},
	} {
		failures, err := scanRunnerMachineValidation(context.Background(), strings.NewReader(test.body), "validation.json")
		if err != nil || len(failures) != test.failures {
			t.Fatalf("body=%s failures=%v error=%v", test.body, failures, err)
		}
	}
}

func TestArtifactPresentationStreamCleansTemporaryText(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("TMPDIR", directory)
	for _, malformed := range []bool{false, true} {
		var archive bytes.Buffer
		writer := zip.NewWriter(&archive)
		entry, err := writer.Create("ppt/slides/slide1.xml")
		if err != nil {
			t.Fatal(err)
		}
		body := `<slide><t>outputs/missing.csv</t><t>中文</t></slide>`
		if malformed {
			body = `<slide><t>unclosed`
		}
		if _, err := io.WriteString(entry, body); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		reader, err := streamRunnerPresentationText(context.Background(), bytes.NewReader(archive.Bytes()))
		if malformed {
			if err == nil {
				t.Fatal("malformed slide accepted")
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(reader)
			if err != nil || string(data) != "outputs/missing.csv\n中文\n" {
				t.Fatalf("text=%q err=%v", data, err)
			}
			if err := reader.Close(); err != nil {
				t.Fatal(err)
			}
		}
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != 0 {
			t.Fatalf("temporary text leaked: %v %v", entries, err)
		}
	}
}
