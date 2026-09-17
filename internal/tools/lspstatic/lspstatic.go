package lspstatic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"synon-go/internal/tools/fileops"
)

var validOperations = map[string]struct{}{
	"goToDefinition":       {},
	"findReferences":       {},
	"hover":                {},
	"documentSymbol":       {},
	"workspaceSymbol":      {},
	"goToImplementation":   {},
	"prepareCallHierarchy": {},
	"incomingCalls":        {},
	"outgoingCalls":        {},
	"diagnostics":          {},
	"renamePreview":        {},
	"status":               {},
}

type Input struct {
	Operation string `json:"operation"`
	FilePath  string `json:"filePath,omitempty"`
	Line      int    `json:"line,omitempty"`
	Character int    `json:"character,omitempty"`
	Query     string `json:"query,omitempty"`
	NewName   string `json:"newName,omitempty"`
}

type Output struct {
	Operation           string   `json:"operation"`
	Result              string   `json:"result"`
	FilePath            string   `json:"filePath"`
	ResultCount         int      `json:"resultCount,omitempty"`
	FileCount           int      `json:"fileCount,omitempty"`
	Source              string   `json:"source,omitempty"`
	LiveAttempted       bool     `json:"liveAttempted,omitempty"`
	LiveServer          string   `json:"liveServer,omitempty"`
	LiveTransport       string   `json:"liveTransport,omitempty"`
	LanguageID          string   `json:"languageId,omitempty"`
	LiveSessionReused   bool     `json:"liveSessionReused,omitempty"`
	LiveSessionPoolSize int      `json:"liveSessionPoolSize,omitempty"`
	FallbackReason      string   `json:"fallbackReason,omitempty"`
	ConfigSource        string   `json:"configSource,omitempty"`
	ConfigPresent       bool     `json:"configPresent,omitempty"`
	LiveServers         []string `json:"liveServers,omitempty"`
	PendingDiagnostics  int      `json:"pendingDiagnostics,omitempty"`
}

type Options struct {
	Root string
}

func Run(ctx context.Context, input Input, options Options) (Output, error) {
	_ = ctx
	operation := strings.TrimSpace(input.Operation)
	if _, ok := validOperations[operation]; !ok {
		return Output{}, fmt.Errorf("unsupported LSP operation: %s", operation)
	}
	if err := validateInput(input); err != nil {
		return Output{}, err
	}
	root := strings.TrimSpace(options.Root)
	if root == "" {
		return Output{}, errors.New("LSP root is not configured")
	}
	displayPath := input.FilePath
	if strings.TrimSpace(displayPath) == "" {
		displayPath = "."
	}
	if operation == "status" {
		return liveStatus(root, input, displayPath)
	}

	if liveOutput, attempted, err := runLive(ctx, input, root, displayPath); attempted {
		if err == nil {
			return liveOutput, nil
		}
		staticOutput, staticErr := runStatic(root, input, operation, displayPath)
		if staticErr != nil {
			return Output{}, fmt.Errorf("live LSP failed: %w; static fallback failed: %v", err, staticErr)
		}
		staticOutput.Result = "Live LSP failed: " + err.Error() + "\nStatic fallback:\n" + staticOutput.Result
		staticOutput = annotateStaticOutput(staticOutput)
		staticOutput.LiveAttempted = true
		staticOutput.FallbackReason = err.Error()
		return staticOutput, nil
	}
	staticOutput, err := runStatic(root, input, operation, displayPath)
	if err != nil {
		return Output{}, err
	}
	return annotateStaticOutput(staticOutput), nil
}

func runStatic(root string, input Input, operation string, displayPath string) (Output, error) {
	switch operation {
	case "documentSymbol":
		return documentSymbols(root, input, displayPath)
	case "workspaceSymbol":
		return workspaceSymbols(root, input, displayPath)
	case "findReferences":
		return referencesAtPosition(root, input, displayPath)
	case "goToDefinition", "goToImplementation", "prepareCallHierarchy":
		return definitionsAtPosition(root, input, displayPath)
	case "hover":
		return hoverAtPosition(root, input, displayPath)
	case "diagnostics":
		return diagnostics(root, input, displayPath)
	case "renamePreview":
		return renamePreview(root, input, displayPath)
	case "incomingCalls", "outgoingCalls":
		return unsupported(operation, displayPath, "No matching live LSP server was available for call hierarchy; static fallback cannot infer dynamic call hierarchy."), nil
	default:
		return Output{}, fmt.Errorf("unsupported LSP operation: %s", operation)
	}
}

