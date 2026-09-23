package kernel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	defaultManagedPythonEnvironment = "synon-biomed-python"
	defaultManagedREnvironment      = "synon-biomed-r"
	maxCondaRuntimeMetadataBytes    = 16 * 1024 * 1024
	maxManagedPythonHelperBytes     = 1 * 1024 * 1024
	managedRuntimeMarkerName        = ".synon-runtime.json"
	managedRuntimeMarkerVersion     = 2
	managedPythonActivationContract = "synon-managed-python-runtime-v2"
	managedRActivationContract      = "synon-managed-r-runtime-v1"
)

type condaRuntimeReference struct {
	Product string `json:"product"`
	Version string `json:"version"`
	Purpose string `json:"purpose,omitempty"`
}

type condaRuntimeRequirement struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type condaRuntimeCatalogEntry struct {
	Name                   string                    `json:"name"`
	Platform               string                    `json:"platform"`
	Oracle                 *condaRuntimeReference    `json:"oracle,omitempty"`
	AlignmentReference     *condaRuntimeReference    `json:"alignmentReference,omitempty"`
	RequiredPackages       []condaRuntimeRequirement `json:"requiredPackages,omitempty"`
	PackageCount           int                       `json:"packageCount"`
	Generation             string                    `json:"generation"`
	ManifestPath           string                    `json:"manifestPath"`
	ExplicitPath           string                    `json:"explicitPath"`
	LicensesPath           string                    `json:"licensesPath"`
	LicenseTextsIncluded   bool                      `json:"licenseTextsIncluded"`
	ExplicitSHA256         string                    `json:"explicitSHA256"`
	LicenseInventorySHA256 string                    `json:"licenseInventorySHA256"`
}

type condaRuntimeCatalog struct {
	SchemaVersion int                        `json:"schemaVersion"`
	Platform      string                     `json:"platform"`
	Oracle        *condaRuntimeReference     `json:"oracle,omitempty"`
	Runtimes      []condaRuntimeCatalogEntry `json:"runtimes"`
	CatalogSHA256 string                     `json:"catalogSHA256,omitempty"`
}

type condaRuntimePackage struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Build       string   `json:"build"`
	BuildNumber int      `json:"buildNumber"`
	Subdir      string   `json:"subdir"`
	Channel     string   `json:"channel"`
	URL         string   `json:"url"`
	SHA256      string   `json:"sha256"`
	License     string   `json:"license"`
	Depends     []string `json:"depends"`
}

type condaRuntimeManifest struct {
	SchemaVersion          int                       `json:"schemaVersion"`
	Name                   string                    `json:"name"`
	Platform               string                    `json:"platform"`
	Oracle                 *condaRuntimeReference    `json:"oracle,omitempty"`
	AlignmentReference     *condaRuntimeReference    `json:"alignmentReference,omitempty"`
	RequiredPackages       []condaRuntimeRequirement `json:"requiredPackages,omitempty"`
	PackageCount           int                       `json:"packageCount"`
	ExplicitSHA256         string                    `json:"explicitSHA256"`
	LicenseInventorySHA256 string                    `json:"licenseInventorySHA256"`
	Packages               []condaRuntimePackage     `json:"packages"`
	ManifestSHA256         string                    `json:"manifestSHA256,omitempty"`
}

type managedCondaRuntime struct {
	entry           condaRuntimeCatalogEntry
	manifest        condaRuntimeManifest
	explicitPath    string
	manifestDigest  string
	packageVersions map[string]string
}

type managedPythonRuntime struct {
	managedCondaRuntime
	helperDigest         string
	activationGeneration string
	rdkitVersion         string
	pythonVersion        string
}

type managedRuntimeMarker struct {
	SchemaVersion        int    `json:"schemaVersion"`
	Name                 string `json:"name"`
	Platform             string `json:"platform"`
	Generation           string `json:"generation"`
	ManifestSHA256       string `json:"manifestSHA256"`
	ExplicitSHA256       string `json:"explicitSHA256"`
	HelperManifestSHA256 string `json:"helperManifestSHA256"`
	Language             string `json:"language,omitempty"`
	RuntimeVersion       string `json:"runtimeVersion,omitempty"`
	PythonVersion        string `json:"pythonVersion,omitempty"`
	RDKitVersion         string `json:"rdkitVersion,omitempty"`
}

func managedPythonName(config Config) string {
	if value := strings.TrimSpace(config.ManagedPythonEnvironment); value != "" {
		return canonicalCondaRuntimeName(value)
	}
	return defaultManagedPythonEnvironment
}

func (m *Manager) ManagedPythonEnvironmentName() string {
	if m == nil {
		return defaultManagedPythonEnvironment
	}
	return managedPythonName(m.config)
}

func (m *Manager) ManagedPythonProvisioningEnabled() bool {
	return m != nil && strings.TrimSpace(m.config.CondaRuntimeCatalog) != "" &&
		strings.TrimSpace(m.config.CondaEnvsPath) != "" && strings.TrimSpace(m.config.Micromamba) != ""
}

