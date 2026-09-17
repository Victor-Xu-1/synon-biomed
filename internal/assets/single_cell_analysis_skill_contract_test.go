package assets_test

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"synon-go/internal/skills"
)

func TestSingleCellAnalysisIsTheDefaultLightweightRoute(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	skillRoot := filepath.Join(repositoryRoot, "skills", "synonbiomed")
	catalog := skills.Load([]string{skillRoot}, skills.LoadOptions{MaxBodyBytes: 20_000})
	if loadErrors := catalog.LoadErrors(); len(loadErrors) > 0 {
		t.Fatalf("load bundled skills: %s: %s", loadErrors[0].Path, loadErrors[0].Err)
	}

	analysis, found := catalogSkillByName(catalog.Skills(), "single-cell-rna-analysis")
	if !found {
		t.Fatal("single-cell-rna-analysis is missing")
	}
	if _, found := catalogSkillByName(catalog.Skills(), "single-cell-rna-qc"); found {
		t.Fatal("retired single-cell-rna-qc route is still loaded")
	}
	for _, dependency := range []string{
		"scanpy",
		"anndata",
		"harmonypy",
		"leidenalg",
		"igraph",
		"matplotlib",
		"seaborn",
		"pandas",
		"scipy",
	} {
		if !slices.Contains(analysis.RequiredEnvironmentPackages, dependency) &&
			!slices.Contains(analysis.RequiredEnvironmentPackages, "pip::"+dependency) {
			t.Errorf("single-cell analysis environment contract is missing %q", dependency)
		}
	}
	if !slices.Contains(analysis.Tools, "download_public_scientific_file") {
		t.Fatal("single-cell analysis cannot reach the governed public-file downloader")
	}
	for _, scientificBoundary := range []string{
		"Use Scanpy PCA plus Harmony for ordinary batch-aware exploration",
		"completed receipt before parsing",
		"population proportions and paired or stratified summaries",
		"Use pseudobulk expression",
		"Do not claim treatment-associated change from pooled cell counts",
		"Prefer the tested Skill assets over rebuilding",
		"one wide gene-by-cell CSV/TSV matrix",
		"join them by exact cell ID with `--obs-file`",
		"Treat a `metadata_mapping_required` preflight as an input-contract result",
		"Use `--population-key` when an authoritative annotation column",
		"select `--matrix-kind` from evidence",
		"memory-bounded chunked matrix loading",
		"validates Harmony orientation against the number of cells",
	} {
		if !strings.Contains(analysis.Body, scientificBoundary) {
			t.Errorf("single-cell analysis contract is missing %q", scientificBoundary)
		}
	}
	if !slices.Contains(analysis.PreferredExecutionAssets, "scripts/analysis_pipeline.py") {
		t.Fatal("single-cell analysis does not recommend its canonical sparse pipeline")
	}
	if !slices.Contains(analysis.PreferredExecutionAssets, "scripts/annotation_pipeline.py") {
		t.Fatal("single-cell analysis does not recommend its evidence-backed annotation pipeline")
	}
	for _, script := range []string{
		"analysis_pipeline.py",
		"analysis_core.py",
		"annotation_pipeline.py",
		"annotation_core.py",
		"comparison_core.py",
		"matrix_manifest.py",
	} {
		if _, err := os.Stat(filepath.Join(skillRoot, "single-cell-rna-analysis", "scripts", script)); err != nil {
			t.Errorf("single-cell analysis runtime asset %s is unavailable: %v", script, err)
		}
	}

	matches := catalog.SearchNames("single-cell RNA treatment marker genes population composition response signature", 5)
	if len(matches) == 0 || matches[0] != "single-cell-rna-analysis" {
		t.Fatalf("ordinary single-cell analysis did not prefer the lightweight route: %v", matches)
	}

	scvi, found := catalogSkillByName(catalog.Skills(), "scvi-tools")
	if !found {
		t.Fatal("scvi-tools is missing")
	}
	if !slices.Contains(scvi.RequiredCapabilities, "gpu") ||
		!slices.Contains(scvi.RequiredEnvironmentPackages, "scvi-tools==1.4.2") ||
		!slices.Contains(scvi.RequiredEnvironmentPackages, "torch") {
		t.Fatalf("scvi-tools machine-readable admission contract is incomplete: capabilities=%v packages=%v", scvi.RequiredCapabilities, scvi.RequiredEnvironmentPackages)
	}
	for _, boundary := range []string{
		"verified raw integer UMI counts",
		"Do not use for ordinary QC",
		"never install scvi-tools or PyTorch incrementally during a task",
	} {
		if !strings.Contains(scvi.Description, boundary) {
			t.Errorf("scvi-tools selection boundary is missing %q", boundary)
		}
	}

	if _, err := os.Stat(filepath.Join(skillRoot, "single-cell-rna-qc")); !os.IsNotExist(err) {
		t.Fatalf("retired single-cell-rna-qc directory still exists or cannot be inspected: %v", err)
	}
}

func catalogSkillByName(values []skills.Skill, name string) (skills.Skill, bool) {
	for _, value := range values {
		if value.Name == name {
			return value, true
		}
	}
	return skills.Skill{}, false
}
