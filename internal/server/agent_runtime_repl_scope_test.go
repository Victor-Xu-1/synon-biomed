package server

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestREPLRecoveryParameterScopeBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		want       []string
	}{
		{"def outside", "def read(record):\n    return record.get('x')\nprint(record.get('x'))", []string{"record"}},
		{"lambda outside", "read = lambda record: record.get('x')\nprint(record.get('x'))", []string{"record"}},
		{"nested default", "def read(factory=dict(), record=None):\n    return record.get('x')", []string{}},
		{"default outer scope", "def read(record=record.get('x')):\n    return record.get('x')", []string{"record"}},
		{"lambda call boundary", "read = (lambda record: record.get('x'))(record.get('x'))", []string{"record"}},
		{"lambda comma boundary", "values = [lambda record: record.get('x'), record.get('x')]", []string{"record"}},
		{"one line def", "def read(record): return record.get('x')\nprint(record.get('x'))", []string{"record"}},
		{"multiline signature", "def read(\n factory=dict(a=1),\n record=None,\n):\n return record.get('x')", []string{}},
		{"multiline parameter outside", "def read(\n record=None,\n):\n return record.get('x')\nprint(record.get('x'))", []string{"record"}},
		{"sibling def scope", "def read(record):\n return record.get('x')\ndef other():\n return record.get('x')", []string{"record"}},
		{"async def", "async def read(record):\n return record.get('x')", []string{}},
		{"annotations and collections", "def read(record: dict[str, int], factory=dict(a=[1,2])) -> dict[str,int]:\n return record.get('x')", []string{}},
		{"unindented continuation", "def read(record):\n return (\nrecord.get('x')\n )\nprint(record.get('x'))", []string{"record"}},
		{"closure", "def read(record):\n    return lambda key: record.get(key)\n", []string{}},
		{"nested lambda", "read = lambda record: lambda x,y: record.get(x)\nprint(record.get('x'))", []string{"record"}},
		{"multiline lambda", "read = (lambda record:\n record.get('x'))", []string{}},
		{"lambda default lambda", "read = lambda factory=lambda item: item.get('x'), record=None: record.get('x')", []string{}},
		{"lambda nested default", "read = lambda factory=dict(a=1), record=None: record.get('x')", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := replRecoveryUndeclaredReceivers(tc.code); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v; want %v", got, tc.want)
			}
		})
	}
}

// Execute the actual language, not a parser mock. These cases verify that the
// preflight accepts valid local bindings but retains a real NameError outside
// their scope. Production preflight does not require spawning Python.
func TestREPLRecoveryParameterScopeAgainstPython(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required for real-language acceptance")
	}
	for _, tc := range []struct {
		name, code string
		stale      bool
	}{
		{"valid def", "def read(factory=dict(), record=None):\n return record.get('x')\nassert read(record={'x':7}) == 7", false},
		{"valid lambda", "read = lambda factory=dict(a=1), record=None: record.get('x')\nassert read(record={'x':7}) == 7", false},
		{"stale def", "def read(record):\n return record.get('x')\nprint(record.get('x'))", true},
		{"stale lambda", "read = lambda record: record.get('x')\nprint(record.get('x'))", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := exec.Command(python, "-I", "-c", tc.code).CombinedOutput()
			if tc.stale {
				if err == nil || !strings.Contains(string(output), "NameError") {
					t.Fatalf("expected real NameError: %v %s", err, output)
				}
			} else if err != nil {
				t.Fatalf("valid Python failed: %v %s", err, output)
			}
			missing := replRecoveryUndeclaredReceivers(tc.code)
			if (len(missing) > 0) != tc.stale {
				t.Fatalf("preflight and Python disagree: %v", missing)
			}
		})
	}
}
