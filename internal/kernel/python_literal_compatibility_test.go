package kernel

import (
	"strings"
	"testing"
)

func TestPythonJSONLiteralRecoveryUsesPersistentNamespace(t *testing.T) {
	manager, _ := newHostCallTestSession(t, nil)
	cases := []struct{ code, want string }{
		{`print({"enabled": true, "missing": null, "note": "true false null"})`, `{'enabled': True, 'missing': None, 'note': 'true false null'}`},
		{`null = 37; true = "bound"; false = 5`, ""},
		{`print(null, true, false)`, "37 bound 5"},
		{`del null; print({"missing": false})`, "{'missing': 5}"},
		{`print({"missing": null})`, "{'missing': None}"},
	}
	for index, test := range cases {
		outcome := executeHostCallCell(t, manager, "literal-cell-"+string(rune('a'+index)), test.code, nil)
		if outcome.Err != nil || outcome.Response.Error != "" || strings.TrimSpace(outcome.Response.Stdout) != test.want {
			t.Fatalf("cell %d: outcome=%#v, error=%v, want stdout %q", index, outcome, outcome.Err, test.want)
		}
	}
}
