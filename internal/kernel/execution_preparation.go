package kernel

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"

	"synon-go/internal/executionprep"
)

// PrepareExecutionSource shares one effect plan across direct language calls,
// embedded languages and workspace script entries. This is bounded static
// preparation, not a sandbox claim or execution-success witness.
func (m *Manager) PrepareExecutionSource(ctx context.Context, request executionprep.Request) (executionprep.Result, error) {
	return executionprep.Analyze(ctx, request, func(ctx context.Context, language, source string) ([]executionprep.Fact, error) {
		if m == nil {
			return nil, errors.New("source parser runtime unavailable")
		}
		executable, prefix, script := m.config.Python, "", executionprep.PythonParser
		arguments := []string{"-I", "-c", script}
		if language == "r" {
			environment := request.Environment
			if environment == "" {
				environment = m.config.DefaultREnv
			}
			var err error
			prefix, executable, err = m.managedEnvironmentRuntime(environment, "Rscript")
			if err != nil {
				return nil, err
			}
			arguments = []string{"--vanilla", "-e", executionprep.RParser}
		} else if language == "powershell" {
			var err error
			executable, err = executionprep.NativePowerShell()
			if err != nil {
				return nil, err
			}
			arguments = []string{"-NoProfile", "-NonInteractive", "-Command", executionprep.PowerShellParser()}
			source = base64.StdEncoding.EncodeToString([]byte(source))
		} else if language != "python" {
			return nil, errors.New("source parser language unavailable")
		}
		command := newWorkerProcessCommand(ctx, executable, arguments...)
		if prefix != "" {
			command.Env = managedEnvironmentRuntimeEnv(prefix)
		}
		command.Stdin = strings.NewReader(source)
		stdout, stderr := newTailBuffer(4*executionprep.MaxSourceBytes), newTailBuffer(maxDiagnosticBytes)
		command.Stdout, command.Stderr = stdout, stderr
		runErr := runWorkerProcess(command)
		facts, decodeErr := executionprep.DecodeNative(language, []byte(stdout.String()))
		if runErr != nil {
			return facts, errors.New("native source parser did not complete")
		}
		return facts, decodeErr
	})
}

// Called only while executeMu is held, after the existing session identity and
// generation checks. It never queries a possibly polluted guest namespace.
func (w *Worker) validateObservationExecution(request SubmitRequest) string {
	plan := request.Observation
	if plan == nil {
		return ""
	}
	if w.observationTainted {
		return "runtime_binding_provenance_unproved"
	}
	source := request.Code
	if plan.Language == "bash" {
		// The server's one canonical Bash renderer binds its generated code to
		// the proven original command. Do not reverse-parse the Python envelope
		// here or import the server-facing kernel contract back into the kernel.
		if request.KernelKind != "bash" || request.ToolName != "bash" || request.Language != "python" ||
			request.ObservationCodeSHA256 == "" || request.ObservationCodeSHA256 != executionprep.SourceSHA256(request.Code) {
			return "diagnostic_shell_startup_unproved"
		}
		return ""
	} else if plan.Language != request.Language || (plan.Language != "python" && plan.Language != "r") {
		return "diagnostic_language_binding_mismatch"
	}
	if !plan.Matches(plan.Language, source) {
		return "diagnostic_source_binding_mismatch"
	}
	return ""
}

func observationExecutionDigest(request SubmitRequest) string {
	if request.Observation == nil {
		return ""
	}
	return executionprep.SourceSHA256(request.Code)
}
