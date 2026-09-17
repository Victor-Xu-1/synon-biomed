package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"synon-go/internal/buildinfo"
)

func TestRunReleaseSupplyChainCLICreatesAndVerifiesRealGoBinaryEvidence(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	binary := filepath.Join(root, "synon-go-test")
	content, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, content, 0o755); err != nil {
		t.Fatal(err)
	}
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	moduleCacheOutput, err := exec.Command(goBinary, "env", "GOMODCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	moduleCache := strings.TrimSpace(string(moduleCacheOutput))
	identity := buildinfo.Release()
	t.Setenv("SOURCE_DATE_EPOCH", "1783872000")
	var created bytes.Buffer
	if err := runReleaseSupplyChainCLI([]string{
		"create", "--root", root, "--goos", runtime.GOOS, "--goarch", runtime.GOARCH,
		"--binaries", "synon-go-test", "--module-cache", moduleCache, "--go-root", runtime.GOROOT(),
		"--source-uri", fmt.Sprintf("pkg:generic/%s@%s", identity.MachineSlug, identity.Version), "--source-revision", "test-revision",
		"--source-sha256", strings.Repeat("a", 64), "--source-dirty",
	}, &created); err != nil {
		t.Fatalf("create supply chain: %v", err)
	}
	if !strings.Contains(created.String(), "\"valid\": true") {
		t.Fatalf("create output = %s", created.String())
	}
	var verified bytes.Buffer
	if err := runReleaseSupplyChainCLI([]string{"verify", "--root", root}, &verified); err != nil {
		t.Fatalf("verify supply chain: %v", err)
	}
	if !strings.Contains(verified.String(), "\"valid\": true") {
		t.Fatalf("verify output = %s", verified.String())
	}
}
