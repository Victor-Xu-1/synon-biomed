package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRetiredHarnessImplementationsAreAbsentAndCompatFilesRemainConsumed(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve retired Harness path test")
	}
	directory := filepath.Dir(currentFile)
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	identifierCounts := map[string]int{}
	compatFunctions := map[string][]string{}
	allSource := ""
	fileSet := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		allSource += "\n" + string(raw)
		parsed, err := parser.ParseFile(fileSet, path, raw, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			if identifier, ok := node.(*ast.Ident); ok {
				identifierCounts[identifier.Name]++
			}
			return true
		})
		if strings.HasSuffix(entry.Name(), "_compat_api.go") {
			for _, declaration := range parsed.Decls {
				if function, ok := declaration.(*ast.FuncDecl); ok {
					compatFunctions[entry.Name()] = append(compatFunctions[entry.Name()], function.Name.Name)
				}
			}
		}
	}
	for _, retired := range []string{
		"executeDirectToolGatewayLegacyResponse",
		"recordDirectToolGatewayAudit",
		"executeApprovedAgentRuntimeToolLegacy",
		"executeChatToolCall",
		"chatToolResultMessage",
	} {
		if strings.Contains(allSource, retired) {
			t.Errorf("retired Harness implementation %s remains in production source", retired)
		}
	}
	for fileName, names := range compatFunctions {
		consumed := false
		for _, name := range names {
			if identifierCounts[name] > 1 {
				consumed = true
				break
			}
		}
		if !consumed {
			t.Errorf("compatibility file %s has no production consumer", fileName)
		}
	}
}
