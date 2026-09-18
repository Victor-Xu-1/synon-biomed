package kernel

import (
	"context"
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
