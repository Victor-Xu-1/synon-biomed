package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAgentSaveArtifactsLargeMoleculeLibraries(t *testing.T) {
	manager := realManagedScientificKernelManagerForServerTest(t)
	for _, format := range []string{"sdf", "smi"} {
		t.Run(format, func(t *testing.T) {
			fixture := newAgentSaveArtifactsFixtureWithKernelManager(t, manager)
			body := "#" + strings.Repeat("padding", 1024) + "\n"
			if format == "sdf" {
				body = strings.Replace(validServerEthanolSDF, "$$$$", "> <description>\n"+strings.Repeat("padding", 1024)+"\n\n$$$$", 1)
			}
			content := strings.Repeat(body, 8000)
			if format == "smi" {
				content += "CCO\n"
			}
			if len(content) <= 50<<20 {
				t.Fatal("fixture did not cross former input limit")
			}
			name := "out/molecules." + format
			write := writeAgentSaveArtifactsFile(t, fixture.projectPath, name, content)
			fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "large-valid", 1, write)
			input := map[string]any{"files": []any{name}, "language": "python", "environment": "synon-biomed-python", "human_description": "Saving validated molecules"}
			result, err := fixture.server.executeAgentSaveArtifacts(fixture.toolContext(t, "save-large", input), fixture.identity, "save-large", input)
			if err != nil || result["errors"] != nil {
				t.Fatalf("save large %s: %v %#v", format, err, result)
			}
			artifacts := agentSaveArtifactResults(t, result)
			if len(artifacts) != 1 {
				t.Fatalf("artifacts=%v", artifacts)
			}
			versionID := stringValue(artifacts[0]["version_id"])
			artifact, version, reader, found, err := fixture.store.OpenArtifactVersionContent(versionID)
			if err != nil || !found {
				t.Fatalf("open version: %v found=%v", err, found)
			}
			hash := sha256.New()
			n, copyErr := io.Copy(hash, reader)
			closeErr := reader.Close()
			want := sha256.Sum256([]byte(content))
			if copyErr != nil || closeErr != nil || n != int64(len(content)) || version.StoragePath == "" || hex.EncodeToString(hash.Sum(nil)) != hex.EncodeToString(want[:]) {
				t.Fatalf("large content not preserved: bytes=%d copy=%v close=%v", n, copyErr, closeErr)
			}
			write = writeAgentSaveArtifactsFile(t, fixture.projectPath, name, content+"invalid molecular tail\n")
			fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "large-invalid", 2, write)
			result, err = fixture.server.executeAgentSaveArtifacts(fixture.toolContext(t, "save-large-invalid", input), fixture.identity, "save-large-invalid", input)
			if !errors.Is(err, errAgentSaveArtifactsNoResults) || len(agentSaveArtifactResults(t, result)) != 0 {
				t.Fatalf("invalid large tail published: %v %#v", err, result)
			}
			_, current, found, err := fixture.store.GetCurrentArtifactVersionMetadata(artifact.ID)
			if err != nil || !found || current.ID != versionID {
				t.Fatalf("invalid source replaced good artifact: %#v %v", current, err)
			}
		})
	}
}

func scientificArtifactStreamTestValidator(t *testing.T) func(context.Context, io.Reader, string) (scientificArtifactValidation, error) {
	t.Helper()
	python := os.Getenv("SYNON_TEST_SCIENTIFIC_PYTHON")
	if python == "" {
		t.Skip("set SYNON_TEST_SCIENTIFIC_PYTHON to run the real RDKit validator protocol; managed-generation resolution is tested separately")
	}
	version, err := exec.Command(python, "-I", "-c", "from rdkit import rdBase; print(rdBase.rdkitVersion)").Output()
	if err != nil {
		t.Fatal(err)
	}
	_, source, _, _ := runtime.Caller(0)
	validator := filepath.Join(filepath.Dir(source), "..", "..", "assets", "optional", "kernels", "sdf_artifact_validator.py")
	return func(ctx context.Context, reader io.Reader, format string) (scientificArtifactValidation, error) {
		return runScientificArtifactValidator(ctx, reader, format, python, validator, "protocol-test", strings.TrimSpace(string(version)))
	}
}

