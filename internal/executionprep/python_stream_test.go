package executionprep

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestPythonPreparationStreamedResponseDataflow(t *testing.T) {
	for _, test := range []struct {
		name, source string
		wantDownload bool
	}{
		{"response iterator", `import requests
r = requests.get("https://example.org/data.tar", stream=True)
with open("data.tar", "wb") as output:
    for chunk in r.iter_content(chunk_size=8192):
        if chunk:
            output.write(chunk)`, true},
		{"aliased context and helper", `from requests import get as fetch
def persist(source, destination):
    with fetch(url=source, stream=True) as response:
        with open(destination, "wb") as output:
            for block in response.iter_content(4096):
                save_block(output, block)
def save_block(output, block):
    output.write(block)
persist(destination="data.tar", source="https://example.org/data.tar")`, true},
		{"iterator binding", `import httpx
response = httpx.get("https://example.org/data.tar")
blocks = response.iter_bytes()
with open("data.tar", "wb") as output:
    for block in blocks:
        copied = block
        output.write(copied)`, true},
		{"response iteration", `import urllib.request
with urllib.request.urlopen("https://example.org/data.txt") as response:
    with open("data.txt", "wb") as output:
        for line in response:
            output.write(line)`, true},
		{"raw stream copy", `import requests, shutil
response = requests.get("https://example.org/data.tar", stream=True)
with open("data.tar", "wb") as output:
    shutil.copyfileobj(response.raw, output)`, true},
		{"keyword-only writer helper", `import requests
def persist(*, reader, destination):
    chunks = reader.iter_content
    with open(destination, "wb") as output:
        for chunk in chunks(8192):
            output.write(chunk)
r = requests.get("https://example.org/data.tar", stream=True)
persist(destination="data.tar", reader=r)`, true},
		{"ordinary API analysis", `import requests
response = requests.get("https://example.org/events", stream=True)
for line in response.iter_lines():
    print(line)
with open("summary.txt", "w") as output:
    output.write("computed summary")`, false},
		{"rebound local chunk", `import requests
r = requests.get("https://example.org/data.tar", stream=True)
with open("output.bin", "wb") as output:
    for chunk in r.iter_content(4096):
        chunk = b"local-data"
        output.write(chunk)`, false},
		{"rebound response", `import requests
r = requests.get("https://example.org/data.tar", stream=True)
r = local_response()
with open("output.bin", "wb") as output:
    for chunk in r.iter_content(4096):
        output.write(chunk)`, false},
		{"unknown loop shadows old response bytes", `import requests
r = requests.get("https://example.org/data.tar")
chunk = r.content
with open("local.bin", "wb") as output:
    for chunk in local_chunks():
        output.write(chunk)`, false},
		{"unresolved helper parameter shadows global", `import requests
response = requests.get("https://example.org/data.tar")
def local_copy(response):
    with open("local.bin", "wb") as output:
        for chunk in response.iter_content(8192):
            output.write(chunk)
local_copy(response=local_response())`, false},
		{"unused helper", `import requests
def unused():
    with requests.get("https://example.org/data.tar", stream=True) as r:
        with open("data.tar", "wb") as f:
            for chunk in r.iter_content(4096):
                f.write(chunk)
print("not downloading")`, false},
		{"authenticated request", `import requests
r = requests.get("https://example.org/data.tar", stream=True, auth=credentials)
with open("data.tar", "wb") as f:
    for chunk in r.iter_content(4096):
        f.write(chunk)`, false},
		{"local response", `import requests
r = requests.get("http://127.0.0.1/data.tar", stream=True)
with open("data.tar", "wb") as f:
    for chunk in r.iter_content(4096):
        f.write(chunk)`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := analyzePythonSourceForTest(t, test.source)
			found := false
			for _, requirement := range result.Requirements {
				if requirement.Effect == FileAcquisition {
					found = true
					if requirement.Authority != "durable_download" || requirement.Mechanism != "file.write_http" {
						t.Fatalf("competing stream authority: %#v", requirement)
					}
				}
			}
			if found != test.wantDownload {
				t.Fatalf("download=%t want=%t result=%#v", found, test.wantDownload, result)
			}
		})
	}
}

func analyzePythonSourceForTest(t *testing.T, source string) Result {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := Analyze(ctx, Request{Language: "python", Source: source},
		func(ctx context.Context, language, source string) ([]Fact, error) {
			command := exec.CommandContext(ctx, python, "-I", "-S", "-c", PythonParser)
			command.Stdin = strings.NewReader(source)
			output, err := command.Output()
			if err != nil {
				return nil, err
			}
			return DecodeNative(language, output)
		})
	if err != nil || len(result.Unresolved) != 0 {
		t.Fatalf("native Python analysis=%#v err=%v", result, err)
	}
	return result
}
