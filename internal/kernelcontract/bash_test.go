package kernelcontract

import (
	"strings"
	"testing"
)

func TestBashWrapperRoundTripRequiresCanonicalEnvelope(t *testing.T) {
	command := "printf 'result=%s\\n' ok\nprintf 'warning\\n' >&2"
	wrapper, err := BashPythonWrapper(command)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := BashCommandFromWrapper(wrapper)
	if err != nil || decoded != command {
		t.Fatalf("decoded=%q err=%v", decoded, err)
	}

	for _, invalid := range []string{
		strings.Replace(wrapper, "bufsize=1", "bufsize=2", 1),
		strings.Replace(wrapper, BashSourcePrefix, "# untrusted:", 1),
		BashSourcePrefix + "%%%\n",
	} {
		if _, err := BashCommandFromWrapper(invalid); err == nil {
			t.Fatalf("non-canonical Bash envelope was accepted: %q", invalid)
		}
	}
}

func TestBashWrapperRejectsEmptyNULAndOversizedCommands(t *testing.T) {
	for _, command := range []string{"", "\x00", strings.Repeat("x", MaxBashBytes+1)} {
		if _, err := BashPythonWrapper(command); err == nil {
			t.Fatalf("invalid Bash command was accepted: bytes=%d", len(command))
		}
	}
}

func TestBashWrapperDecodesImmutableHistoricalEnvelopeWithoutGeneratingIt(t *testing.T) {
	command := "printf 'historical evidence\\n'"
	legacy, err := bashPythonWrapper(command, bashLegacyPump)
	if err != nil {
		t.Fatal(err)
	}
	current, err := BashPythonWrapper(command)
	if err != nil || current == legacy {
		t.Fatalf("current wrapper did not change: %v", err)
	}
	decoded, err := BashCommandFromWrapper(legacy)
	if err != nil || decoded != command {
		t.Fatalf("historical source=%q err=%v", decoded, err)
	}
	if _, err := BashCommandFromWrapper(strings.Replace(legacy, "read(8192)", "read(1)", 1)); err == nil {
		t.Fatal("modified historical execution envelope was accepted")
	}
}