func TestScientificArtifactStreamLargeInputAndTail(t *testing.T) {
	validate := scientificArtifactStreamTestValidator(t)
	for _, format := range []string{"sdf", "smi"} {
		t.Run(format, func(t *testing.T) {
			file, err := os.CreateTemp(t.TempDir(), "molecules-*")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			body := "#" + strings.Repeat("padding", 1024) + "\n"
			expected := 1
			if format == "sdf" {
				body = strings.Replace(validServerEthanolSDF, "$$$$", "> <description>\n"+strings.Repeat("padding", 1024)+"\n\n$$$$", 1)
				expected = 8000
			}
			for n := 0; n < 8000; n++ {
				if _, err := io.WriteString(file, body); err != nil {
					t.Fatal(err)
				}
			}
			if format == "smi" {
				if _, err := io.WriteString(file, "CCO\n"); err != nil {
					t.Fatal(err)
				}
			}
			info, err := file.Stat()
			if err != nil || info.Size() <= 50<<20 {
				t.Fatalf("large input size: %v %v", info, err)
			}
			if _, err := file.Seek(0, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			result, err := validate(context.Background(), file, format)
			if err != nil || !result.OK || result.ParsedCount != expected {
				t.Fatalf("full %s input: result=%#v err=%v", format, result, err)
			}
			if _, err := file.Seek(0, io.SeekEnd); err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(file, "invalid molecular tail\n"); err != nil {
				t.Fatal(err)
			}
			if _, err := file.Seek(0, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			if _, err := validate(context.Background(), file, format); !errors.Is(err, errInvalidScientificArtifact) {
				t.Fatalf("invalid tail accepted: %v", err)
			}
		})
	}
}

func TestScientificArtifactStreamLargeRecordCount(t *testing.T) {
	validate := scientificArtifactStreamTestValidator(t)
	const count = 550000
	result, err := validate(context.Background(), strings.NewReader(strings.Repeat("C\n", count)), "smi")
	if err != nil || !result.OK || result.ParsedCount != count {
		t.Fatalf("many molecules: result=%#v err=%v", result, err)
	}
}

func TestScientificArtifactStreamDelimiterBoundaries(t *testing.T) {
	python := os.Getenv("SYNON_TEST_SCIENTIFIC_PYTHON")
	if python == "" {
		t.Skip("set SYNON_TEST_SCIENTIFIC_PYTHON for the real parser")
	}
	_, source, _, _ := runtime.Caller(0)
	validator := filepath.Join(filepath.Dir(source), "..", "..", "assets", "optional", "kernels", "sdf_artifact_validator.py")
	code := `
import importlib.util, io, random, sys
spec = importlib.util.spec_from_file_location("validator_test", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
rng = random.Random(15)
for _ in range(500):
    raw = b"".join(rng.choice([b"$$$$", b"$", b"\r", b"\n", b" ", b"data"]) for _ in range(30))
    lines = raw.splitlines()
    count = sum(line == b"$$$$" for line in lines)
    while lines and not lines[-1]: lines.pop()
    terminal = bool(lines) and lines[-1] == b"$$$$"
    for size in (1, 2, 3, 4, 17, 1024):
        reader = module.DelimiterReader(io.BytesIO(raw))
        while reader.read(size): pass
        assert (reader.delimiter_count, reader.terminal_delimiter) == (count, terminal), raw
class BoundedReads(io.BytesIO):
    def read(self, size=-1):
        assert size >= 0, "whole input read is forbidden"
        return super().read(size)
result = module.validate_sdf(BoundedReads(sys.stdin.buffer.read()))
assert result["ok"], result
assert result["parsedCount"] == 1, result
`
	command := exec.Command(python, "-I", "-c", code, validator)
	command.Stdin = strings.NewReader(validServerEthanolSDF)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("stream boundaries: %v\n%s", err, output)
	}
}
