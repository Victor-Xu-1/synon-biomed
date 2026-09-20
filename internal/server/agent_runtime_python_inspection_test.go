package server

import "testing"

func TestManagedPythonAPIInspectionDoesNotAuthorizeScientificExecution(t *testing.T) {
	if agentRuntimePythonCompilerExecutable() == "" {
		t.Skip("Python AST parser unavailable")
	}
	for _, source := range []string{
		"import package\nprint(dir(package))",
		"import package as p; print(p.__version__)",
		"from package import Model\nimport inspect as i\nprint(i.signature(Model))",
	} {
		if !isManagedPythonAPIInspection(source) {
			t.Fatalf("inspection rejected: %q", source)
		}
	}
	for _, source := range []string{
		"import package",
		"import package\npackage.run()",
		"import package\nprint(dir(package.run()))",
		"import package\nprint(package.run())",
		"import package\nprint(dir(package)); open('result', 'w').write('altered')",
		"import package as print\nprint(dir(print))",
		"from package import run as dir\nimport package\nprint(dir(package))",
		"import package\nprint = package.run\nprint(dir(package))",
		"import package\nprint([x() for x in dir(package)])",
		"import package\nprint(dir(package), file=open('result', 'w'))",
		"import package\nprint(f'{package.run()}')",
	} {
		if isManagedPythonAPIInspection(source) {
			t.Fatalf("execution misclassified as inspection: %q", source)
		}
	}
}
