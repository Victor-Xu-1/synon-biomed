package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	assetpkg "synon-go/internal/assets"
	"synon-go/internal/buildinfo"
	"synon-go/internal/compat/oracle"
	"synon-go/internal/config"
)

const operationalResponseLimit = 8 << 20

type contractVerificationReport struct {
	Valid          bool   `json:"valid"`
	ServiceTotal   int    `json:"serviceTotal"`
	HTTPRouteTotal int    `json:"httpRouteTotal"`
	EventTotal     int    `json:"eventTotal"`
	QueryTotal     int    `json:"queryTotal"`
	NotApplicable  int    `json:"notApplicable"`
	FrontendOwned  int    `json:"frontendOwned"`
	InScope        int    `json:"inScope"`
	Implemented    int    `json:"implemented"`
	Missing        int    `json:"missing"`
	Blocked        int    `json:"blocked"`
	EvidenceFile   string `json:"evidenceFile"`
}

func runVerifyContractsCLI(args []string, output io.Writer) error {
	flags := newOperationalFlagSet("verify-contracts")
	root := flags.String("root", "", "release or repository root")
	evidence := flags.String("evidence", "docs/compatibility/v1.1-behavior-evidence.json", "behavior evidence path relative to root")
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
	manifest, err := oracle.Load(evidencePath)
	if err != nil {
		return err
	}
	verification, verificationErr := oracle.Verify(resolvedRoot, manifest)
	report := contractVerificationReport{
		Valid:          verification.Valid,
		ServiceTotal:   manifest.Summary.Services,
		HTTPRouteTotal: manifest.Summary.HTTPRoutes,
		EventTotal:     manifest.Summary.Events,
		QueryTotal:     manifest.Summary.Queries,
		NotApplicable:  verification.FrontendOwned,
		FrontendOwned:  verification.FrontendOwned,
		InScope:        verification.InScope,
		Implemented:    verification.Evidenced,
		Missing:        verification.Missing,
		Blocked:        verification.Blocked,
		EvidenceFile:   filepath.ToSlash(*evidence),
	}
	if err := writeMigrationJSON(output, report); err != nil {
		return err
	}
	return verificationErr
}

func resolveContractEvidencePath(root string, configured string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured == "" || filepath.IsAbs(configured) || filepath.VolumeName(configured) != "" {
		return "", errors.New("--evidence must be a non-empty path relative to --root")
	}
	clean := filepath.Clean(filepath.FromSlash(configured))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("--evidence escapes --root")
	}
	resolved := filepath.Join(root, clean)
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("--evidence escapes --root")
	}
	return resolved, nil
}

type operationalAssetPack struct {
	Name         string
	ManifestPath string
	AssetRoot    string
}

type assetPackVerification struct {
	Name       string `json:"name"`
	Manifest   string `json:"manifest"`
	Runtime    string `json:"runtime,omitempty"`
	Optional   bool   `json:"optional,omitempty"`
	Files      int    `json:"files"`
	Checked    int    `json:"checked"`
	TotalBytes int64  `json:"totalBytes"`
}

type assetVerificationReport struct {
	Valid      bool                    `json:"valid"`
	Root       string                  `json:"root"`
	Packs      []assetPackVerification `json:"packs"`
	Checked    int                     `json:"checked"`
	TotalBytes int64                   `json:"totalBytes"`
}

