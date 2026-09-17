package server

import (
	"path/filepath"
	"strings"
	"unicode"
)

type managedExecutionToken struct {
	kind  byte
	value string
}

func managedExecutionIdentifier(publicName, content string, identifiers []string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(publicName)) {
	case "python", "py":
		return managedExecutionPythonIdentifier(content, identifiers)
	case "r":
		return managedExecutionRIdentifier(content, identifiers)
	case "bash", "sh", "zsh", "powershell", "pwsh":
		return managedExecutionShellIdentifier(content, identifiers)
	default:
		return "", false
	}
}

func managedExecutionSourceLanguage(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".py":
		return "python"
	case ".r":
		return "r"
	case ".ps1":
		return "powershell"
	default:
		return "bash"
	}
}

func managedExecutionIdentifierMatch(candidate string, identifiers []string) (string, bool) {
	candidate = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(candidate, `\`, "/")))
	base := strings.ToLower(filepath.Base(candidate))
	for _, identifier := range identifiers {
		identifier = strings.ToLower(strings.TrimSpace(identifier))
		if identifier != "" && (candidate == identifier || base == identifier || strings.HasPrefix(base, identifier+".")) {
			return identifier, true
		}
	}
	return "", false
}

func managedExecutionShellIdentifier(content string, identifiers []string) (string, bool) {
	bindings := map[string]string{}
	for _, command := range managedExecutionShellCommands(content) {
		words := managedExecutionShellWords(command)
		for _, word := range words {
			name, value, found := strings.Cut(word, "=")
			if !found || !managedExecutionVariableName(name) || strings.TrimSpace(value) == "" {
				continue
			}
			bindings[name] = value
		}
		index := managedExecutionShellExecutableIndex(words)
		if index < 0 {
			continue
		}
		executableWord := managedExecutionResolveShellWord(words[index], bindings)
		if identifier, matched := managedExecutionIdentifierMatch(executableWord, identifiers); matched {
			return identifier, true
		}
		executable := strings.ToLower(filepath.Base(executableWord))
		if executable == "python" || strings.HasPrefix(executable, "python") {
			for cursor := index + 1; cursor+1 < len(words); cursor++ {
				if words[cursor] == "-m" {
					if identifier, matched := managedExecutionIdentifierMatch(words[cursor+1], identifiers); matched {
						return identifier, true
					}
				}
				if words[cursor] == "-c" {
					if identifier, matched := managedExecutionPythonIdentifier(words[cursor+1], identifiers); matched {
						return identifier, true
					}
				}
			}
		}
		if executable == "bash" || executable == "sh" || executable == "zsh" || executable == "pwsh" || executable == "powershell" {
			for cursor := index + 1; cursor+1 < len(words); cursor++ {
				if words[cursor] == "-c" || words[cursor] == "-lc" || strings.EqualFold(words[cursor], "-Command") {
					if identifier, matched := managedExecutionShellIdentifier(words[cursor+1], identifiers); matched {
						return identifier, true
					}
				}
			}
		}
	}
	return "", false
}

func managedExecutionVariableName(value string) bool {
	if value == "" {
		return false
	}
	for index, char := range value {
		if !(unicode.IsLetter(char) || char == '_' || index > 0 && unicode.IsDigit(char)) {
			return false
		}
	}
	return true
}

func managedExecutionResolveShellWord(value string, bindings map[string]string) string {
	name := ""
	if strings.HasPrefix(value, "${") && strings.HasSuffix(value, "}") {
		name = strings.TrimSuffix(strings.TrimPrefix(value, "${"), "}")
	} else if strings.HasPrefix(value, "$") {
		name = strings.TrimPrefix(value, "$")
	}
	if managedExecutionVariableName(name) {
		if resolved := strings.TrimSpace(bindings[name]); resolved != "" {
			return resolved
		}
	}
	return value
}

func managedExecutionShellExecutableIndex(words []string) int {
	for index := 0; index < len(words); index++ {
		word := strings.TrimSpace(words[index])
		if word == "" || word == "!" || word == "{" || word == "}" || strings.Contains(word, "=") && !strings.ContainsAny(word, `/\`) {
			continue
		}
		switch strings.ToLower(filepath.Base(word)) {
		case "if", "then", "elif", "else", "do", "done", "while", "until", "for", "sudo", "command", "exec", "nohup", "time":
			continue
		case "env":
			continue
		}
		if strings.HasPrefix(word, "-") {
			continue
		}
		return index
	}
	return -1
}

func managedExecutionShellCommands(content string) []string {
	commands := make([]string, 0, 4)
	var current strings.Builder
	var quote rune
	escaped := false
	comment := false
	flush := func() {
		if value := strings.TrimSpace(current.String()); value != "" {
			commands = append(commands, value)
		}
		current.Reset()
	}
	for _, char := range strings.ReplaceAll(content, "\\\n", " ") {
		if comment {
			if char == '\n' {
				comment = false
				flush()
			}
			continue
		}
		if escaped {
			current.WriteRune(char)
			escaped = false
			continue
		}
		if char == '\\' && quote != '\'' {
			escaped = true
			current.WriteRune(char)
			continue
		}
		if quote != 0 {
			current.WriteRune(char)
			if char == quote {
				quote = 0
			}
			continue
		}
		if char == '\'' || char == '"' {
			quote = char
			current.WriteRune(char)
			continue
		}
		if char == '#' && (current.Len() == 0 || strings.HasSuffix(current.String(), " ") || strings.HasSuffix(current.String(), "\t")) {
			comment = true
			continue
		}
		if char == '\n' || char == ';' || char == '|' || char == '&' {
			flush()
			continue
		}
		current.WriteRune(char)
	}
	flush()
	return commands
}

func managedExecutionShellWords(command string) []string {
	words := make([]string, 0, 8)
	var current strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if current.Len() > 0 {
			words = append(words, current.String())
			current.Reset()
		}
	}
	for _, char := range command {
		if escaped {
			current.WriteRune(char)
			escaped = false
			continue
		}
		if char == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if char == quote {
				quote = 0
			} else {
				current.WriteRune(char)
			}
			continue
		}
		if char == '\'' || char == '"' {
			quote = char
			continue
		}
		if unicode.IsSpace(char) {
			flush()
			continue
		}
		current.WriteRune(char)
	}
	flush()
	return words
}

func managedExecutionPythonIdentifier(content string, identifiers []string) (string, bool) {
	tokens := managedExecutionLanguageTokens(content)
	bindings := managedExecutionPythonBindings(tokens)
	for index, token := range tokens {
		if token.kind != 'i' {
			continue
		}
		switch token.value {
		case "import", "from":
			if index+1 < len(tokens) && tokens[index+1].kind == 'i' {
				if identifier, matched := managedExecutionIdentifierMatch(strings.Split(tokens[index+1].value, ".")[0], identifiers); matched {
					return identifier, true
				}
			}
		case "__import__":
			if identifier, matched := managedExecutionCallStringIdentifier(tokens, index, identifiers); matched {
				return identifier, true
			}
		case "subprocess", "os", "importlib":
			if identifier, matched := managedExecutionQualifiedCallIdentifier(tokens, index, identifiers, bindings); matched {
				return identifier, true
			}
		}
	}
	return "", false
}

func managedExecutionPythonBindings(tokens []managedExecutionToken) map[string]string {
	bindings := map[string]string{}
	for index := 0; index+2 < len(tokens); index++ {
		if tokens[index].kind != 'i' || tokens[index+1].value != "=" {
			continue
		}
		cursor := index + 2
		if tokens[cursor].value == "[" || tokens[cursor].value == "(" {
			cursor++
		}
		if cursor < len(tokens) && tokens[cursor].kind == 's' {
			bindings[tokens[index].value] = tokens[cursor].value
		}
	}
	return bindings
}

func managedExecutionQualifiedCallIdentifier(
	tokens []managedExecutionToken,
	index int,
	identifiers []string,
	bindings map[string]string,
) (string, bool) {
	if index+3 >= len(tokens) || tokens[index+1].value != "." || tokens[index+2].kind != 'i' || tokens[index+3].value != "(" {
		return "", false
	}
	owner, method := tokens[index].value, tokens[index+2].value
	allowed := owner == "subprocess" && (method == "run" || method == "popen" || method == "call" || method == "check_call" || method == "check_output") ||
		owner == "os" && (method == "system" || method == "popen") || owner == "importlib" && method == "import_module"
	if !allowed {
		return "", false
	}
	if identifier, matched := managedExecutionCallStringIdentifier(tokens, index+2, identifiers); matched {
		return identifier, true
	}
	for cursor := index + 4; cursor < len(tokens) && cursor <= index+7; cursor++ {
		if tokens[cursor].kind != 'i' {
			continue
		}
		if value := bindings[tokens[cursor].value]; value != "" {
			words := managedExecutionShellWords(value)
			if len(words) > 0 {
				return managedExecutionIdentifierMatch(words[0], identifiers)
			}
		}
	}
	return "", false
}

func managedExecutionCallStringIdentifier(tokens []managedExecutionToken, index int, identifiers []string) (string, bool) {
	for cursor := index + 1; cursor < len(tokens) && cursor <= index+5; cursor++ {
		if tokens[cursor].kind == 's' {
			words := managedExecutionShellWords(tokens[cursor].value)
			if len(words) > 0 {
				return managedExecutionIdentifierMatch(words[0], identifiers)
			}
		}
	}
	return "", false
}

func managedExecutionRIdentifier(content string, identifiers []string) (string, bool) {
	tokens := managedExecutionLanguageTokens(content)
	for index, token := range tokens {
		if token.kind != 'i' || (token.value != "library" && token.value != "require") {
			continue
		}
		for cursor := index + 1; cursor < len(tokens) && cursor <= index+3; cursor++ {
			if tokens[cursor].kind == 'i' || tokens[cursor].kind == 's' {
				if identifier, matched := managedExecutionIdentifierMatch(tokens[cursor].value, identifiers); matched {
					return identifier, true
				}
			}
		}
	}
	return "", false
}

func managedExecutionLanguageTokens(content string) []managedExecutionToken {
	tokens := make([]managedExecutionToken, 0, 32)
	for index := 0; index < len(content); {
		char := rune(content[index])
		if unicode.IsSpace(char) {
			index++
			continue
		}
		if char == '#' {
			for index < len(content) && content[index] != '\n' {
				index++
			}
			continue
		}
		if char == '\'' || char == '"' {
			quote := byte(char)
			index++
			var value strings.Builder
			for index < len(content) {
				if content[index] == '\\' && index+1 < len(content) {
					value.WriteByte(content[index+1])
					index += 2
					continue
				}
				if content[index] == quote {
					index++
					break
				}
				value.WriteByte(content[index])
				index++
			}
			tokens = append(tokens, managedExecutionToken{kind: 's', value: value.String()})
			continue
		}
		if unicode.IsLetter(char) || char == '_' {
			start := index
			index++
			for index < len(content) {
				next := rune(content[index])
				if !unicode.IsLetter(next) && !unicode.IsDigit(next) && next != '_' {
					break
				}
				index++
			}
			tokens = append(tokens, managedExecutionToken{kind: 'i', value: strings.ToLower(content[start:index])})
			continue
		}
		tokens = append(tokens, managedExecutionToken{kind: 'p', value: string(char)})
		index++
	}
	return tokens
}