func annotateStaticOutput(output Output) Output {
	if output.Source == "" {
		output.Source = "static"
	}
	return output
}

func validateInput(input Input) error {
	switch input.Operation {
	case "workspaceSymbol":
		if strings.TrimSpace(input.Query) == "" {
			return errors.New("LSP.workspaceSymbol query is required")
		}
	case "documentSymbol", "diagnostics":
		if strings.TrimSpace(input.FilePath) == "" {
			return fmt.Errorf("LSP.%s filePath is required", input.Operation)
		}
	case "renamePreview":
		if strings.TrimSpace(input.NewName) == "" {
			return errors.New("LSP.renamePreview newName is required")
		}
		fallthrough
	case "goToDefinition", "findReferences", "hover", "goToImplementation", "prepareCallHierarchy", "incomingCalls", "outgoingCalls":
		if strings.TrimSpace(input.FilePath) == "" {
			return fmt.Errorf("LSP.%s filePath is required", input.Operation)
		}
		if input.Line <= 0 || input.Character <= 0 {
			return fmt.Errorf("LSP.%s line and character must be positive", input.Operation)
		}
	}
	return nil
}

func documentSymbols(root string, input Input, displayPath string) (Output, error) {
	index, err := fileops.CodeIndex(root, input.FilePath, fileops.CodeIndexOptions{Limit: 1, SymbolLimit: 500})
	if err != nil {
		return Output{}, err
	}
	lines := []string{}
	count := 0
	for _, file := range index.Files {
		lines = append(lines, fmt.Sprintf("%s", file.Path))
		for _, symbol := range file.Symbols {
			count++
			lines = append(lines, fmt.Sprintf("  line %d %s %s%s", symbol.Line, symbol.Kind, symbol.Name, signatureSuffix(symbol.Signature)))
		}
	}
	if count == 0 {
		lines = append(lines, "No document symbols found by the Go static indexer.")
	}
	return Output{Operation: input.Operation, FilePath: displayPath, Result: strings.Join(lines, "\n"), ResultCount: count, FileCount: len(index.Files)}, nil
}

func workspaceSymbols(root string, input Input, displayPath string) (Output, error) {
	path := input.FilePath
	if strings.TrimSpace(path) == "" {
		path = "."
	}
	index, err := fileops.CodeIndex(root, path, fileops.CodeIndexOptions{Limit: 200, SymbolLimit: 500, Query: input.Query})
	if err != nil {
		return Output{}, err
	}
	lines := []string{}
	count := 0
	for _, file := range index.Files {
		for _, symbol := range file.Symbols {
			count++
			lines = append(lines, fmt.Sprintf("%s:%d %s %s%s", file.Path, symbol.Line, symbol.Kind, symbol.Name, signatureSuffix(symbol.Signature)))
		}
	}
	if count == 0 {
		lines = append(lines, fmt.Sprintf("No workspace symbols matched %q by the Go static indexer.", input.Query))
	}
	return Output{Operation: input.Operation, FilePath: displayPath, Result: strings.Join(lines, "\n"), ResultCount: count, FileCount: len(index.Files)}, nil
}

func referencesAtPosition(root string, input Input, displayPath string) (Output, error) {
	symbol, err := symbolAtPosition(root, input)
	if err != nil {
		return Output{}, err
	}
	refs, err := fileops.CodeReferences(root, ".", symbol, fileops.CodeReferencesOptions{Limit: 200, ContextLines: 1})
	if err != nil {
		return Output{}, err
	}
	files := map[string]struct{}{}
	lines := []string{fmt.Sprintf("Symbol: %s", symbol)}
	for _, ref := range refs.References {
		files[ref.Path] = struct{}{}
		lines = append(lines, fmt.Sprintf("%s:%d %s", ref.Path, ref.Line, strings.TrimSpace(ref.Text)))
	}
	if len(refs.References) == 0 {
		lines = append(lines, "No references found by the Go static reference scanner.")
	}
	return Output{Operation: input.Operation, FilePath: displayPath, Result: strings.Join(lines, "\n"), ResultCount: len(refs.References), FileCount: len(files)}, nil
}

