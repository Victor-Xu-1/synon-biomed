package server

import (
	"context"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func validateAgentSavedPythonSource(ctx context.Context, python string, source []byte) (agentSavedPythonStaticValidation, error) {
	return validateAgentSavedPythonReader(ctx, python, strings.NewReader(string(source)))
}

func TestValidateAgentSavedPythonStreamsLargeSourceAndTail(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	padding := "#" + strings.Repeat(" ", 17<<20) + "\n"
	for _, tail := range []string{"print('ok')", "print(undefined_value)", "def invalid(:"} {
		result, err := validateAgentSavedPythonReader(context.Background(), python, strings.NewReader(padding+tail))
		if err != nil {
			t.Fatal(err)
		}
		if result.OK != (tail == "print('ok')") {
			t.Fatalf("tail=%q result=%#v", tail, result)
		}
	}
}

func TestValidateAgentSavedPythonSource(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	valid, err := validateAgentSavedPythonSource(context.Background(), python, []byte(`
import math
values = [1, 2, 3]
def scale(value):
    return math.sqrt(value)
print([scale(value) for value in values])
`))
	if err != nil || !valid.OK || valid.Code != "valid_python" || len(valid.Unresolved) != 0 {
		t.Fatalf("valid source result=%#v err=%v", valid, err)
	}

	invalid, err := validateAgentSavedPythonSource(context.Background(), python, []byte(`print(results)`))
	if err != nil {
		t.Fatalf("invalid source infrastructure error: %v", err)
	}
	if invalid.OK || invalid.Code != "unresolved_global" || !reflect.DeepEqual(invalid.Unresolved, []string{"results"}) {
		t.Fatalf("invalid source result=%#v", invalid)
	}
}

func TestValidateAgentSavedPythonSourceRejectsSyntaxError(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	result, err := validateAgentSavedPythonSource(context.Background(), python, []byte(`def broken(:`))
	if err != nil {
		t.Fatalf("syntax validation infrastructure error: %v", err)
	}
	if result.OK || result.Code != "syntax_error" {
		t.Fatalf("syntax result=%#v", result)
	}
}
