package executionprep

import (
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// SingleShellCommand uses the same grammar as effect preparation, but requires
// an exact static argv. It never executes substitutions or reads the filesystem.
// Comments and formatting do not alter identity; side effects and expansions do.
func SingleShellCommand(source string) ([]string, bool) {
	if len(source) > MaxSourceBytes || strings.ContainsRune(source, 0) {
		return nil, false
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(source), "<execution>")
	if err != nil || len(file.Stmts) != 1 {
		return nil, false
	}
	statement := file.Stmts[0]
	return staticShellStatement(statement)
}

func staticShellStatement(statement *syntax.Stmt) ([]string, bool) {
	call, ok := statement.Cmd.(*syntax.CallExpr)
	if !ok || statement.Background || statement.Coprocess || statement.Negated || len(statement.Redirs) != 0 || len(call.Assigns) != 0 || len(call.Args) == 0 {
		return nil, false
	}
	args := make([]string, 0, len(call.Args))
	for _, word := range call.Args {
		if !staticShellWord(word.Parts, false) {
			return nil, false
		}
		values, err := expand.Fields(nil, word)
		if err != nil || len(values) != 1 || strings.ContainsRune(values[0], 0) {
			return nil, false
		}
		args = append(args, values[0])
	}
	return args, args[0] != ""
}

// ShellCommandInDirectory resolves one static invocation, optionally preceded
// by exactly `cd <literal> &&`. It does not evaluate substitutions or pretend
// to resolve working directories across arbitrary control-flow branches.
func ShellCommandInDirectory(source string) (string, []string, bool) {
	if args, ok := SingleShellCommand(source); ok {
		return "", args, true
	}
	if len(source) > MaxSourceBytes || strings.ContainsRune(source, 0) {
		return "", nil, false
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(source), "<execution>")
	if err != nil || len(file.Stmts) != 1 {
		return "", nil, false
	}
	statement := file.Stmts[0]
	chain, ok := statement.Cmd.(*syntax.BinaryCmd)
	if !ok || chain.Op != syntax.AndStmt || statement.Background || statement.Coprocess || statement.Negated || len(statement.Redirs) != 0 {
		return "", nil, false
	}
	left, ok := staticShellStatement(chain.X)
	if !ok || len(left) != 2 || left[0] != "cd" || left[1] == "" || strings.HasPrefix(left[1], "-") {
		return "", nil, false
	}
	right, ok := staticShellStatement(chain.Y)
	return left[1], right, ok
}

func staticShellWord(parts []syntax.WordPart, quoted bool) bool {
	for _, part := range parts {
		switch value := part.(type) {
		case *syntax.Lit:
			if quoted {
				continue
			}
			for i := 0; i < len(value.Value); i++ {
				if value.Value[i] == '\\' {
					i++
					continue
				}
				if strings.ContainsRune("*?[]{}~", rune(value.Value[i])) {
					return false
				}
			}
		case *syntax.SglQuoted:
		case *syntax.DblQuoted:
			if !staticShellWord(value.Parts, true) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// AppendShellArguments rebuilds the one admitted argv so a trailing comment
// cannot swallow runtime-owned parameters and parameter data cannot become code.
func AppendShellArguments(source string, extra []string) (string, bool) {
	args, ok := SingleShellCommand(source)
	if !ok {
		return "", false
	}
	args = append(args, extra...)
	for i, arg := range args {
		quoted, err := syntax.Quote(arg, syntax.LangBash)
		if err != nil {
			return "", false
		}
		args[i] = quoted
	}
	return strings.Join(args, " "), true
}
