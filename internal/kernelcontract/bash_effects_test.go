package kernelcontract

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"synon-go/internal/executionprep"
)

func TestBashObservationUsesCanonicalNativeEnvelope(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("native Python unavailable")
	}
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("native Bash unavailable")
	}
	root := t.TempDir()
	startup := filepath.Join(root, "startup.sh")
	if err := os.WriteFile(startup, []byte("printf startup-ran\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{
		"pwd\nprintf '%s\\n' 'quoted $(touch out)'\necho ready",
		"# diagnostic\np\"w\"d -P\nprintf 'value=%04d\\n' 7",
	} {
		result, err := executionprep.Analyze(context.Background(), executionprep.Request{Language: "bash", Source: source}, nil)
		if err != nil || result.Observation == nil {
			t.Fatalf("plan: %#v %v", result, err)
		}
		wrapper, err := BashObservationPythonWrapper(source, result.Observation)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := BashCommandFromWrapper(wrapper)
		if err != nil || decoded != source {
			t.Fatalf("canonical decode: %q %v", decoded, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		command := exec.CommandContext(ctx, python, "-I", "-c", wrapper)
		command.Dir = root
		command.Env = append(os.Environ(), "BASH_ENV="+startup, "ENV="+startup, "BASH_FUNC_pwd%%=() { printf shadow-ran; }")
		output, err := command.CombinedOutput()
		cancel()
		if err != nil || !strings.Contains(string(output), root) || strings.Contains(string(output), "shadow-ran") || strings.Contains(string(output), "startup-ran") || !strings.Contains(string(output), BashExitPrefix+"0") {
			t.Fatalf("native startup contract: %s %v", output, err)
		}
		if _, err := os.Stat(filepath.Join(root, "out")); !os.IsNotExist(err) {
			t.Fatal("quoted data executed")
		}
		if _, err := BashObservationPythonWrapper(source+"\ntouch out", result.Observation); err == nil {
			t.Fatal("changed source reused proof")
		}
		if _, err := BashCommandFromWrapper(strings.Replace(wrapper, `"--noprofile", "--norc", "-p", "-c"`, `"-lc"`, 1) + "\n"); err == nil {
			t.Fatal("modified envelope accepted")
		}
	}
}
