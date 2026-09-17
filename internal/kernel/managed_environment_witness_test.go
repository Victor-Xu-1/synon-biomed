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
