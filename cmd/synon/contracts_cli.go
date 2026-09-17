package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"synon-go/internal/compat/oracle"
)

func runContractsCLI(args []string, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: synon-go contracts <scaffold|verify|report|capture-http|compare> [options]")
	}
	switch args[0] {
	case "scaffold":
		return runContractsScaffoldCLI(args[1:], output)
	case "verify":
		return runContractsVerifyCLI(args[1:], output)
	case "report":
		return runContractsReportCLI(args[1:], output)
	case "capture-http":
		return runContractsCaptureHTTPCLI(args[1:], output)
	case "compare":
		return runContractsCompareCLI(args[1:], output)
	default:
		return errors.New("usage: synon-go contracts <scaffold|verify|report|capture-http|compare> [options]")
	}
}

type contractInventoryReport struct {
	Kind      string         `json:"kind"`
	Authority string         `json:"authority"`
	Baseline  string         `json:"baseline"`
	File      string         `json:"file"`
	SHA256    string         `json:"sha256"`
	Summary   map[string]int `json:"summary"`
}

type currentCompatibilityReport struct {
	SchemaVersion                 int                        `json:"schemaVersion"`
	Scope                         string                     `json:"scope"`
	Baseline                      string                     `json:"baseline"`
	Authority                     string                     `json:"authority"`
	CompatibilityBaselineEligible bool                       `json:"compatibilityBaselineEligible"`
	Behavior                      contractVerificationReport `json:"behavior"`
	BehaviorSHA256                string                     `json:"behaviorSha256"`
	Inventories                   []contractInventoryReport  `json:"inventories"`
}

func runContractsReportCLI(args []string, output io.Writer) error {
	flags := newOperationalFlagSet("contracts report")
	root := flags.String("root", "", "repository root")
	evidence := flags.String("evidence", "docs/compatibility/v1.1-behavior-evidence.json", "behavior evidence path relative to root")
	target := flags.String("output", "docs/compatibility/current-report.json", "generated report path relative to root")
	force := flags.Bool("force", false, "replace an existing report")
	if err := parseOperationalFlags(flags, args); err != nil {
		return err
	}
	resolvedRoot, err := resolveOperationalRoot(*root)
	if err != nil {
		return err
	}
	evidencePath, err := resolveContractEvidencePath(resolvedRoot, *evidence)
	if err != nil {
		return err
	}
	targetPath, err := resolveContractsRelativePath(resolvedRoot, *target, "--output")
	if err != nil {
		return err
	}
	manifest, err := oracle.Load(evidencePath)
	if err != nil {
		return err
	}
	verification, verifyErr := oracle.Verify(resolvedRoot, manifest)
	if verifyErr != nil && !errors.Is(verifyErr, oracle.ErrIncomplete) {
		return verifyErr
	}
	behaviorData, err := os.ReadFile(evidencePath)
	if err != nil {
		return fmt.Errorf("read behavior evidence for report: %w", err)
	}
	behaviorDigest := sha256.Sum256(behaviorData)
	behavior := contractVerificationReport{
		Valid: verification.Valid, ServiceTotal: manifest.Summary.Services,
		HTTPRouteTotal: manifest.Summary.HTTPRoutes, EventTotal: manifest.Summary.Events,
		QueryTotal: manifest.Summary.Queries, NotApplicable: verification.FrontendOwned,
		FrontendOwned: verification.FrontendOwned, InScope: verification.InScope,
		Implemented: verification.Evidenced, Missing: verification.Missing,
		Blocked: verification.Blocked, EvidenceFile: filepath.ToSlash(*evidence),
	}
	inventories := make([]contractInventoryReport, 0, 2)
	for _, spec := range []struct {
		kind string
		path string
		want map[string]int
	}{
		{"service-contracts", "docs/compatibility/v1.1-service-contract-coverage.json", map[string]int{"total": manifest.Summary.Services}},
		{"realtime-contracts", "docs/compatibility/v1.1-realtime-contract-coverage.json", map[string]int{"eventsTotal": manifest.Summary.Events, "queriesTotal": manifest.Summary.Queries}},
	} {
		inventory, err := loadContractInventory(resolvedRoot, spec.kind, spec.path, spec.want)
		if err != nil {
			return err
		}
		inventories = append(inventories, inventory)
	}
	report := currentCompatibilityReport{
		SchemaVersion: 2, Scope: "historical-non-web-compatibility", Baseline: manifest.Baseline,
		Authority: "strict-behavior-evidence", CompatibilityBaselineEligible: verification.Valid,
		Behavior: behavior, BehaviorSHA256: hex.EncodeToString(behaviorDigest[:]),
		Inventories: inventories,
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal compatibility report: %w", err)
	}
	data = append(data, '\n')
	if err := writeContractOutput(targetPath, data, *force); err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	return writeMigrationJSON(output, map[string]any{
		"created": filepath.ToSlash(*target), "sha256": hex.EncodeToString(digest[:]),
		"scope":                         "historical-non-web-compatibility",
		"compatibilityBaselineEligible": verification.Valid, "evidenced": verification.Evidenced,
		"missing": verification.Missing,
	})
}

