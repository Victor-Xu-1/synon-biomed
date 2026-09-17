package ketchermcp

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestServerImplementsToolsAndResourcesOverRealStdio(t *testing.T) {
	html := []byte("<!doctype html><title>Ketcher fixture</title>")
	digest := sha256.Sum256(html)
	widget := filepath.Join(t.TempDir(), "index.html.gz")
	file, err := os.Create(widget)
	if err != nil {
		t.Fatal(err)
	}
	writer := gzip.NewWriter(file)
	if _, err := writer.Write(html); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	requests := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"open_sketcher","arguments":{"smiles":"c1ccccc1","filename":"benzene"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":5,"method":"resources/read","params":{"uri":"ui://ketcher-chemistry/editor"}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := Run(context.Background(), Options{
		Input: strings.NewReader(requests), Output: &output, WidgetGzipPath: widget,
		WidgetSHA256: hex.EncodeToString(digest[:]),
	}); err != nil {
		t.Fatal(err)
	}

	responses := make([]map[string]any, 0, 5)
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var response map[string]any
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatal(err)
		}
		responses = append(responses, response)
	}
	if len(responses) != 5 {
		t.Fatalf("responses = %d, want 5: %s", len(responses), output.String())
	}
	tools := responses[1]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "open_sketcher" {
		t.Fatalf("tools/list result = %#v", responses[1])
	}
	metadata := tools[0].(map[string]any)["_meta"].(map[string]any)
	viewer := metadata["operon.dev/viewer"].(map[string]any)
	save, ok := viewer["save"].(map[string]any)
	if !ok {
		t.Fatalf("editable viewer save contract = %#v", viewer["save"])
	}
	for key, want := range map[string]string{
		"appTool":        "get_structure",
		"resultField":    "ket",
		"mimeType":       "application/json",
		"extension":      ".ket",
		"filenameStem":   "sketcher",
		"hasChangeField": "has_change",
		"emptyTemplate":  `{"root":{"nodes":[]}}`,
	} {
		if got := save[key]; got != want {
			t.Fatalf("viewer save %s = %#v, want %q", key, got, want)
		}
	}
	wantPrompt := `Save molecules and reactions as .ket/.mol/.rxn artifacts — they open in the sketcher where the user can edit. Do NOT render as static PNGs unless asked. Drive the live tile via host.app("ketcher-chemistry").<handler>(artifact_id=...).`
	if prompt, _ := viewer["promptHint"].(string); prompt != wantPrompt {
		t.Fatalf("editable viewer prompt = %q", prompt)
	}
	structured := responses[2]["result"].(map[string]any)["structuredContent"].(map[string]any)
	if structured["smiles"] != "c1ccccc1" {
		t.Fatalf("tools/call result = %#v", responses[2])
	}
	content := responses[2]["result"].(map[string]any)["content"].([]any)
	if got := content[0].(map[string]any)["text"]; got != "Opened molecule sketcher." {
		t.Fatalf("tools/call text = %#v", got)
	}
	resources := responses[3]["result"].(map[string]any)["resources"].([]any)
	if len(resources) != 1 || resources[0].(map[string]any)["uri"] != ResourceURI {
		t.Fatalf("resources/list result = %#v", responses[3])
	}
	if got := resources[0].(map[string]any)["description"]; got != "Interactive 2D molecule sketcher (Ketcher)" {
		t.Fatalf("resource description = %#v", got)
	}
	contents := responses[4]["result"].(map[string]any)["contents"].([]any)
	if contents[0].(map[string]any)["text"] != string(html) {
		t.Fatalf("resources/read result = %#v", responses[4])
	}
}