func definitionsAtPosition(root string, input Input, displayPath string) (Output, error) {
	symbol, err := symbolAtPosition(root, input)
	if err != nil {
		return Output{}, err
	}
	index, err := fileops.CodeIndex(root, ".", fileops.CodeIndexOptions{Limit: 200, SymbolLimit: 1000, Query: symbol})
	if err != nil {
		return Output{}, err
	}
	lines := []string{fmt.Sprintf("Symbol: %s", symbol)}
	count := 0
	files := map[string]struct{}{}
	for _, file := range index.Files {
		for _, item := range file.Symbols {
			if item.Name != symbol {
				continue
			}
			count++
			files[file.Path] = struct{}{}
			lines = append(lines, fmt.Sprintf("%s:%d %s %s%s", file.Path, item.Line, item.Kind, item.Name, signatureSuffix(item.Signature)))
		}
	}
	if count == 0 {
		lines = append(lines, "No static definition found. A live LSP server may be required for this language feature.")
	}
	return Output{Operation: input.Operation, FilePath: displayPath, Result: strings.Join(lines, "\n"), ResultCount: count, FileCount: len(files)}, nil
}

func hoverAtPosition(root string, input Input, displayPath string) (Output, error) {
	symbol, err := symbolAtPosition(root, input)
	if err != nil {
		return Output{}, err
	}
	definition, err := definitionsAtPosition(root, Input{Operation: "goToDefinition", FilePath: input.FilePath, Line: input.Line, Character: input.Character}, displayPath)
	if err != nil {
		return Output{}, err
	}
	result := "Symbol: " + symbol + "\n" + definition.Result
	return Output{Operation: input.Operation, FilePath: displayPath, Result: result, ResultCount: definition.ResultCount, FileCount: definition.FileCount}, nil
}

func diagnostics(root string, input Input, displayPath string) (Output, error) {
	path, _, err := resolve(root, input.FilePath)
	if err != nil {
		return Output{}, err
	}
	if output, ok := pendingDiagnosticsOutput(input.Operation, displayPath, fileURI(path)); ok {
		return output, nil
	}
	if strings.EqualFold(filepath.Ext(path), ".go") {
		fset := token.NewFileSet()
		if _, err := parser.ParseFile(fset, path, nil, parser.AllErrors); err != nil {
			return Output{Operation: input.Operation, FilePath: displayPath, Result: "Go parser diagnostics:\n" + err.Error(), ResultCount: 1, FileCount: 1}, nil
		}
		return Output{Operation: input.Operation, FilePath: displayPath, Result: "No Go parser diagnostics found.", ResultCount: 0, FileCount: 1}, nil
	}
	return Output{Operation: input.Operation, FilePath: displayPath, Result: "No matching live LSP server was available; static diagnostics are only available for Go files in this compact package.", ResultCount: 0, FileCount: 1}, nil
}

func pendingDiagnosticsOutput(operation string, displayPath string, uri string) (Output, bool) {
	sets := CheckForLSPDiagnosticsForURI(uri)
	if len(sets) == 0 {
		return Output{}, false
	}
	files := []DiagnosticFile{}
	diagnosticCount := 0
	for _, set := range sets {
		for _, file := range set.Files {
			if file.URI != uri {
				continue
			}
			files = append(files, file)
			diagnosticCount += len(file.Diagnostics)
		}
	}
	if diagnosticCount == 0 {
		return Output{}, false
	}
	raw, err := json.MarshalIndent(files, "", "  ")
	if err != nil {
		raw, _ = json.Marshal(files)
	}
	return Output{
		Operation:   operation,
		FilePath:    displayPath,
		Result:      "Pending LSP diagnostics from passive diagnostic registry:\n" + string(raw),
		ResultCount: diagnosticCount,
		FileCount:   len(files),
	}, true
}

