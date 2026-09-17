package compute

import "testing"

func TestNormalizeBioNeMoHostedHost(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "default", want: DefaultBioNeMoHostedHost},
		{name: "bare host", raw: " API.NVIDIA.EXAMPLE ", want: "api.nvidia.example"},
		{name: "HTTPS origin", raw: "https://API.NVIDIA.EXAMPLE/", want: "api.nvidia.example"},
		{name: "explicit port", raw: "https://api.nvidia.example:8443/", want: "api.nvidia.example:8443"},
		{name: "single label", raw: "not-a-url", want: "not-a-url"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeBioNeMoHostedHost(test.raw)
			if err != nil || got != test.want {
				t.Fatalf("got=%q err=%v want=%q", got, err, test.want)
			}
		})
	}
	for _, raw := range []string{
		"http://api.nvidia.example",
		"https://user:pass@api.nvidia.example",
		"https://api.nvidia.example?token=x",
		"https://api.nvidia.example/#fragment",
		"bad host",
		"https://api.nvidia.example/v1",
		"https://localhost:8443",
		"https://127.0.0.1:8443",
		"https://10.0.0.1",
	} {
		if _, err := NormalizeBioNeMoHostedHost(raw); err == nil {
			t.Fatalf("expected %q to fail", raw)
		}
	}
}
