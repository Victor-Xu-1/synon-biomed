package server

import "testing"

func TestSessionRunnerPublicProgressNarrationSuppressesProviderControlEnvelope(t *testing.T) {
	for _, content := range []string{
		`<|FunctionCallBegin|>[{"name":"complete_task"}]<|FunctionCallEnd|>`,
		`"\u003c|FunctionCallBegin|\u003e[{\"name\":\"complete_task\"}]\u003c|FunctionCallEnd|\u003e"`,
	} {
		if got := sessionRunnerPublicProgressNarration(content); got != "" {
			t.Fatalf("published control envelope %q", got)
		}
	}
}
