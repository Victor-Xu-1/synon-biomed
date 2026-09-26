package executionprep

import (
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// This grammar covers whole statements, not matching command strings. Its
// operation registry is paired with the executor's native builtin/startup guard.
func shellObservation(file *syntax.File) []string {
	if len(file.Stmts) == 0 || len(file.Stmts) > MaxFacts {
		return nil
	}
	var operations []string
	for _, statement := range file.Stmts {
		if statement.Background || statement.Negated || statement.Coprocess || len(statement.Redirs) != 0 {
			return nil
		}
		call, ok := statement.Cmd.(*syntax.CallExpr)
		if !ok || len(call.Assigns) != 0 || len(call.Args) == 0 || len(call.Args) > MaxFacts {
			return nil
		}
		args := make([]string, len(call.Args))
		for i, word := range call.Args {
			var literal bool
			args[i], literal = observationShellWord(word.Parts, false)
			if !literal {
				return nil
			}
		}
		switch args[0] {
		case "pwd":
			if len(args) > 2 || (len(args) == 2 && args[1] != "-L" && args[1] != "-P") {
				return nil
			}
			operations = append(operations, "bash.directory")
		case "echo":
			operations = append(operations, "bash.output")
		case "printf":
			format := 1
			if len(args) > 1 && args[1] == "--" {
				format++
			}
			if len(args) <= format || strings.HasPrefix(args[format], "-") || !observationPrintfFormat(args[format]) {
				return nil
			}
			operations = append(operations, "bash.format")
		default:
			return nil
		}
	}
	return operations
}

func observationShellWord(parts []syntax.WordPart, quoted bool) (string, bool) {
	var literal strings.Builder
	for _, part := range parts {
		switch node := part.(type) {
		case *syntax.Lit:
			// Expansion, escaping and glob/brace/tilde syntax need their own
			// contracts. Plain quoted data can contain apparent commands safely.
			if strings.ContainsAny(node.Value, "\\") || (!quoted && strings.ContainsAny(node.Value, "*?[]{}~$`")) {
				return "", false
			}
			literal.WriteString(node.Value)
		case *syntax.SglQuoted:
			if node.Dollar {
				return "", false
			}
			literal.WriteString(node.Value)
		case *syntax.DblQuoted:
			if node.Dollar {
				return "", false
			}
			value, ok := observationShellWord(node.Parts, true)
			if !ok {
				return "", false
			}
			literal.WriteString(value)
		default:
			return "", false
		}
	}
	return literal.String(), true
}

var observationFormatConversion = regexp.MustCompile(`^%[-+ #0]*[0-9]{0,3}(\.[0-9]{1,3})?[sqbdiuoxX]`)

func observationPrintfFormat(format string) bool {
	for offset := 0; offset < len(format); offset++ {
		if format[offset] != '%' {
			continue
		}
		if offset+1 < len(format) && format[offset+1] == '%' {
			offset++
			continue
		}
		conversion := observationFormatConversion.FindString(format[offset:])
		if conversion == "" {
			// In particular %n assigns a variable and is not observation.
			return false
		}
		offset += len(conversion) - 1
	}
	return true
}
