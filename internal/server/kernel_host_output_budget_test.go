package server

import "testing"

func TestKernelHostOutputBudgetUsesProviderDefaultUnlessExplicit(t *testing.T) {
	for _, test := range []struct {
		name     string
		value    any
		expected int
		invalid  bool
	}{
		{name: "provider default"},
		{name: "explicit small", value: 123, expected: 123},
		{name: "explicit beyond former ceiling", value: 65536, expected: 65536},
		{name: "zero", value: 0, invalid: true},
		{name: "negative", value: -1, invalid: true},
		{name: "text", value: "4096", invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := map[string]any{"prompt": "Analyze the supplied records."}
			if test.value != nil {
				raw["max_tokens"] = test.value
			}
			request, _, err := parseKernelHostLLMRequest(raw, t.TempDir())
			if test.invalid {
				if err == nil {
					t.Fatal("invalid budget accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if request.MaxTokens != test.expected {
				t.Fatalf("budget=%d want=%d", request.MaxTokens, test.expected)
			}
		})
	}
}
