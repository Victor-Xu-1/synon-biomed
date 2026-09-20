package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"synon-go/internal/executionprep"
	kernelruntime "synon-go/internal/kernel"
	"synon-go/internal/skills"
)

func observationPreflightManager(t *testing.T, language string) *kernelruntime.Manager {
	t.Helper()
	config := kernelruntime.Config{}
	if language == "python" {
		var err error
		config.Python, err = exec.LookPath("python3")
		if err != nil {
			t.Skip("native Python unavailable")
		}
	}
	if language == "r" {
		interpreter := os.Getenv("SYNON_TEST_RSCRIPT")
		if interpreter == "" {
			t.Skip("set SYNON_TEST_RSCRIPT to an existing native Rscript")
		}
		prefix := filepath.Dir(filepath.Dir(interpreter))
		config.CondaEnvsPath, config.DefaultREnv = filepath.Dir(prefix), filepath.Base(prefix)
	}
	if language == "powershell" {
		if _, err := executionprep.NativePowerShell(); err != nil {
			t.Skip(err)
		}
	}
	return kernelruntime.NewManager(config)
}

// Exercise the production composition, not just the implementation-choice
// helper: observation must leave every other preparation boundary in place.
func TestImplementationChoiceDiagnosticComposition(t *testing.T) {
	for _, test := range []struct{ name, language, source string }{
		{"python output", "python", `print(1)`},
		{"python literal expressions", "python", "1\n('environment', 2, True)"},
		{"python quoted comments multiline", "python", "# inspection only\nprint(\n 'open(\"x\", \"w\")', 2\n)\nprint('ready')"},
		{"bash directory", "bash", "pwd"},
		{"bash literal output", "bash", "printf '%s\\n' 'ready'"},
		{"bash quoted comments multiline", "bash", "# inspection only\npwd\nprintf '%s\\n' '$(touch output)'\npwd -P"},
		{"r directory", "r", "getwd()"},
		{"r literal expressions", "r", "1\n'ready'"},
		{"r quoted comments multiline", "r", "# inspection only\ngetwd(\n)\n'writeLines(1)'\ngetwd()"},
		{"powershell directory", "powershell", "Get-Location"},
		{"powershell literal expressions", "powershell", "1\n'ready'"},
		{"powershell quoted comments multiline", "powershell", "# inspection only\nGet-Location\n'$(Set-Content output data)'\nGet-Location"},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := &sessionRunnerChatRun{TaskIntent: "Prepare the machine inventory before choosing an implementation."}
			run.addRequiredScientificCapabilities("pocket-conditioned-molecule-generation")
			run.setImplementationSelectionRequired(true)
			capabilities := run.requiredScientificCapabilitiesSnapshot()
			gateway := serverAgentRuntimeToolGateway{
				server:  &Server{skillCatalog: skills.NewCatalog(), kernelManager: observationPreflightManager(t, test.language)},
				taskRun: run,
			}
			input := map[string]any{"code": test.source}
			if test.language == "bash" || test.language == "powershell" {
				input = map[string]any{"command": test.source}
			}
			result := gateway.agentRuntimeSkillExecutionContractPreflight(test.language, input)
			if status := stringValue(result["status"]); status == "implementation_selection_required" || status == "implementation_skill_required" {
				t.Errorf("diagnostic was mistaken for a scientific implementation: %#v", result)
			}
			if !run.implementationSelectionRequiredSnapshot() || len(run.selectedImplementationsSnapshot()) != 0 ||
				!reflect.DeepEqual(capabilities, run.requiredScientificCapabilitiesSnapshot()) {
				t.Fatal("diagnostic changed durable selection or required capabilities")
			}
		})
	}
}

func TestImplementationChoiceUnprovedEffectsRemainGated(t *testing.T) {
	for _, test := range []struct{ name, language, source string }{
		{"python unknown binding", "python", "observe(1)"},
		{"python dynamic call", "python", "globals()['print'](1)"},
		{"python rebound output", "python", "print = lambda x: open('output', 'w').write(str(x))\nprint(1)"},
		{"python file write", "python", "open('output', 'w').write('x')"},
		{"python network", "python", "import urllib.request\nurllib.request.urlopen('https://example.org')"},
		{"python install", "python", "import subprocess\nsubprocess.run(['pip', 'install', 'example'])"},
		{"python scientific call", "python", "from rdkit import Chem\nChem.MolFromSmiles('CCO')"},
		{"python script", "python", "exec(open('analysis.py').read())"},
		{"bash unknown binding", "bash", "observe"},
		{"bash function shadow", "bash", "pwd() { touch output; }; pwd"},
		{"bash alias", "bash", "alias pwd='touch output'; pwd"},
		{"bash expansion", "bash", "printf '%s' \"$(touch output)\""},
		{"bash redirect", "bash", "pwd > output"},
		{"bash network", "bash", "curl https://example.org"},
		{"bash install", "bash", "pip install example"},
		{"bash script", "bash", "bash analysis.sh"},
		{"r unknown binding", "r", "observe()"},
		{"r rebound directory", "r", "getwd <- function() writeLines('x', 'output'); getwd()"},
		{"r dynamic call", "r", "do.call('getwd', list())"},
		{"r write", "r", "writeLines('x', 'output')"},
		{"r network", "r", "url('https://example.org')"},
		{"r install", "r", "install.packages('example')"},
		{"r scientific call", "r", "stats::lm(y ~ x)"},
		{"r script", "r", "source('analysis.R')"},
		{"powershell unknown binding", "powershell", "Observe-State"},
		{"powershell rebound directory", "powershell", "function Get-Location { Set-Content output x }; Get-Location"},
		{"powershell alias", "powershell", "Set-Alias Get-Location Set-Content; Get-Location output x"},
		{"powershell dynamic call", "powershell", "& $command"},
		{"powershell write", "powershell", "Get-Location > output"},
		{"powershell network", "powershell", "Invoke-WebRequest https://example.org"},
		{"powershell install", "powershell", "Install-Module example"},
		{"powershell script", "powershell", ". ./analysis.ps1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := &sessionRunnerChatRun{}
			run.addRequiredScientificCapabilities("pocket-conditioned-molecule-generation")
			gateway := serverAgentRuntimeToolGateway{server: &Server{skillCatalog: skills.NewCatalog(), kernelManager: observationPreflightManager(t, test.language)}, taskRun: run}
			input := map[string]any{"code": test.source}
			if test.language == "bash" || test.language == "powershell" {
				input = map[string]any{"command": test.source}
			}
			// Model claims are never evidence of effect or runtime binding.
			input["read_only"] = true
			result := gateway.agentRuntimeSkillExecutionContractPreflight(test.language, input)
			if stringValue(result["status"]) != "implementation_selection_required" || boolValue(result["executed"], true) {
				t.Fatalf("unproved execution escaped the selection gate: %#v", result)
			}
		})
	}
}
