package server

import (
	"regexp"
	"strings"
)

var replRecoveryLambdaStartPattern = regexp.MustCompile(`\blambda\b`)

// This is a lexical receiver preflight, not a Python interpreter. Preserve
// offsets in the sanitized cell so parameters authorize only body receivers;
// defaults and annotations are evaluated in the enclosing scope.
type replRecoveryParameterScope struct {
	headerStart int
	start, end  int
	names       map[string]struct{}
}

func replRecoveryInsideParameterHeader(scopes []replRecoveryParameterScope, offset int) bool {
	for _, scope := range scopes {
		if offset >= scope.headerStart && offset < scope.start {
			return true
		}
	}
	return false
}

func replRecoveryParameterInScope(scopes []replRecoveryParameterScope, name string, offset int) bool {
	for _, scope := range scopes {
		if offset >= scope.start && offset < scope.end {
			if _, found := scope.names[name]; found {
				return true
			}
		}
	}
	return false
}

func replRecoveryParameterScopes(code string) []replRecoveryParameterScope {
	var scopes []replRecoveryParameterScope
	for _, match := range replRecoveryFunctionPattern.FindAllStringIndex(code, -1) {
		open := match[1] - 1
		close := replRecoveryTopLevelDelimiter(code, open+1, ")")
		if close == len(code) {
			continue
		}
		colon := replRecoveryTopLevelDelimiter(code, close+1, ":\n")
		if colon == len(code) || code[colon] != ':' {
			continue
		}
		scopes = append(scopes, replRecoveryParameterScope{
			headerStart: open + 1,
			start:       colon + 1, end: replRecoveryFunctionBodyEnd(code, match[0], colon+1),
			names: replRecoveryParameterNames(code[open+1 : close]),
		})
	}
	for _, match := range replRecoveryLambdaStartPattern.FindAllStringIndex(code, -1) {
		headerDelimiters, bodyDelimiters := ":\n", ",;\n)]}"
		if replRecoveryBracketDepth(code[:match[0]]) > 0 {
			headerDelimiters, bodyDelimiters = ":", ",;)]}"
		}
		colon := replRecoveryTopLevelDelimiter(code, match[1], headerDelimiters)
		if colon == len(code) || code[colon] != ':' {
			continue
		}
		scopes = append(scopes, replRecoveryParameterScope{
			headerStart: match[1],
			start:       colon + 1, end: replRecoveryTopLevelDelimiter(code, colon+1, bodyDelimiters),
			names: replRecoveryParameterNames(code[match[1]:colon]),
		})
	}
	return scopes
}

// Skip balanced expressions, including calls, collection literals and type
// annotations. A delimiter inside one of them cannot end a parameter list.
func replRecoveryTopLevelDelimiter(code string, start int, delimiters string) int {
	depth := 0
	lambdaHeaders := 0
	for i := start; i < len(code); i++ {
		c := code[i]
		if depth == 0 && strings.HasPrefix(code[i:], "lambda") &&
			(i == 0 || !replRecoveryIdentifierByte(code[i-1])) &&
			(i+6 == len(code) || !replRecoveryIdentifierByte(code[i+6])) {
			lambdaHeaders++
			i += 5
			continue
		}
		if depth == 0 && lambdaHeaders > 0 && (c == ':' || c == ',') {
			if c == ':' {
				lambdaHeaders--
			}
			continue
		}
		if depth == 0 && strings.ContainsRune(delimiters, rune(c)) {
			return i
		}
		switch c {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth == 0 {
				return i
			}
			depth--
		}
	}
	return len(code)
}

func replRecoveryIdentifierByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

func replRecoveryBracketDepth(code string) int {
	depth := 0
	for _, c := range code {
		switch c {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		}
	}
	return depth
}

func replRecoveryParameterNames(parameters string) map[string]struct{} {
	names := make(map[string]struct{})
	for start := 0; start < len(parameters); {
		end := replRecoveryTopLevelDelimiter(parameters, start, ",")
		parameter := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(parameters[start:end]), "*"))
		if separator := strings.IndexAny(parameter, ":="); separator >= 0 {
			parameter = strings.TrimSpace(parameter[:separator])
		}
		if replRecoveryIdentifierPattern.MatchString(parameter) {
			names[parameter] = struct{}{}
		}
		start = end + 1
	}
	return names
}

func replRecoveryFunctionBodyEnd(code string, declaration, body int) int {
	lineEnd := strings.IndexByte(code[body:], '\n')
	if lineEnd < 0 {
		return len(code)
	}
	lineEnd += body
	if strings.TrimSpace(code[body:lineEnd]) != "" {
		return lineEnd
	}
	baseIndent := replRecoveryIndent(code[declaration:])
	depth := 0
	continued := false
	for start := lineEnd + 1; start < len(code); {
		end := strings.IndexByte(code[start:], '\n')
		if end < 0 {
			end = len(code)
		} else {
			end += start
		}
		line := code[start:end]
		if strings.TrimSpace(line) != "" {
			if depth == 0 && !continued && replRecoveryIndent(line) <= baseIndent {
				return start
			}
			for _, c := range line {
				switch c {
				case '(', '[', '{':
					depth++
				case ')', ']', '}':
					depth--
				}
			}
			continued = strings.HasSuffix(strings.TrimSpace(line), "\\")
		}
		start = end + 1
	}
	return len(code)
}

func replRecoveryIndent(line string) int {
	indent := 0
	for _, c := range line {
		switch c {
		case ' ':
			indent++
		case '\t':
			indent += 8 - indent%8
		default:
			return indent
		}
	}
	return indent
}
