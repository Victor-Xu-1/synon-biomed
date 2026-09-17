package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const agentSavedPythonStaticValidationTimeout = 10 * time.Second

const agentSavedPythonStaticValidator = `
import ast
import builtins
import json
import symtable
import sys

source = sys.stdin.read()
result = {"ok": False, "code": "invalid_python", "unresolved": []}
try:
    ast.parse(source, filename="artifact.py", mode="exec")
    root = symtable.symtable(source, "artifact.py", "exec")
except (SyntaxError, ValueError) as exc:
    result["code"] = "syntax_error"
    result["detail"] = str(exc)
else:
    module_definitions = {
        symbol.get_name()
        for symbol in root.get_symbols()
        if symbol.is_assigned() or symbol.is_imported() or symbol.is_parameter() or symbol.is_namespace()
    }
    allowed = set(dir(builtins)) | {"__name__", "__file__", "__package__", "__spec__"}
    unresolved = set()

    def visit(table):
        for symbol in table.get_symbols():
            name = symbol.get_name()
            if symbol.is_referenced() and symbol.is_global() and name not in module_definitions and name not in allowed:
                unresolved.add(name)
        for child in table.get_children():
            visit(child)

    visit(root)
    result["unresolved"] = sorted(unresolved)
    if unresolved:
        result["code"] = "unresolved_global"
    else:
        result["ok"] = True
        result["code"] = "valid_python"
print(json.dumps(result, sort_keys=True))
`

type agentSavedPythonStaticValidation struct {
	OK         bool     `json:"ok"`
	Code       string   `json:"code"`
	Unresolved []string `json:"unresolved"`
	Detail     string   `json:"detail,omitempty"`
}

func (s *Server) validateAgentSavedPythonArtifact(ctx context.Context, relativePath string, snapshot *os.File) error {
	if !strings.EqualFold(filepath.Ext(strings.TrimSpace(relativePath)), ".py") || snapshot == nil {
		return nil
	}
	if s == nil || s.kernelManager == nil {
		return errors.New("saved Python artifact validation is unavailable")
	}
	python, _, _, _, err := s.kernelManager.ScientificArtifactValidator()
	if err != nil || strings.TrimSpace(python) == "" {
		return errors.New("saved Python artifact validation is unavailable")
	}
	if _, err := snapshot.Seek(0, io.SeekStart); err != nil {
		return err
	}
	source, err := io.ReadAll(io.LimitReader(snapshot, maxRunnerCrossArtifactScanBytes+1))
	if err != nil {
		return err
	}
	if _, seekErr := snapshot.Seek(0, io.SeekStart); seekErr != nil {
		return seekErr
	}
	if len(source) > maxRunnerCrossArtifactScanBytes {
		return errors.New("saved Python artifact exceeds the static validation bound")
	}
	result, err := validateAgentSavedPythonSource(ctx, python, source)
	if err != nil {
		return err
	}
	if result.OK && result.Code == "valid_python" && len(result.Unresolved) == 0 {
		return nil
	}
	detail := strings.TrimSpace(result.Detail)
	if len(result.Unresolved) > 0 {
		detail = "unresolved globals: " + strings.Join(result.Unresolved, ", ")
	}
	if detail == "" {
		detail = result.Code
	}
	return fmt.Errorf("%w: %s", errAgentSavedArtifactPythonInvalid, detail)
}

func validateAgentSavedPythonSource(ctx context.Context, python string, source []byte) (agentSavedPythonStaticValidation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	validationContext, cancel := context.WithTimeout(ctx, agentSavedPythonStaticValidationTimeout)
	defer cancel()
	command := exec.CommandContext(validationContext, python, "-I", "-c", agentSavedPythonStaticValidator)
	command.Stdin = strings.NewReader(string(source))
	command.Env = []string{"LANG=C.UTF-8", "PYTHONNOUSERSITE=1", "PYTHONUNBUFFERED=1"}
	output, err := command.Output()
	if validationContext.Err() != nil {
		return agentSavedPythonStaticValidation{}, errors.New("saved Python artifact validation timed out")
	}
	if err != nil {
		return agentSavedPythonStaticValidation{}, errors.New("saved Python artifact validator failed")
	}
	var result agentSavedPythonStaticValidation
	if err := json.Unmarshal(output, &result); err != nil || strings.TrimSpace(result.Code) == "" {
		return agentSavedPythonStaticValidation{}, errors.New("saved Python artifact validator returned an invalid result")
	}
	return result, nil
}
