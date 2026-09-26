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
)

const agentSavedPythonStaticValidator = `
import builtins
import json
import symtable
import sys

source = sys.stdin.read()
result = {"ok": False, "code": "invalid_python", "unresolved": [], "unresolved_count": 0}
try:
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
    result["unresolved_count"] = len(unresolved)
    result["unresolved"] = sorted(unresolved)[:32]
    if unresolved:
        result["code"] = "unresolved_global"
    else:
        result["ok"] = True
        result["code"] = "valid_python"
print(json.dumps(result, sort_keys=True))
`

type agentSavedPythonStaticValidation struct {
	OK              bool     `json:"ok"`
	Code            string   `json:"code"`
	Unresolved      []string `json:"unresolved"`
	UnresolvedCount int      `json:"unresolved_count"`
	Detail          string   `json:"detail,omitempty"`
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
	result, err := validateAgentSavedPythonReader(ctx, python, snapshot)
	if _, seekErr := snapshot.Seek(0, io.SeekStart); seekErr != nil {
		return errors.Join(err, seekErr)
	}
	if err != nil {
		return err
	}
	if result.OK && result.Code == "valid_python" && result.UnresolvedCount == 0 {
		return nil
	}
	detail := strings.TrimSpace(result.Detail)
	if len(result.Unresolved) > 0 {
		detail = "unresolved globals: " + strings.Join(result.Unresolved, ", ")
		if result.UnresolvedCount > len(result.Unresolved) {
			detail += fmt.Sprintf(" (and %d more)", result.UnresolvedCount-len(result.Unresolved))
		}
	}
	if detail == "" {
		detail = result.Code
	}
	return fmt.Errorf("%w: %s", errAgentSavedArtifactPythonInvalid, detail)
}

func validateAgentSavedPythonReader(ctx context.Context, python string, source io.Reader) (agentSavedPythonStaticValidation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	command := exec.Command(python, "-I", "-c", agentSavedPythonStaticValidator)
	command.Stdin = &contextReader{ctx: ctx, reader: source}
	command.Env = []string{"LANG=C.UTF-8", "PYTHONNOUSERSITE=1", "PYTHONUNBUFFERED=1"}
	output := &cappedScientificOutput{limit: maxScientificArtifactValidatorOutput}
	diagnostic := &cappedScientificOutput{limit: maxScientificArtifactValidatorOutput}
	command.Stdout, command.Stderr = output, diagnostic
	err := runArtifactValidationCommand(ctx, command, 0)
	if ctx.Err() != nil {
		return agentSavedPythonStaticValidation{}, ctx.Err()
	}
	if err != nil {
		return agentSavedPythonStaticValidation{}, fmt.Errorf("saved Python artifact validator failed: %w", err)
	}
	if output.overflow || diagnostic.overflow {
		return agentSavedPythonStaticValidation{}, errors.New("saved Python artifact validator result exceeded its diagnostic bound")
	}
	var result agentSavedPythonStaticValidation
	if err := json.Unmarshal([]byte(output.String()), &result); err != nil || strings.TrimSpace(result.Code) == "" || result.UnresolvedCount < len(result.Unresolved) || len(result.Unresolved) > 32 {
		return agentSavedPythonStaticValidation{}, errors.New("saved Python artifact validator returned an invalid result")
	}
	return result, nil
}