func TestServerImplementsModernStatelessProtocolOverRealStdio(t *testing.T) {
	html := []byte("<!doctype html><title>Ketcher modern fixture</title>")
	digest := sha256.Sum256(html)
	widget := filepath.Join(t.TempDir(), "index.html.gz")
	file, err := os.Create(widget)
	if err != nil {
		t.Fatal(err)
	}
	writer := gzip.NewWriter(file)
	if _, err := writer.Write(html); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	meta := `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"}}`
	requests := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{` + meta + `}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{` + meta + `}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"open_sketcher","arguments":{"smiles":"CCO"},` + meta + `}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := Run(context.Background(), Options{
		Input: strings.NewReader(requests), Output: &output, WidgetGzipPath: widget,
		WidgetSHA256: hex.EncodeToString(digest[:]),
	}); err != nil {
		t.Fatal(err)
	}
	for index, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var response map[string]any
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatal(err)
		}
		result, _ := response["result"].(map[string]any)
		if result["resultType"] != "complete" {
			t.Fatalf("response %d resultType = %#v", index, result["resultType"])
		}
		resultMeta, _ := result["_meta"].(map[string]any)
		serverInfo, _ := resultMeta[serverInfoMetaKey].(map[string]any)
		if serverInfo["name"] != "ketcher-chemistry" {
			t.Fatalf("response %d serverInfo = %#v", index, serverInfo)
		}
		if index == 0 {
			versions, _ := result["supportedVersions"].([]any)
			if len(versions) == 0 || versions[0] != modernProtocol {
				t.Fatalf("discover versions = %#v", versions)
			}
		}
	}
}

func TestBundledWidgetIsExactV11Payload(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	widget := filepath.Join(root, "assets", "optional", "mcp-servers", "ketcher-chemistry", "widget", "index.html.gz")
	html, err := loadWidgetHTML(widget, ExpectedWidgetSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if len(html) != 26190058 || !bytes.Contains(html, []byte("ketcher-chemistry")) {
		t.Fatalf("bundled Ketcher widget bytes = %d", len(html))
	}
	for _, appTool := range []string{"set_structure", "highlight_atoms", "get_structure"} {
		if !bytes.Contains(html, []byte(appTool)) {
			t.Fatalf("bundled Ketcher widget is missing dynamic app tool %q", appTool)
		}
	}
}

func TestServerFailsClosedForUnknownToolAndTamperedWidget(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delete_everything","arguments":{}}}` + "\n"
	var output bytes.Buffer
	if err := Run(context.Background(), Options{
		Input: strings.NewReader(input), Output: &output, WidgetGzipPath: filepath.Join(t.TempDir(), "missing.gz"),
	}); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Error *rpcError `json:"error"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != -32602 {
		t.Fatalf("unknown tool response = %s", output.String())
	}
}

func TestOpenSketcherUsesOneBoundedInputContract(t *testing.T) {
	result, err := OpenSketcher(map[string]any{
		"ket": `{"root":{"nodes":[]}}`, "filename": "empty.ket",
	})
	if err != nil {
		t.Fatal(err)
	}
	structured, _ := result["structuredContent"].(map[string]string)
	if structured["ket"] != `{"root":{"nodes":[]}}` {
		t.Fatalf("structured result=%#v", result)
	}
	content := result["content"].([]any)[0].(map[string]any)["text"]
	if content != "Opened molecule sketcher." {
		t.Fatalf("editable mount result=%#v", result)
	}
	htmlSensitive := strings.Repeat("<", 700*1024)
	if _, err := OpenSketcher(map[string]any{"ket": htmlSensitive}); err != nil {
		t.Fatalf("HTML-sensitive input within the JSON.stringify boundary was rejected: %v", err)
	}
	if bytes, valid := jsonStringUTF8Bytes(htmlSensitive); !valid || bytes != len(htmlSensitive)+2 {
		t.Fatalf("HTML-sensitive JSON string bytes=(%d,%v), want (%d,true)", bytes, valid, len(htmlSensitive)+2)
	}
	for name, arguments := range map[string]map[string]any{
		"unknown":  {"path": "/private/value"},
		"type":     {"ket": 7},
		"oversize": {"ket": strings.Repeat("x", maxMountInputBytes+1)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := OpenSketcher(arguments); err == nil {
				t.Fatal("expected bounded mount validation error")
			}
		})
	}
}