func operationalAssetPacks(root string) []operationalAssetPack {
	packs := []operationalAssetPack{
		{Name: "skills", ManifestPath: filepath.Join(root, "assets", "synonbiomed", "skills.manifest.json"), AssetRoot: filepath.Join(root, "skills", "synonbiomed")},
		{Name: "agents", ManifestPath: filepath.Join(root, "assets", "synonbiomed", "agents.manifest.json"), AssetRoot: filepath.Join(root, "assets", "synonbiomed", "agents")},
		{Name: "kernel-compute", ManifestPath: filepath.Join(root, "assets", "optional", "kernel-compute.manifest.json"), AssetRoot: filepath.Join(root, "assets", "optional")},
		{Name: "bio-tools", ManifestPath: filepath.Join(root, "assets", "optional", "mcp-servers", "bio-tools.manifest.json"), AssetRoot: filepath.Join(root, "assets", "optional", "mcp-servers", "bio-tools")},
		{Name: "ketcher-chemistry", ManifestPath: filepath.Join(root, "assets", "optional", "mcp-servers", "ketcher-chemistry.manifest.json"), AssetRoot: filepath.Join(root, "assets", "optional", "mcp-servers", "ketcher-chemistry")},
		{Name: "synon-link", ManifestPath: filepath.Join(root, "assets", "synon-link", "manifest.json"), AssetRoot: filepath.Join(root, "assets", "synon-link")},
	}
	micromambaManifest := filepath.Join(root, "assets", "optional", "micromamba", "manifest.json")
	if info, err := os.Stat(micromambaManifest); err == nil && info.Mode().IsRegular() {
		packs = append(packs, operationalAssetPack{
			Name: "micromamba", ManifestPath: micromambaManifest,
			AssetRoot: filepath.Join(root, "assets", "optional", "micromamba"),
		})
	}
	return packs
}

func runAssetsCLI(args []string, output io.Writer) error {
	if len(args) == 0 || (args[0] != "list" && args[0] != "verify") {
		return errors.New("usage: synon-go assets <list|verify> [--root <release-or-repository-root>]")
	}
	action := args[0]
	flags := newOperationalFlagSet("assets " + action)
	root := flags.String("root", "", "release or repository root")
	if err := parseOperationalFlags(flags, args[1:]); err != nil {
		return err
	}
	resolvedRoot, err := resolveOperationalRoot(*root)
	if err != nil {
		return err
	}
	report := assetVerificationReport{Valid: true, Root: resolvedRoot, Packs: make([]assetPackVerification, 0, 7)}
	for _, pack := range operationalAssetPacks(resolvedRoot) {
		manifest, err := assetpkg.Load(pack.ManifestPath)
		if err != nil {
			return fmt.Errorf("%s asset pack: %w", pack.Name, err)
		}
		item := assetPackVerification{
			Name: pack.Name, Manifest: pack.ManifestPath, Runtime: manifest.Runtime.Kind,
			Optional: manifest.Runtime.Optional, Files: len(manifest.Files),
		}
		if action == "verify" {
			verified, err := assetpkg.Verify(pack.AssetRoot, manifest)
			if err != nil {
				return fmt.Errorf("%s asset pack: %w", pack.Name, err)
			}
			item.Checked = verified.Checked
			item.TotalBytes = verified.TotalBytes
			report.Checked += verified.Checked
			report.TotalBytes += verified.TotalBytes
		}
		report.Packs = append(report.Packs, item)
	}
	return writeMigrationJSON(output, report)
}

type doctorCLIReport struct {
	BaseURL string         `json:"baseUrl"`
	Health  map[string]any `json:"health"`
	Doctor  map[string]any `json:"doctor"`
}

func runDoctorCLI(ctx context.Context, args []string, output io.Writer) error {
	flags := newOperationalFlagSet("doctor")
	baseURL := flags.String("base-url", "", "running "+buildinfo.Release().Name+" base URL")
	scope := flags.String("scope", "all", "AgentRuntimeDoctor scope")
	timeout := flags.Duration("timeout", 15*time.Second, "HTTP request timeout")
	allowNotReady := flags.Bool("allow-not-ready", false, "return success after printing a non-ready report")
	if err := parseOperationalFlags(flags, args); err != nil {
		return err
	}
	if *timeout <= 0 {
		return errors.New("--timeout must be positive")
	}
	resolvedBaseURL, err := resolveDoctorBaseURL(*baseURL)
	if err != nil {
		return err
	}
	requestContext, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	client := &http.Client{Timeout: *timeout}
	health := map[string]any{}
	if err := requestOperationalJSON(requestContext, client, http.MethodGet, resolvedBaseURL+"/health", nil, &health); err != nil {
		return fmt.Errorf("runtime health: %w", err)
	}
	var envelope struct {
		Result map[string]any `json:"result"`
		Error  any            `json:"error"`
	}
	if err := requestOperationalJSON(requestContext, client, http.MethodPost, resolvedBaseURL+"/api/tools/AgentRuntimeDoctor/execute", map[string]any{"input": map[string]any{"scope": strings.TrimSpace(*scope)}}, &envelope); err != nil {
		return fmt.Errorf("runtime doctor: %w", err)
	}
	if envelope.Result == nil {
		return fmt.Errorf("runtime doctor returned no result: %v", envelope.Error)
	}
	if err := writeMigrationJSON(output, doctorCLIReport{BaseURL: resolvedBaseURL, Health: health, Doctor: envelope.Result}); err != nil {
		return err
	}
	ready, _ := envelope.Result["ok"].(bool)
	if !ready && !*allowNotReady {
		return errors.New("runtime doctor reported not ready")
	}
	return nil
}