func renamePreview(root string, input Input, displayPath string) (Output, error) {
	symbol, err := symbolAtPosition(root, input)
	if err != nil {
		return Output{}, err
	}
	refs, err := fileops.CodeReferences(root, ".", symbol, fileops.CodeReferencesOptions{Limit: 200, ContextLines: 0})
	if err != nil {
		return Output{}, err
	}
	files := map[string]struct{}{}
	lines := []string{fmt.Sprintf("Rename preview: %s -> %s", symbol, input.NewName)}
	for _, ref := range refs.References {
		files[ref.Path] = struct{}{}
		lines = append(lines, fmt.Sprintf("%s:%d %s", ref.Path, ref.Line, strings.ReplaceAll(ref.Text, symbol, input.NewName)))
	}
	if len(refs.References) == 0 {
		lines = append(lines, "No rename candidates found by the static reference scanner.")
	}
	return Output{Operation: input.Operation, FilePath: displayPath, Result: strings.Join(lines, "\n"), ResultCount: len(refs.References), FileCount: len(files)}, nil
}

func unsupported(operation string, displayPath string, message string) Output {
	return Output{Operation: operation, FilePath: displayPath, Result: message, ResultCount: 0, FileCount: 0}
}

func symbolAtPosition(root string, input Input) (string, error) {
	path, _, err := resolve(root, input.FilePath)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(data), "\n")
	if input.Line <= 0 || input.Line > len(lines) {
		return "", fmt.Errorf("line %d is outside file", input.Line)
	}
	line := lines[input.Line-1]
	character := input.Character
	if character > len([]rune(line))+1 {
		return "", fmt.Errorf("character %d is outside line %d", input.Character, input.Line)
	}
	start, end := identifierBounds(line, character-1)
	if start == end {
		return "", fmt.Errorf("no identifier at %s:%d:%d", input.FilePath, input.Line, input.Character)
	}
	return string([]rune(line)[start:end]), nil
}

func identifierBounds(line string, zeroBasedCharacter int) (int, int) {
	runes := []rune(line)
	if len(runes) == 0 {
		return 0, 0
	}
	if zeroBasedCharacter >= len(runes) {
		zeroBasedCharacter = len(runes) - 1
	}
	if zeroBasedCharacter < 0 {
		zeroBasedCharacter = 0
	}
	if !isIdentifierRune(runes[zeroBasedCharacter]) && zeroBasedCharacter > 0 && isIdentifierRune(runes[zeroBasedCharacter-1]) {
		zeroBasedCharacter--
	}
	if !isIdentifierRune(runes[zeroBasedCharacter]) {
		return zeroBasedCharacter, zeroBasedCharacter
	}
	start := zeroBasedCharacter
	for start > 0 && isIdentifierRune(runes[start-1]) {
		start--
	}
	end := zeroBasedCharacter + 1
	for end < len(runes) && isIdentifierRune(runes[end]) {
		end++
	}
	return start, end
}

func isIdentifierRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func signatureSuffix(signature string) string {
	signature = strings.TrimSpace(signature)
	if signature == "" {
		return ""
	}
	return " " + signature
}

func resolve(root string, requested string) (string, string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	rootEval, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		rootEval = rootAbs
	}
	requested = strings.TrimSpace(requested)
	if requested == "" {
		requested = "."
	}
	candidate := requested
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(rootEval, candidate)
	}
	cleaned, err := filepath.Abs(candidate)
	if err != nil {
		return "", "", err
	}
	evaluated, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return "", "", err
	}
	if err := ensureInside(rootEval, evaluated); err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(rootEval, evaluated)
	if err != nil {
		return "", "", err
	}
	if !utf8.ValidString(rel) {
		return "", "", errors.New("path is not valid UTF-8")
	}
	return evaluated, filepath.ToSlash(rel), nil
}

func ensureInside(root string, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("path escapes configured root")
	}
	return nil
}
