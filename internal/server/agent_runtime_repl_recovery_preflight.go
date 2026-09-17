package server

import (
	"regexp"
	"sort"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

var (
	replRecoveryReceiverPattern     = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\s*(?:\.|\[)`)
	replRecoveryAssignmentPattern   = regexp.MustCompile(`(?m)^\s*([A-Za-z_][A-Za-z0-9_]*)\s*(?::[^=\n]+)?=`)
	replRecoveryImportPattern       = regexp.MustCompile(`(?m)^\s*(?:import\s+([A-Za-z_][A-Za-z0-9_]*)|from\s+[A-Za-z_][A-Za-z0-9_.]*\s+import\s+([A-Za-z_][A-Za-z0-9_]*))`)
	replRecoveryLoopVariablePattern = regexp.MustCompile(`\bfor\s+(?:[A-Za-z_][A-Za-z0-9_]*\s*,\s*)?([A-Za-z_][A-Za-z0-9_]*)\s+in\b`)
	replRecoveryWithVariablePattern = regexp.MustCompile(`(?m)^\s*(?:async\s+)?with\s+[^\n]+?\s+as\s+([A-Za-z_][A-Za-z0-9_]*)\s*:`)
	replRecoveryFunctionPattern     = regexp.MustCompile(`(?m)^[\t ]*(?:async\s+)?def\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
	replRecoveryIdentifierPattern   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// agentRuntimeREPLRecoveryStatePreflight prevents a resumed turn from relying
// on process-local variables that are not represented in its current cell.
// It is deliberately limited to receiver-style uses (x.get, x.attr, x[key]),
// which catches stale scientific result objects without pretending to be a
// full Python type checker.
func (g serverAgentRuntimeToolGateway) agentRuntimeREPLRecoveryStatePreflight(
	publicName string,
	input map[string]any,
) map[string]any {
	if !strings.EqualFold(strings.TrimSpace(publicName), "repl") || g.taskRun == nil ||
		g.taskRun.Transcript == nil || g.taskRun.Transcript.Claim.ResumeSource == transcriptstore.ResumeSourceFresh &&
		g.taskRun.Transcript.Claim.Attempt <= 1 && g.taskRun.Attempt <= 1 {
		return nil
	}
	missing := replRecoveryUndeclaredReceivers(stringValue(input["code"]))
	if len(missing) == 0 {
		return nil
	}
	return map[string]any{
		"ok": false, "status": "repl_recovery_state_preflight_required", "executed": false,
		"message":  "The resumed REPL cell references process-local variables that it does not define: " + strings.Join(missing, ", ") + ".",
		"recovery": "Define every referenced variable in this cell. Reissue one required read-only source or MCP call, or reconstruct exact values from a durable file/result; do not rely on variables from a pre-restart REPL process and do not replay side-effecting calls.",
	}
}

func replRecoveryUndeclaredReceivers(code string) []string {
	code = pythonCodeWithoutStringsOrComments(code)
	declared := map[string]struct{}{
		"true": {}, "false": {}, "none": {},
		// The managed scientific REPL injects host for every fresh kernel and
		// recovered cell; it is durable runtime authority, not stale user state.
		"host": {},
		// Built-in receiver types also exist in every fresh interpreter (for
		// example dict[str, int] annotations and str.maketrans).
		"dict": {}, "list": {}, "tuple": {}, "set": {}, "frozenset": {},
		"type": {}, "str": {}, "bytes": {}, "bytearray": {}, "int": {},
		"float": {}, "complex": {}, "bool": {}, "object": {}, "memoryview": {},
		// Receiver-style regex matching can see a keyword immediately before a
		// list or attribute expression (for example `... if cond else []`).
		// Python keywords can never identify process-local receiver state.
		"and": {}, "as": {}, "assert": {}, "async": {}, "await": {},
		"break": {}, "case": {}, "class": {}, "continue": {}, "def": {},
		"del": {}, "elif": {}, "else": {}, "except": {}, "finally": {},
		"for": {}, "from": {}, "global": {}, "if": {}, "import": {},
		"in": {}, "is": {}, "lambda": {}, "match": {}, "nonlocal": {},
		"not": {}, "or": {}, "pass": {}, "raise": {}, "return": {},
		"try": {}, "while": {}, "with": {}, "yield": {},
	}
	parameterScopes := replRecoveryParameterScopes(code)
	for _, match := range replRecoveryAssignmentPattern.FindAllStringSubmatchIndex(code, -1) {
		if !replRecoveryInsideParameterHeader(parameterScopes, match[2]) {
			declared[code[match[2]:match[3]]] = struct{}{}
		}
	}
	for _, match := range replRecoveryImportPattern.FindAllStringSubmatch(code, -1) {
		for _, name := range match[1:] {
			if name != "" {
				declared[name] = struct{}{}
			}
		}
	}
	for _, match := range replRecoveryLoopVariablePattern.FindAllStringSubmatch(code, -1) {
		declared[match[1]] = struct{}{}
	}
	for _, match := range replRecoveryWithVariablePattern.FindAllStringSubmatch(code, -1) {
		declared[match[1]] = struct{}{}
	}
	for _, match := range replRecoveryFunctionPattern.FindAllStringSubmatch(code, -1) {
		declared[match[1]] = struct{}{}
	}
	missingSet := map[string]struct{}{}
	for _, match := range replRecoveryReceiverPattern.FindAllStringSubmatchIndex(code, -1) {
		if len(match) < 4 || match[2] < 0 || match[3] < 0 {
			continue
		}
		// Receiver matches may start at a nested attribute (host.mcp.list_methods
		// also contains mcp.list_methods). Only the left-most base receiver can
		// depend on process-local state; an identifier immediately preceded by a
		// dot is already owned by the declared base object.
		if match[2] > 0 && code[match[2]-1] == '.' {
			continue
		}
		name := code[match[2]:match[3]]
		if _, found := declared[name]; !found && !replRecoveryParameterInScope(parameterScopes, name, match[2]) {
			missingSet[name] = struct{}{}
		}
	}
	missing := make([]string, 0, len(missingSet))
	for name := range missingSet {
		missing = append(missing, name)
	}
	sort.Strings(missing)
	return missing
}

func pythonCodeWithoutStringsOrComments(code string) string {
	out := []byte(code)
	quote := byte(0)
	triple := false
	escaped := false
	comment := false
	for index := 0; index < len(out); index++ {
		value := out[index]
		if comment {
			if value == '\n' {
				comment = false
			} else {
				out[index] = ' '
			}
			continue
		}
		if quote != 0 {
			if triple && index+2 < len(out) && out[index] == quote && out[index+1] == quote && out[index+2] == quote {
				out[index], out[index+1], out[index+2] = ' ', ' ', ' '
				index += 2
				quote, triple, escaped = 0, false, false
				continue
			}
			if !triple && !escaped && value == quote {
				out[index] = ' '
				quote = 0
				continue
			}
			if !triple && !escaped && value == '\\' {
				escaped = true
			} else {
				escaped = false
			}
			if value != '\n' {
				out[index] = ' '
			}
			continue
		}
		if value == '#' {
			comment = true
			out[index] = ' '
			continue
		}
		if value == '\'' || value == '"' {
			quote = value
			triple = index+2 < len(out) && out[index+1] == value && out[index+2] == value
			out[index] = ' '
			if triple {
				out[index+1], out[index+2] = ' ', ' '
				index += 2
			}
		}
	}
	return string(out)
}
