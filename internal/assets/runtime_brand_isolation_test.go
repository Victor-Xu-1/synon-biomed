package assets

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestRuntimeAndModelSurfacesUseOnlySynonProductIdentity(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	for _, relative := range []string{
		"internal/agentruntime", "internal/kernel", "internal/server", "internal/software", "internal/tools",
	} {
		walkGoStringLiterals(t, filepath.Join(root, relative))
	}
	for _, relative := range []string{
		"assets/synonbiomed/agents", "skills/synonbiomed",
	} {
		walkModelTextFiles(t, filepath.Join(root, relative))
	}
}

func walkGoStringLiterals(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err == nil && containsReferenceProductLabel(value) && !isPersistedRuntimeAlias(path, value) {
				t.Errorf("runtime string leaks reference product identity: %s", path)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Historical configuration IDs are not display labels or model instructions.
// Permit only these exact inputs at their single normalization boundary; all
// other strings, including new strings in that file, remain subject to the gate.
func isPersistedRuntimeAlias(path, value string) bool {
	if !strings.HasSuffix(filepath.ToSlash(path), "/internal/kernel/conda_runtime_identity.go") {
		return false
	}
	return value == "claude-science-python" || value == "claude-science-r"
}

func TestPersistedRuntimeAliasExceptionIsPathAndValueScoped(t *testing.T) {
	path := "/repo/internal/kernel/conda_runtime_identity.go"
	if !isPersistedRuntimeAlias(path, "claude-science-python") ||
		isPersistedRuntimeAlias("/repo/internal/kernel/prompt.go", "claude-science-python") ||
		isPersistedRuntimeAlias(path, "claude-science-new-label") {
		t.Fatal("legacy identity exception escaped its exact path/value boundary")
	}
}

func walkModelTextFiles(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || strings.Contains(strings.ToUpper(entry.Name()), "THIRD_PARTY") ||
			strings.HasSuffix(entry.Name(), ".orig") {
			return nil
		}
		extension := strings.ToLower(filepath.Ext(path))
		if extension != ".md" && extension != ".yaml" && extension != ".yml" && extension != ".json" && extension != ".py" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if containsReferenceProductLabel(string(raw)) {
			t.Errorf("model-visible text leaks reference product identity: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func containsReferenceProductLabel(value string) bool {
	value = strings.ToLower(value)
	return strings.Contains(value, "claude science") || strings.Contains(value, "claude-science") ||
		strings.Contains(value, "llm science")
}
