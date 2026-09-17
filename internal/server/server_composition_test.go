package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestServerCompositionRootOwnsOnlyConstructionLifecycleAndRoutes(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve server composition test path")
	}
	path := filepath.Join(filepath.Dir(currentFile), "server.go")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read server composition root: %v", err)
	}
	if lines := len(bytesLines(source)); lines > 1000 {
		t.Fatalf("server composition root grew to %d lines; maximum is 1000", lines)
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), path, source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse server composition root: %v", err)
	}
	allowedFunctions := map[string]bool{
		"New":                                 true,
		"RunManagedPythonProvisioner":         true,
		"RunManagedEnvironmentSupervisor":     true,
		"ManagedEnvironmentSupervisorEnabled": true,
		"ManagedPythonProvisioningEnabled":    true,
		"kernelRuntimeWake":                   true,
		"Handler":                             true,
	}
	allowedTypes := map[string]bool{
		"Options":                    true,
		"RunnerDiagnostics":          true,
		"AdapterDiagnostics":         true,
		"AdapterPlatformDiagnostics": true,
		"Server":                     true,
	}
	for _, decl := range parsed.Decls {
		switch item := decl.(type) {
		case *ast.FuncDecl:
			if !allowedFunctions[item.Name.Name] {
				t.Errorf("server composition root contains business function %s", item.Name.Name)
			}
		case *ast.GenDecl:
			if item.Tok == token.IMPORT {
				continue
			}
			for _, spec := range item.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || !allowedTypes[typeSpec.Name.Name] {
					name := item.Tok.String()
					if ok {
						name = typeSpec.Name.Name
					}
					t.Errorf("server composition root contains non-composition declaration %s", name)
				}
			}
		}
	}
}

func bytesLines(value []byte) [][]byte {
	var lines [][]byte
	start := 0
	for index, item := range value {
		if item != '\n' {
			continue
		}
		lines = append(lines, value[start:index])
		start = index + 1
	}
	if start < len(value) {
		lines = append(lines, value[start:])
	}
	return lines
}
