package mcpstdio

import (
	"strings"
	"testing"
)

func TestRemoteMCPRequestURLMergesRuntimeQueryParameters(t *testing.T) {
	got, err := remoteMCPRequestURL("https://provider.example/mcp?region=us-east", map[string]string{
		"apikey": "key with spaces",
		"mode":   "read-only",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://provider.example/mcp?apikey=key+with+spaces&mode=read-only&region=us-east" {
		t.Fatalf("request URL=%q", got)
	}
}

func TestRemoteMCPRequestURLRejectsUnsafeRuntimeQueryParameters(t *testing.T) {
	for name, params := range map[string]map[string]string{
		"empty name":    {"": "value"},
		"newline name":  {"api\nkey": "value"},
		"empty value":   {"apikey": ""},
		"newline value": {"apikey": "value\r\n"},
		"nul value":     {"apikey": "value\x00"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := remoteMCPRequestURL("https://provider.example/mcp", params); err == nil {
				t.Fatal("unsafe runtime query parameter was accepted")
			}
		})
	}
	if _, err := remoteMCPRequestURL("https://provider.example/mcp#fragment", map[string]string{"key": "value"}); err == nil {
		t.Fatal("fragment-bearing remote MCP URL was accepted")
	}
	if _, err := remoteMCPRequestURL("https://provider.example/mcp", map[string]string{"key": strings.Repeat("x", maxRuntimeQueryParamValueBytes+1)}); err == nil {
		t.Fatal("oversized runtime query parameter was accepted")
	}
}
