package kernel

import (
	"context"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

func TestVerifyManagedEnvironmentExecutableRejectsPathAuthority(t *testing.T) {
	manager := &Manager{}
	for _, executable := range []string{"../tool", "bin/tool", "tool\x00name", ""} {
		if err := manager.VerifyManagedEnvironmentExecutable("environment", executable); err == nil {
			t.Fatalf("unsafe executable %q was accepted", executable)
		}
	}
}

func TestVerifyManagedEnvironmentImportsRejectsUnsafeWitnessBeforeEnvironmentLookup(t *testing.T) {
	manager := &Manager{}
	if err := manager.VerifyManagedEnvironmentImports(context.Background(), "environment", []string{"os;raise SystemExit()"}); err == nil {
		t.Fatal("unsafe import witness was accepted")
	}
}

func TestRunManagedPythonSourceImportProbeUsesTheSelectedInterpreterAPI(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := runManagedPythonSourceImportProbe(ctx, python, "", `
from math import sqrt, definitely_missing
import json as js
value = sqrt(4)
encoded = js.dumps({"value": value})
`)
	if err != nil {
		t.Fatalf("source import probe: %v", err)
	}
	if !reflect.DeepEqual(result.Missing, []string{"math.definitely_missing"}) {
		t.Fatalf("missing symbols=%#v", result.Missing)
	}
}

func TestRunManagedPythonSourceImportProbeAcceptsAvailableAliasedAttributes(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := runManagedPythonSourceImportProbe(ctx, python, "", `
import math as m
value = m.sqrt(9)
`)
	if err != nil {
		t.Fatalf("source import probe: %v", err)
	}
	if len(result.Missing) != 0 {
		t.Fatalf("available symbols reported missing=%#v", result.Missing)
	}
}

func TestRunManagedPythonSourceImportProbeLoadsFromSubmodules(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := runManagedPythonSourceImportProbe(ctx, python, "", `
from urllib import request as client
from xml.etree import ElementTree as tree
client.Request("https://example.org")
tree.Element("root")
`)
	if err != nil || len(result.Missing) != 0 {
		t.Fatalf("valid submodule imports rejected: %#v %v", result, err)
	}
}

func TestRunManagedPythonSourceImportProbeDoesNotInventScopedRequirements(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := runManagedPythonSourceImportProbe(ctx, python, "", `
try:
    import missing_optional_accelerator
except ImportError:
    pass
import math as m
def function(m):
    return m.custom_interface()
callback = lambda m: m.custom_interface()
m = object()
m.custom_interface()
`)
	if err != nil || len(result.Missing) != 0 {
		t.Fatalf("dynamic scope treated as missing API: %#v %v", result, err)
	}
}
