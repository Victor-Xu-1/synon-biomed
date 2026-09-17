package server

import (
	"os"
	"testing"
)

func TestPublicScientificYAMLContractAcceptsSafeMechanismAndRejectsHTML(t *testing.T) {
	request, err := parseAgentPublicScientificFileRequest(map[string]any{
		"url":               "https://raw.githubusercontent.com/Cantera/cantera/main/data/gri30.yaml",
		"human_description": "Downloading the public chemical mechanism",
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.Filename != "gri30.yaml" || len(request.AcceptedTypes) == 0 {
		t.Fatalf("request=%#v", request)
	}

	mechanism, err := os.CreateTemp(t.TempDir(), "mechanism-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer mechanism.Close()
	if _, err := mechanism.WriteString("description: GRI-Mech 3.0\nphases:\n  - name: gri30\n"); err != nil {
		t.Fatal(err)
	}
	contentType, err := verifyAgentPublicScientificStagedContent(
		mechanism, request.Filename, "text/plain; charset=utf-8", request.AcceptedTypes,
	)
	if err != nil || contentType != "application/yaml" {
		t.Fatalf("content_type=%q err=%v", contentType, err)
	}

	spoofed, err := os.CreateTemp(t.TempDir(), "spoofed-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer spoofed.Close()
	if _, err := spoofed.WriteString("<!doctype html><html><body>upstream error</body></html>"); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyAgentPublicScientificStagedContent(
		spoofed, request.Filename, "text/plain", request.AcceptedTypes,
	); err == nil {
		t.Fatal("HTML error page was accepted as a YAML scientific file")
	}
}

func TestPublicScientificDATContractAcceptsSafeTextAndRejectsSpoofedContent(t *testing.T) {
	request, err := parseAgentPublicScientificFileRequest(map[string]any{
		"url":               "https://example.org/equilibrium.dat",
		"human_description": "Downloading a public scientific parameter database",
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.Filename != "equilibrium.dat" || len(request.AcceptedTypes) == 0 {
		t.Fatalf("request=%#v", request)
	}

	dataFile, err := os.CreateTemp(t.TempDir(), "equilibrium-*.dat")
	if err != nil {
		t.Fatal(err)
	}
	defer dataFile.Close()
	if _, err := dataFile.WriteString("SOLUTION_MASTER_SPECIES\nC C(+4) 2.0\n"); err != nil {
		t.Fatal(err)
	}
	contentType, err := verifyAgentPublicScientificStagedContent(
		dataFile, request.Filename, "application/octet-stream", request.AcceptedTypes,
	)
	if err != nil || contentType != "text/plain" {
		t.Fatalf("content_type=%q err=%v", contentType, err)
	}

	spoofed, err := os.CreateTemp(t.TempDir(), "spoofed-*.dat")
	if err != nil {
		t.Fatal(err)
	}
	defer spoofed.Close()
	if _, err := spoofed.WriteString("<html><body>upstream error</body></html>"); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyAgentPublicScientificStagedContent(
		spoofed, request.Filename, "text/plain", request.AcceptedTypes,
	); err == nil {
		t.Fatal("HTML error page was accepted as a .dat scientific file")
	}
}