func loadContractInventory(root, kind, relativePath string, want map[string]int) (contractInventoryReport, error) {
	path, err := resolveContractsRelativePath(root, relativePath, "inventory")
	if err != nil {
		return contractInventoryReport{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return contractInventoryReport{}, fmt.Errorf("read %s inventory: %w", kind, err)
	}
	var document struct {
		Baseline string         `json:"baseline"`
		Summary  map[string]int `json:"summary"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return contractInventoryReport{}, fmt.Errorf("decode %s inventory: %w", kind, err)
	}
	if document.Baseline != "synonbiomed-v1.1" && !strings.HasPrefix(document.Baseline, "synonbiomed-v1.1 ") {
		return contractInventoryReport{}, fmt.Errorf("%s inventory baseline=%q", kind, document.Baseline)
	}
	for key, expected := range want {
		if document.Summary[key] != expected {
			return contractInventoryReport{}, fmt.Errorf("%s inventory %s=%d, want %d", kind, key, document.Summary[key], expected)
		}
	}
	digest := sha256.Sum256(data)
	return contractInventoryReport{
		Kind: kind, Authority: "inventory-only", Baseline: document.Baseline, File: relativePath,
		SHA256: hex.EncodeToString(digest[:]), Summary: document.Summary,
	}, nil
}

func runContractsScaffoldCLI(args []string, output io.Writer) error {
	flags := newOperationalFlagSet("contracts scaffold")
	root := flags.String("root", "", "release or repository root")
	evidence := flags.String("evidence", "docs/compatibility/v1.1-behavior-evidence.json", "behavior evidence path relative to root")
	force := flags.Bool("force", false, "replace an existing scaffold file")
	if err := parseOperationalFlags(flags, args); err != nil {
		return err
	}
	resolvedRoot, err := resolveOperationalRoot(*root)
	if err != nil {
		return err
	}
	evidencePath, err := resolveContractEvidencePath(resolvedRoot, *evidence)
	if err != nil {
		return err
	}
	manifest := oracle.Scaffold()
	data, err := oracle.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := writeContractOutput(evidencePath, data, *force); err != nil {
		return err
	}
	report, verifyErr := oracle.Verify(resolvedRoot, manifest)
	if verifyErr != nil && !errors.Is(verifyErr, oracle.ErrIncomplete) {
		return verifyErr
	}
	return writeMigrationJSON(output, map[string]any{
		"created":       filepath.ToSlash(*evidence),
		"total":         report.Total,
		"inScope":       report.InScope,
		"evidenced":     report.Evidenced,
		"missing":       report.Missing,
		"frontendOwned": report.FrontendOwned,
	})
}

func runContractsVerifyCLI(args []string, output io.Writer) error {
	flags := newOperationalFlagSet("contracts verify")
	root := flags.String("root", "", "release or repository root")
	evidence := flags.String("evidence", "docs/compatibility/v1.1-behavior-evidence.json", "behavior evidence path relative to root")
	if err := parseOperationalFlags(flags, args); err != nil {
		return err
	}
	resolvedRoot, err := resolveOperationalRoot(*root)
	if err != nil {
		return err
	}
	return runVerifyContractsCLI([]string{
		"--root", resolvedRoot,
		"--evidence", filepath.ToSlash(*evidence),
	}, output)
}

func runContractsCaptureHTTPCLI(args []string, output io.Writer) error {
	flags := newOperationalFlagSet("contracts capture-http")
	root := flags.String("root", "", "repository root")
	scenario := flags.String("scenario", "", "scenario path relative to root")
	baseURL := flags.String("base-url", "", "loopback runtime base URL")
	runtimeName := flags.String("runtime", "", "captured runtime identity")
	target := flags.String("output", "", "capture output path relative to root")
	timeout := flags.Duration("timeout", 30*time.Second, "scenario timeout")
	force := flags.Bool("force", false, "replace an existing capture")
	if err := parseOperationalFlags(flags, args); err != nil {
		return err
	}
	if *timeout <= 0 || *timeout > 10*time.Minute {
		return errors.New("--timeout must be greater than zero and no more than 10m")
	}
	resolvedRoot, err := resolveOperationalRoot(*root)
	if err != nil {
		return err
	}
	scenarioPath, err := resolveContractsRelativePath(resolvedRoot, *scenario, "--scenario")
	if err != nil {
		return err
	}
	targetPath, err := resolveContractsRelativePath(resolvedRoot, *target, "--output")
	if err != nil {
		return err
	}
	spec, err := oracle.LoadScenario(scenarioPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	capture, err := oracle.RunHTTPScenario(
		ctx,
		&http.Client{Timeout: *timeout},
		*baseURL,
		*runtimeName,
		spec,
	)
	if err != nil {
		return err
	}
	data, err := oracle.MarshalCapture(capture)
	if err != nil {
		return err
	}
	if err := writeContractOutput(targetPath, data, *force); err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	return writeMigrationJSON(output, map[string]any{
		"captured": filepath.ToSlash(*target),
		"scenario": spec.ID,
		"runtime":  strings.TrimSpace(*runtimeName),
		"steps":    len(capture.Steps),
		"bytes":    len(data),
		"sha256":   hex.EncodeToString(digest[:]),
	})
}

func runContractsCompareCLI(args []string, output io.Writer) error {
	flags := newOperationalFlagSet("contracts compare")
	root := flags.String("root", "", "repository root")
	scenario := flags.String("scenario", "", "scenario path relative to root")
	baseline := flags.String("baseline", "", "baseline capture path relative to root")
	candidate := flags.String("candidate", "", "candidate capture path relative to root")
	if err := parseOperationalFlags(flags, args); err != nil {
		return err
	}
	resolvedRoot, err := resolveOperationalRoot(*root)
	if err != nil {
		return err
	}
	scenarioPath, err := resolveContractsRelativePath(resolvedRoot, *scenario, "--scenario")
	if err != nil {
		return err
	}
	baselinePath, err := resolveContractsRelativePath(resolvedRoot, *baseline, "--baseline")
	if err != nil {
		return err
	}
	candidatePath, err := resolveContractsRelativePath(resolvedRoot, *candidate, "--candidate")
	if err != nil {
		return err
	}
	spec, err := oracle.LoadScenario(scenarioPath)
	if err != nil {
		return err
	}
	baselineData, err := readContractsFile(baselinePath)
	if err != nil {
		return fmt.Errorf("read baseline capture: %w", err)
	}
	candidateData, err := readContractsFile(candidatePath)
	if err != nil {
		return fmt.Errorf("read candidate capture: %w", err)
	}
	if err := oracle.CompareJSONMode(baselineData, candidateData, spec.Normalization, spec.ComparisonMode); err != nil {
		return err
	}
	return writeMigrationJSON(output, map[string]any{
		"equal":          true,
		"scenario":       spec.ID,
		"baseline":       filepath.ToSlash(*baseline),
		"candidate":      filepath.ToSlash(*candidate),
		"normalizations": len(spec.Normalization),
		"comparisonMode": spec.ComparisonMode,
	})
}

func resolveContractsRelativePath(root string, configured string, flagName string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured == "" || filepath.IsAbs(configured) || filepath.VolumeName(configured) != "" {
		return "", fmt.Errorf("%s must be a non-empty path relative to --root", flagName)
	}
	clean := filepath.Clean(filepath.FromSlash(configured))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s escapes --root", flagName)
	}
	resolved := filepath.Join(root, clean)
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s escapes --root", flagName)
	}
	return resolved, nil
}

func readContractsFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("capture must be a regular non-symlink file")
	}
	return os.ReadFile(path)
}

func writeContractOutput(target string, data []byte, force bool) error {
	target = filepath.Clean(target)
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("contracts output target is a symlink: %s", target)
		}
		if !force {
			return fmt.Errorf("contracts output already exists: %s; pass --force to replace it", target)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect contracts output: %w", err)
	}

	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create contracts output directory: %w", err)
	}
	temporary, err := os.CreateTemp(parent, "."+filepath.Base(target)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create contracts output temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set contracts output permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write contracts output: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync contracts output: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close contracts output: %w", err)
	}

	if force {
		if err := os.Rename(temporaryPath, target); err != nil {
			return fmt.Errorf("replace contracts output: %w", err)
		}
	} else {
		if err := os.Link(temporaryPath, target); err != nil {
			if errors.Is(err, os.ErrExist) {
				return fmt.Errorf("contracts output already exists: %s", target)
			}
			return fmt.Errorf("activate contracts output: %w", err)
		}
	}
	if directory, err := os.Open(parent); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
