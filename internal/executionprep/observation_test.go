package executionprep

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func observationNativeParser(t *testing.T, language string) NativeParser {
	t.Helper()
	var executable string
	var arguments []string
	var err error
	switch language {
	case "python":
		executable, err = exec.LookPath("python3")
		arguments = []string{"-I", "-c", PythonParser}
	case "r":
		executable = os.Getenv("SYNON_TEST_RSCRIPT")
		if executable == "" {
			executable, err = exec.LookPath("Rscript")
		}
		arguments = []string{"--vanilla", "-e", RParser}
	case "powershell":
		executable, err = NativePowerShell()
		arguments = []string{"-NoProfile", "-NonInteractive", "-Command", PowerShellParser()}
	case "bash":
		return nil
	}
	if err != nil || executable == "" {
		t.Skipf("native %s parser unavailable: %v", language, err)
	}
	return func(ctx context.Context, requested, source string) ([]Fact, error) {
		if requested != language {
			return nil, errors.New("nested language is unproved in this fixture")
		}
		if language == "powershell" {
			source = base64.StdEncoding.EncodeToString([]byte(source))
		}
		command := exec.CommandContext(ctx, executable, arguments...)
		command.Stdin = strings.NewReader(source)
		output, err := command.Output()
		if err != nil {
			return nil, err
		}
		return DecodeNative(language, output)
	}
}

func TestObservationNativeASTCoverage(t *testing.T) {
	for _, suite := range []struct {
		language           string
		positive, negative []string
	}{
		{"python", []string{
			`print(1)`, "# observation\nprint(\n 'open(\"x\",\"w\")', [1, (2, True)], sep='|', end='\\n', flush=True)\nprint('ready')",
			"1\n('ready', None, -2, +3.5)\n[True, b'bytes']",
		}, []string{
			`observe()`, `globals()['print'](1)`, `print(value)`, `print(*(1,))`, `print(1, **{})`, `print(1, file=output)`,
			`print(f'{value}')`, `print(1 + value)`, `print(1, end=observe())`, `print(1, sep='a', sep='b')`,
			`print(1); engine()`, "print = lambda x: x\nprint(1)", "# print(1)\nimport rdkit", `Chem.MolFromSmiles('CCO')`,
			`open('out','w').write('x')`, `exec(open('script.py').read())`, "1\nif", `lambda: print(1)`,
		}},
		{"r", []string{
			`getwd()`, "# observation\ngetwd(\n)\n'Sys.setenv(X=1)'\nSys.getpid()\nSys.info()", "1\n'quoted'\nNULL",
		}, []string{
			`observe()`, `get('getwd')()`, `do.call('getwd', list())`, `getwd <- function() 1; getwd()`,
			`base::getwd()`, `print(value)`, `Sys.getenv()`, `getwd(x)`, `getwd(); stats::lm(y~x)`,
			`source('analysis.R')`, `writeLines('x', 'output')`, `url('https://example.org')`, `install.packages('example')`,
		}},
		{"bash", []string{
			`pwd`, "# observation\npwd -P\n'printf' '%s\\n' '$(touch output)'\necho ready", `p"w"d; printf 'value=%04d\n' 7`,
		}, []string{
			`observe`, `pwd() { touch output; }; pwd`, `alias pwd=touch; pwd`, `pwd > out`, `pwd < in`, `pwd &`,
			`! pwd`, `pwd | cat`, `pwd && echo ok`, `echo "$(touch out)"`, `echo *`, `echo ~`, `echo {a,b}`, `echo $value`,
			`printf -v output '%s' x`, `printf '%n' output`, `printf '%30n' output`, `printf '%(x)T' 0`,
			`printf '%s' <(touch output)`, "echo `touch output`", `bash analysis.sh`, `python -c 'print(1)'`,
		}},
		{"powershell", []string{
			`Get-Location`, "# observation\nGet-Location\n'$(Set-Content output x)'\nWrite-Output 'ready' 1",
			"1\n'ready'\nGet-Location; Write-Output 'quoted string'",
		}, []string{
			`Observe-State`, `& $command`, `& 'Get-Location'`, `Get-Location | Write-Output`, `Get-Location > out`,
			`Get-Location -Stack`, `Write-Output $value`, `Write-Output "$(Set-Content out x)"`, `Write-Output @values`,
			`function Get-Location { 1 }; Get-Location`, `Set-Alias Get-Location Write-Output; Get-Location`,
			`Get-Location; Invoke-WebRequest https://example.org`, `. ./analysis.ps1`, `Install-Module example`,
			`[IO.File]::WriteAllText('out','x')`, `trap { Write-Output x }; Get-Location`,
		}},
	} {
		t.Run(suite.language, func(t *testing.T) {
			parser := observationNativeParser(t, suite.language)
			for _, cases := range []struct {
				sources  []string
				observed bool
			}{{suite.positive, true}, {suite.negative, false}} {
				for _, source := range cases.sources {
					t.Run(source, func(t *testing.T) {
						ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
						defer cancel()
						result, err := Analyze(ctx, Request{Language: suite.language, Source: source}, parser)
						if err != nil {
							t.Fatal(err)
						}
						if got := result.Observation.Matches(suite.language, source); got != cases.observed {
							t.Fatalf("observation=%v want=%v plan=%#v", got, cases.observed, result)
						}
					})
				}
			}
		})
	}
}

func TestObservationNeedsPositiveCoverageAndExactSource(t *testing.T) {
	for _, parser := range []NativeParser{
		nil,
		func(context.Context, string, string) ([]Fact, error) { return nil, nil },
		func(context.Context, string, string) ([]Fact, error) {
			return []Fact{{Kind: "call", Name: "scientific.engine"}}, nil
		},
		func(context.Context, string, string) ([]Fact, error) {
			return []Fact{{Kind: "observation", Name: ObservationSchema, Args: []string{"python.output"}}}, errors.New("incomplete parser")
		},
	} {
		result, err := Analyze(context.Background(), Request{Language: "python", Source: "print(1)"}, parser)
		if err != nil || result.Observation != nil {
			t.Fatalf("missing/partial evidence proved observation: %#v %v", result, err)
		}
	}
	result, err := Analyze(context.Background(), Request{Language: "bash", Source: "pwd"}, nil)
	if err != nil || !result.Observation.Matches("bash", "pwd") || result.Observation.Matches("bash", "pwd; engine") || result.Observation.Matches("python", "pwd") {
		t.Fatalf("source/language binding is not exact: %#v %v", result, err)
	}
	ctx := WithObservation(context.Background(), result.Observation)
	result.Observation.Operations[0] = "python.output"
	first := ObservationFromContext(ctx)
	if !first.Matches("bash", "pwd") {
		t.Fatal("caller mutation altered host context")
	}
	first.Operations[0] = "python.output"
	if !ObservationFromContext(ctx).Matches("bash", "pwd") {
		t.Fatal("consumer mutation altered host context")
	}
	if ObservationFromContext(context.WithValue(context.Background(), "observation", first)) != nil {
		t.Fatal("a model-like string key forged the proof")
	}
}