func requestOperationalJSON(ctx context.Context, client *http.Client, method, target string, body any, output any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, operationalResponseLimit+1)
	content, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if len(content) > operationalResponseLimit {
		return errors.New("response exceeds 8 MiB limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(content)))
	}
	if err := json.Unmarshal(content, output); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	return nil
}

func resolveDoctorBaseURL(value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if value == "" {
		value = strings.TrimRight(strings.TrimSpace(os.Getenv("SYNON_BASE_URL")), "/")
	}
	if value == "" {
		cfg, err := config.Load()
		if err != nil {
			return "", fmt.Errorf("load runtime config for doctor: %w", err)
		}
		host, port, err := net.SplitHostPort(cfg.Address())
		if err != nil {
			return "", fmt.Errorf("resolve runtime address for doctor: %w", err)
		}
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		value = "http://" + net.JoinHostPort(host, port)
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("--base-url must be an HTTP(S) origin without credentials, query, or fragment")
	}
	return value, nil
}

func resolveOperationalRoot(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		executable, err := os.Executable()
		if err != nil {
			return "", err
		}
		value = filepath.Dir(executable)
	}
	root, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("operational root is not a directory: %s", root)
	}
	return root, nil
}

func runtimeAssetRoots() []string {
	roots := make([]string, 0, 2)
	if executable, err := os.Executable(); err == nil {
		roots = append(roots, filepath.Dir(executable))
	}
	if workingDirectory, err := os.Getwd(); err == nil {
		roots = append(roots, workingDirectory)
	}
	return roots
}

func resolveRuntimeAssetPath(value string, roots ...string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		candidate := filepath.Join(root, filepath.FromSlash(value))
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() {
			resolved, err := filepath.Abs(candidate)
			if err == nil {
				return filepath.Clean(resolved)
			}
		}
	}
	return value
}

func resolveRuntimeAssetDirectoryPath(value string, roots ...string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if filepath.IsAbs(value) {
		info, err := os.Stat(value)
		if err == nil && info.IsDir() {
			resolved, resolveErr := filepath.Abs(value)
			if resolveErr == nil {
				return filepath.Clean(resolved)
			}
		}
		return ""
	}
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		candidate := filepath.Join(root, filepath.FromSlash(value))
		info, err := os.Stat(candidate)
		if err != nil || !info.IsDir() {
			continue
		}
		resolved, err := filepath.Abs(candidate)
		if err == nil {
			return filepath.Clean(resolved)
		}
	}
	return ""
}

func readOperationalJSON(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, operationalResponseLimit+1))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%s contains multiple JSON values", path)
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

func normalizeMigrationArgs(args []string) []string {
	result := append([]string(nil), args...)
	if len(result) > 0 && result[0] == "run" {
		result[0] = "migrate"
	}
	return result
}

func normalizeServeArgs(args []string) []string {
	if len(args) > 0 && args[0] == "serve" {
		return append([]string(nil), args[1:]...)
	}
	return append([]string(nil), args...)
}

func newOperationalFlagSet(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}

func parseOperationalFlags(flags *flag.FlagSet, args []string) error {
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	return nil
}
