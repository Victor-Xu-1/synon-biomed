package kernel

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"synon-go/internal/executionprep"
)

func TestExecutionPreparationUsesRealParsersAcrossEntryPoints(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Config{Python: python})
	for _, test := range []struct{ name, language, source, effect string }{
		{"shell quoted installer", "bash", `env LANG=C "pip" install arbitrary-package`, executionprep.PackageMutation},
		{"shell quoted data", "bash", `printf '%s' 'pip install arbitrary-package'`, ""},
		{"shell comment", "bash", "# pip install package\nprintf ok", ""},
		{"shell unused helper", "bash", `install_helper() { pip install arbitrary-package; }; printf ok`, ""},
		{"shell invoked helper", "bash", `install_helper() { pip install arbitrary-package; }; install_helper`, executionprep.PackageMutation},
		{"shell command substitution", "bash", `printf '%s' "$(pip install package-name)"`, executionprep.PackageMutation},
		{"shell interpreter", "bash", `python -c 'import os; os.system("pip install package-name")'`, executionprep.PackageMutation},
		{"python alias", "python", `import subprocess as child
args=["python", "-m", "pip", "install", "package-name"]
child.run(args, check=True)`, executionprep.PackageMutation},
		{"python R API alias", "python", `from rpy2.robjects.packages import importr as load
utils=load("utils")
utils.install_packages("package-name")`, executionprep.PackageMutation},
		{"python ordinary computation", "python", `import subprocess
subprocess.run(["solver", "--input", "data.csv"], check=True)`, ""},
		{"python data string", "python", `import subprocess
description="pip install package-name"
print(description)`, ""},
		{"python unused definition", "python", `def install():
    import os
    os.system("pip install arbitrary-package")
print("not called")`, ""},
		{"python helper invocation", "python", `def invoke(command):
    import subprocess
    subprocess.run(command)
invoke(["pip", "install", "package-name"])`, executionprep.PackageMutation},
		{"shell file acquisition", "bash", `curl --output=data.csv https://example.org/data.csv`, executionprep.FileAcquisition},
		{"shell redirected acquisition", "bash", `curl https://example.org/data.csv > data.csv`, executionprep.FileAcquisition},
		{"shell API query", "bash", `curl -sS https://example.org/api/status`, ""},
		{"python remote dataframe", "python", `import pandas as table
uri="https://example.org/matrix.csv"
data=table.read_csv(uri)`, executionprep.FileAcquisition},
		{"python bytes write", "python", `import requests as client
from pathlib import Path
response=client.get("https://example.org/matrix.gz")
Path("matrix.gz").write_bytes(response.content)`, executionprep.FileAcquisition},
		{"python response stream to file", "python", `import requests
response=requests.get("https://example.org/data.csv")
with open("data.csv","wb") as output:
    output.write(response.content)`, executionprep.FileAcquisition},
		{"python API query", "python", `import requests
response=requests.get("https://example.org/api/status")
print(response.json())`, ""},
		{"python local dataframe", "python", `import pandas as pd
data=pd.read_csv("input.csv")`, ""},
		{"python authenticated transfer", "python", `import requests
response=requests.get("https://example.org/file", headers={"Authorization":"credential-placeholder"})
open("file","wb").write(response.content)`, ""},
		{"python localhost access", "python", `import pandas as pd
data=pd.read_csv("http://127.0.0.1/table.csv")`, ""},
		{"python standard library transfer", "python", `from urllib.request import urlopen
open("file","wb").write(urlopen("https://example.org/file").read())`, executionprep.FileAcquisition},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := manager.PrepareExecutionSource(context.Background(), executionprep.Request{Language: test.language, Source: test.source})
			if err != nil {
				t.Fatal(err)
			}
			if test.effect == "" && len(result.Requirements) != 0 {
				t.Fatalf("ordinary execution was redirected: %#v", result)
			}
			if test.effect != "" && (len(result.Requirements) != 1 || result.Requirements[0].Effect != test.effect) {
				t.Fatalf("effect not resolved: %#v", result)
			}
		})
	}
}

func TestExecutionPreparationTracksActualScriptAndNeverExecutesIt(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(Config{Python: python})
	root := t.TempDir()
	marker := filepath.Join(root, "must-not-exist")
	script := filepath.Join(root, "analysis.py")
	if err := os.WriteFile(script, []byte("import subprocess\nopen("+strconv.Quote(marker)+", 'w').write('unsafe')\nsubprocess.run(['pip','install','package-name'])"), 0600); err != nil {
		t.Fatal(err)
	}
	request := executionprep.Request{Language: "bash", Source: `python "analysis.py"`, WorkspaceRoot: root, WorkingDir: root}
	before, err := manager.PrepareExecutionSource(context.Background(), request)
	if err != nil || len(before.Requirements) != 1 || len(before.ReadFiles) != 1 {
		t.Fatalf("script preparation: %#v %v", before, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("preparation executed user source")
	}
	if err := os.WriteFile(script, []byte("print(42)"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := manager.PrepareExecutionSource(context.Background(), request)
	if err != nil || len(after.Requirements) != 0 || len(after.ReadFiles) != 1 || after.ReadFiles[0].SHA256 == before.ReadFiles[0].SHA256 {
		t.Fatalf("changed script reused stale decision: %#v %v", after, err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.py"), []byte("print('private')"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skip("symlink fixture unavailable")
	}
	request.Source = "python escape/secret.py"
	result, err := manager.PrepareExecutionSource(context.Background(), request)
	if err != nil || len(result.ReadFiles) != 0 || len(result.Unresolved) == 0 {
		t.Fatalf("script escaped workspace: %#v %v", result, err)
	}
}
