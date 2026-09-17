package assets

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBundledSkillLicenseNoticesPreserveScopedGrants(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate source root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	for _, test := range []struct {
		path    string
		markers []string
	}{
		{"skills/synonbiomed/ngs-analysis-router/THIRD_PARTY_NOTICES.md", []string{
			"11c74d6ba24d3a6d48f54a194cd00ef3beea18f9", "MIT License",
			"Permission is hereby granted, free of charge", "THE SOFTWARE IS PROVIDED",
		}},
		{"skills/synonbiomed/complexa-slurm/THIRD_PARTY_NOTICES.md", []string{
			"4e8fda769bd773538cb7168c849bd712c1b51b7b", "Apache-2.0", "CC-BY-4.0",
		}},
		{"docs/THIRD_PARTY.md", []string{
			"## Retained Material With Unresolved Provenance",
			"assets/optional/kernels/kernel_worker.R",
			"assets/optional/kernels/synon_host_bridge.py",
			"assets/optional/compute/operon_compute_provider/",
			"assets/synonbiomed/agents/bookmarker/metadata.yaml",
			"Publication does not establish a redistribution grant",
		}},
	} {
		t.Run(filepath.Base(filepath.Dir(test.path)), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(test.path)))
			if err != nil {
				t.Fatal(err)
			}
			for _, marker := range test.markers {
				if !strings.Contains(string(raw), marker) {
					t.Errorf("required license notice absent: %q", marker)
				}
			}
		})
	}
}