// ManagedPythonCapabilityAvailable reports whether the governed scientific
// runtime is either already verified or can be provisioned from the bundled,
// content-addressed runtime contract. It does not claim that installation has
// completed and performs no mutation.
func (m *Manager) ManagedPythonCapabilityAvailable() bool {
	return m != nil && (m.ManagedPythonProvisioningEnabled() || m.RuntimeReady("python", managedPythonName(m.config)))
}

// ManagedPythonProvisioningStatus is the bounded read model used by the API.
// It intentionally exposes no installer output or filesystem detail.
func (m *Manager) ManagedPythonProvisioningStatus() string {
	return m.ManagedPythonProvisioningDetails().Status
}

// ManagedPythonProvisioningDetails is the bounded, user-visible read model
// for the service-owned bundled runtime operation. It reports progress without
// exposing installer output, command lines, or filesystem paths.
type ManagedPythonProvisioningDetails struct {
	Status         string
	Phase          string
	StartedAt      time.Time
	LastProgressAt time.Time
	Error          string
}

// ManagedPythonProvisioningDetails returns the current phase of the shared
// installation. A task waiter may leave while this operation continues; the
// timestamps make that distinction visible to status consumers.
func (m *Manager) ManagedPythonProvisioningDetails() ManagedPythonProvisioningDetails {
	if m == nil {
		return ManagedPythonProvisioningDetails{Status: "unavailable"}
	}
	return managedRuntimeProvisioningDetails(&m.managedPythonState, m.ManagedPythonProvisioningEnabled())
}

func managedRuntimeProvisioningDetails(
	state *managedRuntimeProvisioningState,
	enabled bool,
) ManagedPythonProvisioningDetails {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.provision == nil {
		if enabled {
			return ManagedPythonProvisioningDetails{Status: "pending", Phase: "waiting-to-start"}
		}
		return ManagedPythonProvisioningDetails{Status: "failed", Phase: "unavailable"}
	}
	details := ManagedPythonProvisioningDetails{
		Phase:          state.provision.phase,
		StartedAt:      state.provision.startedAt,
		LastProgressAt: state.provision.lastProgressAt,
	}
	select {
	case <-state.provision.done:
		if state.provision.err != nil {
			details.Status = "failed"
			details.Error = boundedManagedPythonProvisioningError(state.provision.err)
			return details
		}
		details.Status = "ready"
	default:
		details.Status = "installing"
	}
	return details
}

func boundedManagedPythonProvisioningError(err error) string {
	if err == nil {
		return ""
	}
	const maxErrorBytes = 2048
	message := strings.TrimSpace(err.Error())
	if len(message) > maxErrorBytes {
		return message[:maxErrorBytes] + "..."
	}
	return message
}

func (m *Manager) setManagedPythonProvisioningPhase(phase string) {
	if m == nil || strings.TrimSpace(phase) == "" {
		return
	}
	setManagedRuntimeProvisioningPhase(&m.managedPythonState, phase)
}

func setManagedRuntimeProvisioningPhase(state *managedRuntimeProvisioningState, phase string) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.provision == nil {
		return
	}
	state.provision.phase = phase
	state.provision.lastProgressAt = time.Now().UTC()
}

