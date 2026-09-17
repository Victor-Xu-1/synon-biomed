package fileops

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func normalizeCodeIndexOptions(options CodeIndexOptions) CodeIndexOptions {
	options.Extensions = normalizeCodeIndexSet(options.Extensions, true)
	options.SymbolKinds = normalizeCodeIndexSet(options.SymbolKinds, false)
	options.Query = strings.ToLower(strings.TrimSpace(options.Query))
	return options
}

func normalizeCodeReferencesOptions(options CodeReferencesOptions) CodeReferencesOptions {
	options.Extensions = normalizeCodeIndexSet(options.Extensions, true)
	if options.ContextLines < 0 {
		options.ContextLines = 0
	}
	if options.ContextLines > 5 {
		options.ContextLines = 5
	}
	return options
}

func codeReferenceContext(lines []string, matchIndex int, contextLines int) ([]CodeReferenceContextLine, []CodeReferenceContextLine) {
	if contextLines <= 0 || matchIndex < 0 || matchIndex >= len(lines) {
		return nil, nil
	}
	beforeStart := matchIndex - contextLines
	if beforeStart < 0 {
		beforeStart = 0
	}
	before := make([]CodeReferenceContextLine, 0, matchIndex-beforeStart)
	for index := beforeStart; index < matchIndex; index++ {
		before = append(before, CodeReferenceContextLine{Line: index + 1, Text: lines[index]})
	}
	afterEnd := matchIndex + 1 + contextLines
	if afterEnd > len(lines) {
		afterEnd = len(lines)
	}
	after := make([]CodeReferenceContextLine, 0, afterEnd-matchIndex-1)
	for index := matchIndex + 1; index < afterEnd; index++ {
		after = append(after, CodeReferenceContextLine{Line: index + 1, Text: lines[index]})
	}
	return before, after
}

func normalizeCodeIndexSet(values []string, extension bool) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if extension && !strings.HasPrefix(value, ".") {
			value = "." + value
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	sort.Strings(normalized)
	return normalized
}

func isCodeIndexFile(path string, allowedExtensions []string) bool {
	extension := strings.ToLower(filepath.Ext(path))
	if len(allowedExtensions) > 0 {
		return stringInSortedSet(allowedExtensions, extension) && isSupportedCodeIndexExtension(extension)
	}
	return isSupportedCodeIndexExtension(extension)
}

func isSupportedCodeIndexExtension(extension string) bool {
	switch strings.ToLower(extension) {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".py":
		return true
	default:
		return false
	}
}

func filterCodeSymbols(symbols []CodeSymbol, options CodeIndexOptions, total *int64) []CodeSymbol {
	filtered := make([]CodeSymbol, 0, len(symbols))
	for _, symbol := range symbols {
		if codeIndexSymbolLimitReached(*total, options.SymbolLimit) {
			break
		}
		if len(options.SymbolKinds) > 0 && !stringInSortedSet(options.SymbolKinds, strings.ToLower(symbol.Kind)) {
			continue
		}
		if options.Query != "" && !strings.Contains(strings.ToLower(symbol.Name), options.Query) {
			continue
		}
		filtered = append(filtered, symbol)
		*total = *total + 1
	}
	return filtered
}

func codeIndexSymbolLimitReached(count int64, limit int64) bool {
	return limit > 0 && count >= limit
}

func stringInSortedSet(values []string, value string) bool {
	index := sort.SearchStrings(values, value)
	return index < len(values) && values[index] == value
}

func shouldSkipCodeIndexDir(name string) bool {
	switch name {
	case ".git", "node_modules", "dist", "vendor", "models", "users", "workspace", "runtime", "uploads", "test-results":
		return true
	default:
		return false
	}
}

func indexSymbols(path string) ([]CodeSymbol, error) {
	if strings.EqualFold(filepath.Ext(path), ".go") {
		return indexGoSymbols(path)
	}
	return indexTextCodeSymbols(path)
}

