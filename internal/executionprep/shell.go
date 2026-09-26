package executionprep

import (
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// ParseShell consumes grammar nodes; quoted examples, comments and heredocs
// passed as data are not mistaken for executable commands.
func ParseShell(source string) ([]Fact, error) {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(source), "<execution>")
	if err != nil {
		return nil, err
	}
	var facts []Fact
	if operations := shellObservation(file); len(operations) != 0 {
		facts = append(facts, Fact{Kind: "observation", Name: ObservationSchema, Args: operations})
	}
	bindings := make(map[string]string)
	functions := make(map[string]*syntax.Stmt)
	var walk func(syntax.Node, int)
	walk = func(root syntax.Node, depth int) {
		if depth > maxDepth {
			return
		}
		syntax.Walk(root, func(node syntax.Node) bool {
			if len(facts) >= MaxFacts {
				return false
			}
			if function, ok := node.(*syntax.FuncDecl); ok {
				if function.Name != nil {
					functions[function.Name.Value] = function.Body
				}
				return false
			}
			statement, ok := node.(*syntax.Stmt)
			if !ok {
				return true
			}
			call, ok := statement.Cmd.(*syntax.CallExpr)
			if !ok {
				return true
			}
			for _, assignment := range call.Assigns {
				if assignment.Name != nil && assignment.Value != nil {
					if value, ok := shellLiteral(assignment.Value, bindings); ok {
						bindings[assignment.Name.Value] = value
					} else {
						delete(bindings, assignment.Name.Value)
					}
				}
			}
			args := make([]string, 0, len(call.Args))
			for _, word := range call.Args {
				value, ok := shellLiteral(word, bindings)
				if !ok {
					value = ""
				}
				args = append(args, value)
			}
			children := ProcessFacts(args, int(statement.Pos().Line()))
			facts = append(facts, children...)
			if len(args) > 0 {
				if function := functions[args[0]]; function != nil {
					walk(function, depth+1)
				}
				if filepath.Base(args[0]) == "curl" {
					toFile := false
					for _, redirect := range statement.Redirs {
						if redirect.Op == syntax.RdrOut || redirect.Op == syntax.AppOut {
							toFile = true
						}
					}
					if toFile {
						for _, argument := range args[1:] {
							if publicURL(argument) {
								facts = append(facts, Fact{Kind: "call", Name: "process.file_download", Args: []string{argument}, Line: int(statement.Pos().Line())})
								break
							}
						}
					}
				}
			}
			// A literal heredoc is code only when it is the interpreter's stdin.
			if len(args) > 0 {
				language := processLanguage(filepath.Base(args[0]))
				if language != "" && (len(args) == 1 || (len(args) == 2 && args[1] == "-")) {
					for _, redirect := range statement.Redirs {
						if redirect.Hdoc != nil {
							if body, ok := shellLiteral(redirect.Hdoc, bindings); ok {
								facts = append(facts, Fact{Kind: "source", Name: language, Args: []string{body}, Line: int(statement.Pos().Line())})
							}
						}
					}
				}
			}
			return true
		})
	}
	walk(file, 0)
	return facts, nil
}

func shellLiteral(word *syntax.Word, bindings map[string]string) (string, bool) {
	if word == nil {
		return "", false
	}
	var text strings.Builder
	for _, part := range word.Parts {
		switch value := part.(type) {
		case *syntax.Lit:
			text.WriteString(value.Value)
		case *syntax.SglQuoted:
			text.WriteString(value.Value)
		case *syntax.DblQuoted:
			nested, ok := shellLiteral(&syntax.Word{Parts: value.Parts}, bindings)
			if !ok {
				return "", false
			}
			text.WriteString(nested)
		case *syntax.ParamExp:
			if value.Param == nil || value.Exp != nil || value.Repl != nil || value.Slice != nil || value.Index != nil || value.Length || value.Excl {
				return "", false
			}
			literal, ok := bindings[value.Param.Value]
			if !ok {
				return "", false
			}
			text.WriteString(literal)
		default:
			return "", false
		}
	}
	return text.String(), true
}

func processLanguage(program string) string {
	switch strings.ToLower(program) {
	case "r", "rscript":
		return "r"
	case "bash", "sh":
		return "bash"
	case "py", "python", "python3":
		return "python"
	}
	if strings.HasPrefix(program, "python3.") {
		for _, c := range strings.TrimPrefix(program, "python3.") {
			if c < '0' || c > '9' {
				return ""
			}
		}
		return "python"
	}
	return ""
}

func ProcessFacts(args []string, line int) []Fact {
	for len(args) > 0 {
		program := strings.ToLower(filepath.Base(args[0]))
		if program != "env" && program != "command" && program != "sudo" && program != "exec" {
			break
		}
		args = args[1:]
		for len(args) > 0 && (strings.Contains(args[0], "=") || strings.HasPrefix(args[0], "-")) {
			args = args[1:]
		}
	}
	if len(args) == 0 || args[0] == "" {
		return nil
	}
	program := strings.ToLower(filepath.Base(args[0]))
	arguments := args[1:]
	if processLanguage(program) == "python" && len(arguments) >= 2 && arguments[0] == "-m" {
		program = arguments[1]
		arguments = arguments[2:]
	}
	if program == "uv" && len(arguments) > 0 && arguments[0] == "pip" {
		arguments = arguments[1:]
	}
	if packagePrograms[program] {
		for _, argument := range arguments {
			if mutationVerbs[argument] {
				return []Fact{{Kind: "call", Name: "process.package_mutation", Line: line}}
			}
			if !strings.HasPrefix(argument, "-") && argument != "env" {
				break
			}
		}
	}
	if program == "curl" || downloadPrograms[program] {
		toFile := downloadPrograms[program]
		var target string
		for _, arg := range arguments {
			if arg == "-o" || arg == "-O" || arg == "--output" || arg == "--remote-name" || strings.HasPrefix(arg, "--output=") {
				toFile = true
			}
			if publicURL(arg) {
				target = arg
			}
		}
		if toFile && target != "" {
			return []Fact{{Kind: "call", Name: "process.file_download", Args: []string{target}, Line: line}}
		}
	}
	language := processLanguage(program)
	if language == "" {
		return nil
	}
	for index, arg := range arguments {
		if arg == "-c" || arg == "-lc" || arg == "-e" || arg == "--expression" {
			if index+1 < len(arguments) && arguments[index+1] != "" {
				return []Fact{{Kind: "source", Name: language, Args: []string{arguments[index+1]}, Line: line}}
			}
			return nil
		}
		if strings.HasPrefix(arg, "--expression=") {
			return []Fact{{Kind: "source", Name: language, Args: []string{strings.TrimPrefix(arg, "--expression=")}, Line: line}}
		}
		if arg != "" && !strings.HasPrefix(arg, "-") {
			return []Fact{{Kind: "script", Name: language, Args: []string{arg}, Line: line}}
		}
	}
	return nil
}

var packagePrograms = map[string]bool{"pip": true, "pip3": true, "conda": true, "mamba": true, "micromamba": true, "uv": true, "poetry": true, "apt": true, "apt-get": true, "brew": true}
var mutationVerbs = map[string]bool{"install": true, "uninstall": true, "remove": true, "update": true, "upgrade": true, "create": true, "delete": true, "add": true, "sync": true}
var downloadPrograms = map[string]bool{"wget": true, "wget2": true, "aria2c": true, "axel": true}
