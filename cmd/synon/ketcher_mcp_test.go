package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunKetcherMCPCLIUsesBundledAsset(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	widget := filepath.Join(root, "assets", "optional", "mcp-servers", "ketcher-chemistry", "widget", "index.html.gz")
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n")
	var output bytes.Buffer
	if err := runKetcherMCPCLI(context.Background(), []string{"--widget-gzip", widget}, input, &output); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Result.Tools) != 1 || response.Result.Tools[0].Name != "open_sketcher" {
		t.Fatalf("tools/list response = %s", output.String())
	}
}