// RunManagedPythonProvisioner owns the installer for the service lifetime.
// Root cancellation terminates micromamba and leaves only the disposable
// staging directory; the content-addressed active generation is never changed
// before the smoke test and atomic activation complete.
func (m *Manager) RunManagedPythonProvisioner(ctx context.Context) error {
	if m == nil || !m.ManagedPythonProvisioningEnabled() {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	err := m.ProvisionManagedPythonEnvironment(ctx)
	if err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

// ProvisionManagedPythonEnvironment performs one root-context-owned attempt
// and returns as soon as the phase becomes ready or failed.
func (m *Manager) ProvisionManagedPythonEnvironment(ctx context.Context) error {
	if m == nil || !m.ManagedPythonProvisioningEnabled() {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return m.managedPythonProvisioning(ctx, ctx, true, m.ensureManagedPythonEnvironment, true)
}

// EnsureManagedPythonEnvironment joins the one process-wide provisioning
// attempt. Cancelling a waiter releases only that waiter; it never tears down
// an installation which other tasks or a later retry may still need.
func (m *Manager) EnsureManagedPythonEnvironment(ctx context.Context) error {
	if m == nil {
		return errors.New("kernel manager is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return m.managedPythonProvisioning(ctx, nil, false, m.ensureManagedPythonEnvironment, true)
}

// RetryManagedPythonEnvironment is the explicit failed-attempt recovery path.
// Concurrent callers still share one replacement attempt.
func (m *Manager) RetryManagedPythonEnvironment(ctx context.Context) error {
	if m == nil {
		return errors.New("kernel manager is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return m.managedPythonProvisioning(ctx, nil, true, m.ensureManagedPythonEnvironment, true)
}

func (m *Manager) managedPythonProvisioning(
	waitCtx context.Context,
	installParent context.Context,
	retry bool,
	install func(context.Context) error,
	wait bool,
) error {
	return m.managedRuntimeProvisioning(
		waitCtx, installParent, retry, install, wait,
		&m.managedPythonState, m.managedPythonRuntimeReady,
	)
}

func (m *Manager) managedRuntimeProvisioning(
	waitCtx context.Context,
	installParent context.Context,
	retry bool,
	install func(context.Context) error,
	wait bool,
	state *managedRuntimeProvisioningState,
	ready func() error,
) error {
	if ready() == nil {
		// A repaired/published generation can make a previously failed
		// provisioning record stale. Clear only a completed record; an active
		// installer must remain visible to concurrent status readers.
		state.mu.Lock()
		if state.provision != nil {
			select {
			case <-state.provision.done:
				state.provision = nil
			default:
			}
		}
		state.mu.Unlock()
		return nil
	}
	state.mu.Lock()
	provision := state.provision
	if provision != nil && retry {
		select {
		case <-provision.done:
			if provision.err != nil {
				provision = nil
				state.provision = nil
			}
		default:
		}
	}
	if provision == nil {
		now := time.Now().UTC()
		provision = &managedRuntimeProvision{
			done: make(chan struct{}), phase: "queued", startedAt: now, lastProgressAt: now,
		}
		state.provision = provision
		go func(active *managedRuntimeProvision) {
			if installParent == nil {
				m.managedEnvironmentMu.Lock()
				installParent = m.managedEnvironmentSupervisor
				m.managedEnvironmentMu.Unlock()
				if installParent == nil || installParent.Err() != nil {
					installParent = context.Background()
				}
			}
			// Environment construction may legitimately take hours on a new node or
			// days on a remote builder. Its lifetime belongs to the service
			// supervisor (or an explicit caller deadline), never to an invented
			// fixed wall-clock timeout. Waiters remain independently cancellable.
			err := install(installParent)
			state.mu.Lock()
			active.err = err
			close(active.done)
			state.mu.Unlock()
			m.notifyRuntimeChange()
		}(provision)
	}
	state.mu.Unlock()
	if !wait {
		return nil
	}
	select {
	case <-waitCtx.Done():
		return waitCtx.Err()
	case <-provision.done:
		state.mu.Lock()
		err := provision.err
		state.mu.Unlock()
		return err
	}
}

func readBoundedJSON(path string, target any) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxCondaRuntimeMetadataBytes {
		return nil, errors.New("runtime metadata is outside the supported size")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxCondaRuntimeMetadataBytes+1))
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("runtime metadata contains trailing JSON")
	}
	return raw, nil
}

func canonicalJSONDigest(value any) (string, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	digest := sha256.Sum256(buffer.Bytes())
	return hex.EncodeToString(digest[:]), nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func runtimeAssetPath(root, value string) (string, error) {
	if root == "" || value == "" || strings.Contains(value, "\\") || filepath.IsAbs(value) {
		return "", errors.New("runtime asset path is invalid")
	}
	candidate := filepath.Clean(filepath.Join(root, filepath.FromSlash(value)))
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", errors.New("runtime asset path escapes the catalog root")
	}
	return candidate, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validateManagedRuntimeExplicitLock(path string, platform managedRuntimePlatform, packages []condaRuntimePackage) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil || len(raw) == 0 || len(raw) > 64*1024 {
		return errors.New("managed runtime explicit lock is invalid")
	}
	lines := strings.Split(string(raw), "\n")
	if len(lines) != len(packages)+4 ||
		lines[0] != "# Generated from verified Conda package metadata. DO NOT EDIT." ||
		lines[1] != "# platform: "+platform.ID || lines[2] != "@EXPLICIT" || lines[len(lines)-1] != "" {
		return errors.New("managed runtime explicit lock does not match its manifest")
	}
	for index, item := range packages {
		if item.Subdir != "noarch" && item.Subdir != platform.CondaSubdir {
			return errors.New("managed runtime package platform does not match the host")
		}
		parsed, err := url.Parse(item.URL)
		if err != nil || parsed.Scheme != "https" || parsed.Host != "conda.anaconda.org" ||
			parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
			!strings.HasPrefix(parsed.Path, "/conda-forge/"+item.Subdir+"/") ||
			strings.Contains(parsed.Path, "\\") ||
			!(strings.HasSuffix(parsed.Path, ".conda") || strings.HasSuffix(parsed.Path, ".tar.bz2")) {
			return errors.New("managed runtime package source is invalid")
		}
		if lines[index+3] != item.URL+"#"+item.SHA256 {
			return errors.New("managed runtime explicit lock does not match its manifest")
		}
	}
	return nil
}

func loadManagedCondaRuntime(config Config, name string) (managedCondaRuntime, error) {
	platform, supported := currentManagedRuntimePlatform()
	if !supported {
		return managedCondaRuntime{}, errors.New("managed scientific runtime is unsupported on this platform")
	}
	return loadManagedCondaRuntimeForPlatform(config, name, platform)
}

func loadManagedCondaRuntimeForPlatform(config Config, name string, platform managedRuntimePlatform) (managedCondaRuntime, error) {
	catalogPath := strings.TrimSpace(config.CondaRuntimeCatalog)
	if catalogPath == "" {
		return managedCondaRuntime{}, errors.New("managed runtime catalog is not configured")
	}
	var catalog condaRuntimeCatalog
	if _, err := readBoundedJSON(catalogPath, &catalog); err != nil {
		return managedCondaRuntime{}, fmt.Errorf("read managed runtime catalog: %w", err)
	}
	if (catalog.SchemaVersion != 1 && catalog.SchemaVersion != 2) || catalog.Platform != platform.ID || !validSHA256(catalog.CatalogSHA256) {
		return managedCondaRuntime{}, errors.New("managed runtime catalog contract is invalid")
	}
	if catalog.SchemaVersion == 2 {
		if catalog.Oracle != nil {
			return managedCondaRuntime{}, errors.New("runtime catalog contains unsupported legacy metadata")
		}
		for _, item := range catalog.Runtimes {
			if item.Oracle != nil || item.AlignmentReference != nil {
				return managedCondaRuntime{}, errors.New("runtime entry contains unsupported legacy metadata")
			}
		}
	}
	catalogBody := catalog
	catalogBody.CatalogSHA256 = ""
	catalogDigest, err := canonicalJSONDigest(catalogBody)
	if err != nil || catalogDigest != catalog.CatalogSHA256 {
		return managedCondaRuntime{}, errors.New("managed runtime catalog digest does not match")
	}
	name = canonicalCondaRuntimeName(strings.TrimSpace(name))
	if name == "" {
		return managedCondaRuntime{}, errors.New("managed runtime name is required")
	}
	var entry *condaRuntimeCatalogEntry
	for index := range catalog.Runtimes {
		if canonicalCondaRuntimeName(catalog.Runtimes[index].Name) == name {
			if entry != nil {
				return managedCondaRuntime{}, errors.New("managed runtime catalog contains duplicate entries")
			}
			entry = &catalog.Runtimes[index]
		}
	}
	if entry == nil || entry.Platform != platform.ID || !validSHA256(entry.Generation) || !validSHA256(entry.ExplicitSHA256) {
		return managedCondaRuntime{}, errors.New("managed runtime catalog entry is invalid")
	}
	if !ValidEnvironmentName(entry.Name) {
		return managedCondaRuntime{}, errors.New("managed runtime catalog entry name is invalid")
	}
	root := filepath.Dir(catalogPath)
	manifestPath, err := runtimeAssetPath(root, entry.ManifestPath)
	if err != nil {
		return managedCondaRuntime{}, err
	}
	explicitPath, err := runtimeAssetPath(root, entry.ExplicitPath)
	if err != nil {
		return managedCondaRuntime{}, err
	}
	var manifest condaRuntimeManifest
	if _, err := readBoundedJSON(manifestPath, &manifest); err != nil {
		return managedCondaRuntime{}, fmt.Errorf("read managed runtime manifest: %w", err)
	}
	manifestBody := manifest
	manifestBody.ManifestSHA256 = ""
	manifestDigest, err := canonicalJSONDigest(manifestBody)
	if err != nil || manifestDigest != manifest.ManifestSHA256 || manifestDigest != entry.Generation {
		return managedCondaRuntime{}, errors.New("managed runtime manifest digest does not match")
	}
	if manifest.SchemaVersion != catalog.SchemaVersion || manifest.Name != entry.Name || manifest.Platform != entry.Platform ||
		manifest.PackageCount != len(manifest.Packages) || manifest.PackageCount != entry.PackageCount ||
		manifest.ExplicitSHA256 != entry.ExplicitSHA256 ||
		manifest.LicenseInventorySHA256 != entry.LicenseInventorySHA256 || entry.LicenseTextsIncluded {
		return managedCondaRuntime{}, errors.New("managed runtime manifest does not match its catalog entry")
	}
	if manifest.SchemaVersion == 2 && (manifest.Oracle != nil || manifest.AlignmentReference != nil) {
		return managedCondaRuntime{}, errors.New("runtime manifest contains unsupported legacy metadata")
	}
	if !sameRuntimeRequirements(entry.RequiredPackages, manifest.RequiredPackages) {
		return managedCondaRuntime{}, errors.New("managed runtime requirements do not match the catalog entry")
	}
	explicitDigest, err := fileSHA256(explicitPath)
	if err != nil || explicitDigest != manifest.ExplicitSHA256 {
		return managedCondaRuntime{}, errors.New("managed runtime explicit lock digest does not match")
	}
	if err := validateManagedRuntimeExplicitLock(explicitPath, platform, manifest.Packages); err != nil {
		return managedCondaRuntime{}, err
	}
	licensesPath, err := runtimeAssetPath(root, entry.LicensesPath)
	if err != nil {
		return managedCondaRuntime{}, err
	}
	licenseDigest, err := fileSHA256(licensesPath)
	if err != nil || licenseDigest != manifest.LicenseInventorySHA256 {
		return managedCondaRuntime{}, errors.New("managed runtime license inventory digest does not match")
	}
	packageVersions := map[string]string{}
	for _, item := range manifest.Packages {
		if item.Name == "" || item.Version == "" || item.Build == "" || !validSHA256(item.SHA256) {
			return managedCondaRuntime{}, errors.New("managed runtime package contract is invalid")
		}
		if item.Subdir != "noarch" && item.Subdir != platform.CondaSubdir {
			return managedCondaRuntime{}, errors.New("managed runtime package platform does not match the host")
		}
		if _, exists := packageVersions[item.Name]; exists {
			return managedCondaRuntime{}, errors.New("managed runtime package names are not unique")
		}
		packageVersions[item.Name] = item.Version
	}
	for _, requirement := range manifest.RequiredPackages {
		actual := packageVersions[requirement.Name]
		matches := actual == requirement.Version
		if strings.HasSuffix(requirement.Version, ".*") {
			matches = strings.HasPrefix(actual, strings.TrimSuffix(requirement.Version, "*"))
		}
		if !matches {
			return managedCondaRuntime{}, errors.New("managed runtime required package is unavailable")
		}
	}
	resolvedEntry := entryCopy(*entry)
	resolvedEntry.Name = canonicalCondaRuntimeName(resolvedEntry.Name)
	if !ValidEnvironmentName(resolvedEntry.Name) {
		return managedCondaRuntime{}, errors.New("managed runtime canonical entry name is invalid")
	}
	return managedCondaRuntime{
		entry: resolvedEntry, manifest: manifest, explicitPath: explicitPath,
		manifestDigest: manifestDigest, packageVersions: packageVersions,
	}, nil
}

func loadManagedPythonRuntime(config Config) (managedPythonRuntime, error) {
	runtime, err := loadManagedCondaRuntime(config, managedPythonName(config))
	if err != nil {
		return managedPythonRuntime{}, err
	}
	helperDigest, err := fileSHA256(config.ManifestPath)
	if err != nil {
		return managedPythonRuntime{}, errors.New("managed Python helper manifest is unavailable")
	}
	pythonVersion := runtime.packageVersions["python"]
	rdkitVersion := runtime.packageVersions["rdkit"]
	if pythonVersion == "" || rdkitVersion == "" || runtime.packageVersions["py3dmol"] == "" {
		return managedPythonRuntime{}, errors.New("managed Python runtime required package is unavailable")
	}
	activationHash := sha256.Sum256([]byte(managedPythonActivationContract + "\x00" + runtime.manifestDigest + "\x00" + helperDigest))
	return managedPythonRuntime{
		managedCondaRuntime: runtime, helperDigest: helperDigest,
		activationGeneration: hex.EncodeToString(activationHash[:]),
		pythonVersion:        pythonVersion, rdkitVersion: rdkitVersion,
	}, nil
}

func sameRuntimeRequirements(left, right []condaRuntimeRequirement) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func entryCopy(value condaRuntimeCatalogEntry) condaRuntimeCatalogEntry {
	value.RequiredPackages = append([]condaRuntimeRequirement(nil), value.RequiredPackages...)
	return value
}

func (m *Manager) managedPythonGenerationPath(runtime managedPythonRuntime) string {
	return filepath.Join(m.config.CondaEnvsPath, ".generations", runtime.entry.Name, runtime.activationGeneration)
}

func (m *Manager) managedPythonMarker(runtime managedPythonRuntime) managedRuntimeMarker {
	return managedRuntimeMarker{
		SchemaVersion: managedRuntimeMarkerVersion, Name: runtime.entry.Name, Platform: runtime.entry.Platform,
		Generation: runtime.activationGeneration, ManifestSHA256: runtime.manifestDigest,
		ExplicitSHA256: runtime.manifest.ExplicitSHA256, HelperManifestSHA256: runtime.helperDigest,
		PythonVersion: runtime.pythonVersion, RDKitVersion: runtime.rdkitVersion,
	}
}

func managedPythonPurelibRelative(pythonVersion string) (string, error) {
	return managedRuntimePythonPurelibRelative(pythonVersion)
}

func (m *Manager) managedPythonHelperAssets() ([]string, error) {
	helper := filepath.Clean(strings.TrimSpace(m.config.PythonHelperPath))
	if !filepath.IsAbs(helper) || filepath.Base(helper) != "cheminfo_render_helpers.py" {
		return nil, errors.New("managed Python helper path is invalid")
	}
	root := filepath.Dir(helper)
	result := []string{
		filepath.Join(root, "sitecustomize.py"),
		filepath.Join(root, "cheminfo_render_helpers.py"),
	}
	for _, relative := range pythonRuntimePackageAssets() {
		result = append(result, filepath.Join(root, relative))
	}
	return result, nil
}

func copyManagedPythonHelper(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() <= 0 || info.Size() > maxManagedPythonHelperBytes {
		return errors.New("managed Python helper asset is invalid")
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	return os.WriteFile(destination, raw, 0o444)
}

func (m *Manager) installManagedPythonHelpers(runtime managedPythonRuntime, prefix string) error {
	relative, err := managedPythonPurelibRelative(runtime.pythonVersion)
	if err != nil {
		return err
	}
	sources, err := m.managedPythonHelperAssets()
	if err != nil {
		return err
	}
	root := filepath.Dir(m.config.PythonHelperPath)
	purelib := filepath.Join(prefix, relative)
	for _, source := range sources {
		helperRelative, relativeErr := filepath.Rel(root, source)
		if relativeErr != nil || helperRelative == "." || helperRelative == ".." ||
			strings.HasPrefix(helperRelative, ".."+string(os.PathSeparator)) {
			return errors.New("managed Python helper asset escapes its verified root")
		}
		if err := copyManagedPythonHelper(source, filepath.Join(purelib, helperRelative)); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) verifyManagedPythonHelpers(runtime managedPythonRuntime, prefix string) error {
	relative, err := managedPythonPurelibRelative(runtime.pythonVersion)
	if err != nil {
		return err
	}
	sources, err := m.managedPythonHelperAssets()
	if err != nil {
		return err
	}
	root := filepath.Dir(m.config.PythonHelperPath)
	for _, source := range sources {
		helperRelative, relativeErr := filepath.Rel(root, source)
		if relativeErr != nil || helperRelative == "." || helperRelative == ".." ||
			strings.HasPrefix(helperRelative, ".."+string(os.PathSeparator)) {
			return errors.New("managed Python helper asset escapes its verified root")
		}
		destination := filepath.Join(prefix, relative, helperRelative)
		destinationInfo, statErr := os.Lstat(destination)
		if statErr != nil || !destinationInfo.Mode().IsRegular() || destinationInfo.Mode()&os.ModeSymlink != 0 {
			return errors.New("managed Python installed helper is unavailable")
		}
		sourceDigest, sourceErr := fileSHA256(source)
		destinationDigest, destinationErr := fileSHA256(destination)
		if sourceErr != nil || destinationErr != nil || sourceDigest != destinationDigest {
			return errors.New("managed Python installed helper does not match the verified asset")
		}
	}
	return nil
}

func (m *Manager) verifyManagedPythonGeneration(runtime managedPythonRuntime, prefix string) error {
	resolved, err := resolveManagedRuntimeGeneration(prefix)
	if err != nil {
		return err
	}
	expected, err := filepath.EvalSymlinks(m.managedPythonGenerationPath(runtime))
	if err != nil || resolved != expected {
		return errors.New("managed Python active generation does not match the verified runtime")
	}
	var marker managedRuntimeMarker
	if _, err := readBoundedJSON(filepath.Join(resolved, managedRuntimeMarkerName), &marker); err != nil {
		return errors.New("managed Python generation marker is unavailable")
	}
	if marker != m.managedPythonMarker(runtime) {
		return errors.New("managed Python generation marker does not match the verified runtime")
	}
	if _, err := managedPythonExecutableAtPrefix(resolved); err != nil {
		return errors.New("managed Python executable is unavailable")
	}
	return m.verifyManagedPythonHelpers(runtime, resolved)
}

func writeManagedRuntimeMarker(path string, marker managedRuntimeMarker) error {
	raw, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".synon-runtime-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func (m *Manager) smokeManagedPython(ctx context.Context, runtime managedPythonRuntime, prefix string) error {
	python, err := managedPythonExecutableAtPrefix(prefix)
	if err != nil {
		return err
	}
	prefixJSON, _ := json.Marshal(prefix)
	code := "import json,pathlib,tempfile;import rdkit,py3Dmol,shutil,cheminfo_render_helpers as h;out=tempfile.mkdtemp(prefix='synon-rdkit-smoke-',dir=" +
		string(prefixJSON) + ");r=h.render_molecule_images(['CCO'],['smoke'],out_dir=out);p=pathlib.Path(r['grid']);" +
		"assert p.is_file() and p.stat().st_size>0;shutil.rmtree(out);print(json.dumps({'rdkit':rdkit.__version__,'ok':True},sort_keys=True))"
	command := newWorkerProcessCommand(ctx, python, "-I", "-c", code)
	command.Env = kernelEnvironment(map[string]string{
		"CONDA_PREFIX": prefix, "CONDA_DEFAULT_ENV": runtime.entry.Name,
		"PATH": managedExecutableSearchPath(managedRuntimePath(prefix)), "PYTHONNOUSERSITE": "1",
	})
	stdout := newTailBuffer(maxDiagnosticBytes)
	stderr := newTailBuffer(maxDiagnosticBytes)
	command.Stdout, command.Stderr = stdout, stderr
	if err := runWorkerProcess(command); err != nil {
		return fmt.Errorf("managed Python scientific smoke failed: %w: %s", err, boundedProcessDiagnostics(stdout.String(), stderr.String()))
	}
	var response struct {
		OK    bool   `json:"ok"`
		RDKit string `json:"rdkit"`
	}
	valid := false
	for _, line := range reverseNonEmptyOutputLines(stdout.String()) {
		var candidate struct {
			OK    bool   `json:"ok"`
			RDKit string `json:"rdkit"`
		}
		if err := json.Unmarshal([]byte(line), &candidate); err == nil && candidate.OK && candidate.RDKit == runtime.rdkitVersion {
			response = candidate
			valid = true
			break
		}
	}
	if !valid || !response.OK || response.RDKit != runtime.rdkitVersion {
		return errors.New("managed Python scientific smoke returned an invalid result")
	}
	return nil
}

func reverseNonEmptyOutputLines(value string) []string {
	lines := strings.Split(value, "\n")
	result := make([]string, 0, len(lines))
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func boundedProcessDiagnostics(stdout, stderr string) string {
	stderr = strings.TrimSpace(stderr)
	stdout = strings.TrimSpace(stdout)
	diagnostics := ""
	if stderr == "" {
		diagnostics = stdout
	} else if stdout == "" {
		diagnostics = stderr
	} else {
		diagnostics = stderr + " | stdout: " + stdout
	}
	if len(diagnostics) > maxDiagnosticBytes {
		return diagnostics[len(diagnostics)-maxDiagnosticBytes:]
	}
	return diagnostics
}

func quarantineManagedRuntimeGeneration(path string) (string, error) {
	_, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	quarantine := filepath.Join(filepath.Dir(path), ".staging-invalid-"+uuid.NewString())
	if err := os.Rename(path, quarantine); err != nil {
		return "", err
	}
	return quarantine, nil
}

// prepareManagedRuntimeGeneration validates the deterministic generation while
// holding the caller's install lock. A corrupt generation is moved aside rather
// than reused, so a failed or interrupted install can recover on the next
// attempt without touching a healthy active generation.
func prepareManagedRuntimeGeneration(path string, verify func() error) (bool, string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		quarantine, err := quarantineManagedRuntimeGeneration(path)
		if err != nil {
			return false, "", fmt.Errorf("quarantine invalid managed runtime generation: %w", err)
		}
		return false, quarantine, nil
	}
	if verify == nil {
		return false, "", errors.New("managed runtime generation verifier is unavailable")
	}
	if err := verify(); err == nil {
		return true, "", nil
	} else if quarantine, quarantineErr := quarantineManagedRuntimeGeneration(path); quarantineErr != nil {
		return false, "", fmt.Errorf("quarantine invalid managed runtime generation: %w (verification: %v)", quarantineErr, err)
	} else {
		return false, quarantine, nil
	}
}

func (m *Manager) activateManagedPythonGeneration(runtime managedPythonRuntime) error {
	return activateManagedRuntimeGeneration(
		filepath.Join(m.config.CondaEnvsPath, runtime.entry.Name),
		m.managedPythonGenerationPath(runtime),
	)
}

func (m *Manager) ensureManagedPythonEnvironment(ctx context.Context) error {
	m.environmentMu.Lock()
	defer m.environmentMu.Unlock()
	m.setManagedPythonProvisioningPhase("verifying-assets")
	if err := m.Verify(); err != nil {
		return fmt.Errorf("verify managed Python assets: %w", err)
	}
	m.setManagedPythonProvisioningPhase("loading-runtime")
	runtime, err := loadManagedPythonRuntime(m.config)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(m.config.CondaEnvsPath, ".generations", runtime.entry.Name), 0o700); err != nil {
		return fmt.Errorf("create managed Python generation root: %w", err)
	}
	m.setManagedPythonProvisioningPhase("waiting-for-install-lock")
	lockPath := filepath.Join(m.config.CondaEnvsPath, ".generations", runtime.entry.Name, ".install.lock")
	release, err := lockKernelFile(ctx, lockPath)
	if err != nil {
		return fmt.Errorf("lock managed Python installation: %w", err)
	}
	defer release()
	active := filepath.Join(m.config.CondaEnvsPath, runtime.entry.Name)
	m.setManagedPythonProvisioningPhase("checking-active-generation")
	if err := m.verifyManagedPythonGeneration(runtime, active); err == nil {
		return nil
	}
	generationPath := m.managedPythonGenerationPath(runtime)
	generationReady, quarantinedGeneration, err := prepareManagedRuntimeGeneration(generationPath, func() error {
		return m.verifyManagedPythonGeneration(runtime, generationPath)
	})
	if err != nil {
		return fmt.Errorf("inspect managed Python generation: %w", err)
	}
	if quarantinedGeneration != "" {
		defer func() { _ = os.RemoveAll(quarantinedGeneration) }()
	}
	if !generationReady {
		parent := filepath.Dir(generationPath)
		staging, err := os.MkdirTemp(parent, ".staging-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(staging)
		m.setManagedPythonProvisioningPhase("installing-bundled-generation")
		arguments := []string{"--no-rc", "create", "-y", "-p", staging, "-f", runtime.explicitPath}
		if err := m.runManagedEnvironmentCommand(ctx, arguments...); err != nil {
			return fmt.Errorf("install managed Python generation: %w", err)
		}
		m.setManagedPythonProvisioningPhase("installing-runtime-helpers")
		if err := m.installManagedPythonHelpers(runtime, staging); err != nil {
			return fmt.Errorf("install managed Python helpers: %w", err)
		}
		m.setManagedPythonProvisioningPhase("running-scientific-smoke-test")
		if err := m.smokeManagedPython(ctx, runtime, staging); err != nil {
			return err
		}
		if err := writeManagedRuntimeMarker(filepath.Join(staging, managedRuntimeMarkerName), m.managedPythonMarker(runtime)); err != nil {
			return err
		}
		m.setManagedPythonProvisioningPhase("publishing-generation")
		if err := os.Rename(staging, generationPath); err != nil {
			if verifyErr := m.verifyManagedPythonGeneration(runtime, generationPath); verifyErr != nil {
				return fmt.Errorf("publish managed Python generation: %w (existing generation: %v)", err, verifyErr)
			}
		}
	}
	m.setManagedPythonProvisioningPhase("activating-generation")
	if err := m.activateManagedPythonGeneration(runtime); err != nil {
		return fmt.Errorf("activate managed Python generation: %w", err)
	}
	m.setManagedPythonProvisioningPhase("verifying-generation")
	return m.verifyManagedPythonGeneration(runtime, active)
}

func (m *Manager) managedPythonRuntimeReady() error {
	runtime, err := loadManagedPythonRuntime(m.config)
	if err != nil {
		return err
	}
	return m.verifyManagedPythonGeneration(runtime, filepath.Join(m.config.CondaEnvsPath, runtime.entry.Name))
}

// ManagedPythonActiveGeneration verifies and returns the immutable generation
// bound to the active pointer. This runs at execution admission rather than
// status polling or schema rendering.
func (m *Manager) ManagedPythonActiveGeneration() (string, error) {
	if m == nil {
		return "", errors.New("kernel manager is not configured")
	}
	resolved, runtime, err := m.managedPythonActivePrefix()
	if err != nil {
		return "", err
	}
	if filepath.Base(resolved) != runtime.activationGeneration {
		return "", errors.New("managed Python active generation is invalid")
	}
	return runtime.activationGeneration, nil
}

// ManagedPythonActivePrefix returns the resolved path of the verified,
// content-addressed bundled runtime. Callers use it only as an immutable
// clone source; the bundled generation itself is never modified.
func (m *Manager) ManagedPythonActivePrefix() (string, error) {
	resolved, _, err := m.managedPythonActivePrefix()
	return resolved, err
}

// ManagedPythonExecutable returns the executable inside the verified,
// content-addressed managed Python generation. Callers that launch bundled
// Python tools must use this path instead of resolving a machine-global
// `python3`, so the tool process observes the same dependency contract that
// passed the managed-runtime smoke test.
func (m *Manager) ManagedPythonExecutable() (string, error) {
	prefix, err := m.ManagedPythonActivePrefix()
	if err != nil {
		return "", err
	}
	return managedPythonExecutableAtPrefix(prefix)
}

// ManagedPythonPackages returns the verified package inventory for the
// immutable bundled scientific runtime. Callers use this bounded read model
// for dependency preflight; it does not expose runtime paths or mutation
// authority.
func (m *Manager) ManagedPythonPackages() ([]string, error) {
	if m == nil {
		return nil, errors.New("kernel manager is not configured")
	}
	runtime, err := loadManagedPythonRuntime(m.config)
	if err != nil {
		return nil, err
	}
	packages := make([]string, 0, len(runtime.manifest.Packages))
	for _, item := range runtime.manifest.Packages {
		packages = append(packages, item.Name)
	}
	sort.Strings(packages)
	return packages, nil
}

func (m *Manager) managedPythonActivePrefix() (string, managedPythonRuntime, error) {
	if m == nil {
		return "", managedPythonRuntime{}, errors.New("kernel manager is not configured")
	}
	runtime, err := loadManagedPythonRuntime(m.config)
	if err != nil {
		return "", managedPythonRuntime{}, err
	}
	active := filepath.Join(m.config.CondaEnvsPath, runtime.entry.Name)
	if err := m.verifyManagedPythonGeneration(runtime, active); err != nil {
		return "", managedPythonRuntime{}, err
	}
	resolved, err := resolveManagedRuntimeGeneration(active)
	if err != nil || filepath.Base(resolved) != runtime.activationGeneration {
		return "", managedPythonRuntime{}, errors.New("managed Python active generation is invalid")
	}
	return resolved, runtime, nil
}

func (m *Manager) ScientificArtifactValidator() (python, validator, generation, rdkitVersion string, err error) {
	if m == nil {
		return "", "", "", "", errors.New("kernel manager is not configured")
	}
	runtime, err := loadManagedPythonRuntime(m.config)
	if err != nil {
		return "", "", "", "", err
	}
	active := filepath.Join(m.config.CondaEnvsPath, runtime.entry.Name)
	if err := m.verifyManagedPythonGeneration(runtime, active); err != nil {
		return "", "", "", "", err
	}
	prefix, err := resolveManagedRuntimeGeneration(active)
	if err != nil {
		return "", "", "", "", err
	}
	validator = strings.TrimSpace(m.config.SDFValidatorPath)
	if validator == "" || !filepath.IsAbs(validator) {
		return "", "", "", "", errors.New("scientific artifact validator is not configured")
	}
	if info, err := os.Stat(validator); err != nil || !info.Mode().IsRegular() {
		return "", "", "", "", errors.New("scientific artifact validator is unavailable")
	}
	python, err = managedPythonExecutableAtPrefix(prefix)
	if err != nil {
		return "", "", "", "", err
	}
	return python, validator, runtime.activationGeneration, runtime.rdkitVersion, nil
}