func indexGoSymbols(path string) ([]CodeSymbol, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	symbols := make([]CodeSymbol, 0)
	for _, decl := range file.Decls {
		switch typed := decl.(type) {
		case *ast.FuncDecl:
			symbols = append(symbols, CodeSymbol{Name: typed.Name.Name, Kind: "func", Line: fset.Position(typed.Pos()).Line, Signature: goFuncSignature(fset, typed)})
		case *ast.GenDecl:
			for _, spec := range typed.Specs {
				if typeSpec, ok := spec.(*ast.TypeSpec); ok {
					symbols = append(symbols, CodeSymbol{Name: typeSpec.Name.Name, Kind: "type", Line: fset.Position(typeSpec.Pos()).Line, Signature: goTypeSignature(fset, typeSpec)})
				}
			}
		}
	}
	sortSymbols(symbols)
	return symbols, nil
}

func indexTextCodeSymbols(path string) ([]CodeSymbol, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	patterns := []struct {
		kind string
		re   *regexp.Regexp
	}{
		{kind: "class", re: regexp.MustCompile(`^\s*(?:export\s+)?class\s+([A-Za-z_][A-Za-z0-9_]*)`)},
		{kind: "type", re: regexp.MustCompile(`^\s*(?:export\s+)?(?:interface|type)\s+([A-Za-z_][A-Za-z0-9_]*)`)},
		{kind: "func", re: regexp.MustCompile(`^\s*(?:export\s+)?(?:async\s+)?function\s+([A-Za-z_][A-Za-z0-9_]*)`)},
		{kind: "func", re: regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(?:async\s+)?function\b`)},
		{kind: "func", re: regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(?:async\s*)?(?:\([^)]*\)|[A-Za-z_][A-Za-z0-9_]*)\s*=>`)},
		{kind: "func", re: regexp.MustCompile(`^\s*async\s+def\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)},
		{kind: "func", re: regexp.MustCompile(`^\s*def\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)},
		{kind: "class", re: regexp.MustCompile(`^\s*class\s+([A-Za-z_][A-Za-z0-9_]*)`)},
	}
	lines := strings.Split(string(data), "\n")
	symbols := make([]CodeSymbol, 0)
	for index, line := range lines {
		for _, pattern := range patterns {
			if match := pattern.re.FindStringSubmatch(line); len(match) == 2 {
				symbols = append(symbols, CodeSymbol{Name: match[1], Kind: pattern.kind, Line: index + 1, Signature: compactCodeSignature(line)})
				break
			}
		}
	}
	sortSymbols(symbols)
	return symbols, nil
}

func goFuncSignature(fset *token.FileSet, decl *ast.FuncDecl) string {
	if decl == nil {
		return ""
	}
	funcType := printGoNode(fset, decl.Type)
	funcType = strings.TrimSpace(strings.TrimPrefix(funcType, "func"))
	parts := []string{"func"}
	if decl.Recv != nil {
		if receiver := goReceiverSignature(fset, decl.Recv); receiver != "" {
			parts = append(parts, receiver)
		}
	}
	parts = append(parts, decl.Name.Name+funcType)
	return compactCodeSignature(strings.Join(parts, " "))
}

func goTypeSignature(fset *token.FileSet, spec *ast.TypeSpec) string {
	if spec == nil {
		return ""
	}
	return compactCodeSignature("type " + spec.Name.Name + " " + printGoNode(fset, spec.Type))
}

func goReceiverSignature(fset *token.FileSet, receiver *ast.FieldList) string {
	if receiver == nil || len(receiver.List) == 0 {
		return ""
	}
	parts := make([]string, 0, len(receiver.List))
	for _, field := range receiver.List {
		typeText := printGoNode(fset, field.Type)
		if typeText == "" {
			continue
		}
		if len(field.Names) == 0 {
			parts = append(parts, typeText)
			continue
		}
		for _, name := range field.Names {
			parts = append(parts, strings.TrimSpace(name.Name+" "+typeText))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func printGoNode(fset *token.FileSet, node any) string {
	if node == nil {
		return ""
	}
	var buffer bytes.Buffer
	if err := printer.Fprint(&buffer, fset, node); err != nil {
		return ""
	}
	return buffer.String()
}

func compactCodeSignature(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	runes := []rune(value)
	if len(runes) > 240 {
		return string(runes[:240]) + "..."
	}
	return value
}

func sortSymbols(symbols []CodeSymbol) {
	sort.Slice(symbols, func(i, j int) bool {
		if symbols[i].Line != symbols[j].Line {
			return symbols[i].Line < symbols[j].Line
		}
		return symbols[i].Name < symbols[j].Name
	})
}
