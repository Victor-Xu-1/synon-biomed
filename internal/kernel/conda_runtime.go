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
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	defaultManagedPythonEnvironment = "synon-biomed-python"
	maxCondaRuntimeMetadataBytes    = 16 * 1024 * 1024
	maxManagedPythonHelperBytes     = 1 * 1024 * 1024
	managedRuntimeMarkerName        = ".synon-runtime.json"
	managedRuntimeMarkerVersion     = 2
	managedPythonActivationContract = "synon-managed-python-runtime-v2"
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

type managedPythonRuntime struct {
	entry                condaRuntimeCatalogEntry
	manifest             condaRuntimeManifest
	manifestPath         string
	explicitPath         string
	manifestDigest       string
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
	PythonVersion        string `json:"pythonVersion"`
	RDKitVersion         string `json:"rdkitVersion"`
}

func currentCondaPlatform() string {
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		return "linux-x86_64"
	}
	return runtime.GOOS + "-" + runtime.GOARCH
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
	m.managedPythonMu.Lock()
	defer m.managedPythonMu.Unlock()
	if m.managedPythonProvision == nil {
		if m.ManagedPythonProvisioningEnabled() {
			return ManagedPythonProvisioningDetails{Status: "pending", Phase: "waiting-to-start"}
		}
		return ManagedPythonProvisioningDetails{Status: "failed", Phase: "unavailable"}
	}
	details := ManagedPythonProvisioningDetails{
		Phase:          m.managedPythonProvision.phase,
		StartedAt:      m.managedPythonProvision.startedAt,
		LastProgressAt: m.managedPythonProvision.lastProgressAt,
	}
	select {
	case <-m.managedPythonProvision.done:
		if m.managedPythonProvision.err != nil {
			details.Status = "failed"
			details.Error = boundedManagedPythonProvisioningError(m.managedPythonProvision.err)
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
	m.managedPythonMu.Lock()
	defer m.managedPythonMu.Unlock()
	if m.managedPythonProvision == nil {
		return
	}
	m.managedPythonProvision.phase = phase
	m.managedPythonProvision.lastProgressAt = time.Now().UTC()
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
	if m.managedPythonRuntimeReady() == nil {
		return nil
	}
	m.managedPythonMu.Lock()
	provision := m.managedPythonProvision
	if provision != nil && retry {
		select {
		case <-provision.done:
			if provision.err != nil {
				provision = nil
				m.managedPythonProvision = nil
			}
		default:
		}
	}
	if provision == nil {
		now := time.Now().UTC()
		provision = &managedPythonProvision{
			done: make(chan struct{}), phase: "queued", startedAt: now, lastProgressAt: now,
		}
		m.managedPythonProvision = provision
		go func(active *managedPythonProvision) {
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
			m.managedPythonMu.Lock()
			active.err = err
			close(active.done)
			m.managedPythonMu.Unlock()
			m.notifyRuntimeChange()
		}(provision)
	}
	m.managedPythonMu.Unlock()
	if !wait {
		return nil
	}
	select {
	case <-waitCtx.Done():
		return waitCtx.Err()
	case <-provision.done:
		m.managedPythonMu.Lock()
		err := provision.err
		m.managedPythonMu.Unlock()
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

func loadManagedPythonRuntime(config Config) (managedPythonRuntime, error) {
	catalogPath := strings.TrimSpace(config.CondaRuntimeCatalog)
	if catalogPath == "" {
		return managedPythonRuntime{}, errors.New("managed Python runtime catalog is not configured")
	}
	var catalog condaRuntimeCatalog
	if _, err := readBoundedJSON(catalogPath, &catalog); err != nil {
		return managedPythonRuntime{}, fmt.Errorf("read managed Python runtime catalog: %w", err)
	}
	if (catalog.SchemaVersion != 1 && catalog.SchemaVersion != 2) || catalog.Platform != currentCondaPlatform() || !validSHA256(catalog.CatalogSHA256) {
		return managedPythonRuntime{}, errors.New("managed Python runtime catalog contract is invalid")
	}
	if catalog.SchemaVersion == 2 {
		if catalog.Oracle != nil {
			return managedPythonRuntime{}, errors.New("runtime catalog contains unsupported legacy metadata")
		}
		for _, item := range catalog.Runtimes {
			if item.Oracle != nil || item.AlignmentReference != nil {
				return managedPythonRuntime{}, errors.New("runtime entry contains unsupported legacy metadata")
			}
		}
	}
	catalogBody := catalog
	catalogBody.CatalogSHA256 = ""
	catalogDigest, err := canonicalJSONDigest(catalogBody)
	if err != nil || catalogDigest != catalog.CatalogSHA256 {
		return managedPythonRuntime{}, errors.New("managed Python runtime catalog digest does not match")
	}
	name := managedPythonName(config)
	var entry *condaRuntimeCatalogEntry
	for index := range catalog.Runtimes {
		if canonicalCondaRuntimeName(catalog.Runtimes[index].Name) == name {
			if entry != nil {
				return managedPythonRuntime{}, errors.New("managed Python runtime catalog contains duplicate entries")
			}
			entry = &catalog.Runtimes[index]
		}
	}
	if entry == nil || entry.Platform != currentCondaPlatform() || !validSHA256(entry.Generation) || !validSHA256(entry.ExplicitSHA256) {
		return managedPythonRuntime{}, errors.New("managed Python runtime catalog entry is invalid")
	}
	root := filepath.Dir(catalogPath)
	manifestPath, err := runtimeAssetPath(root, entry.ManifestPath)
	if err != nil {
		return managedPythonRuntime{}, err
	}
	explicitPath, err := runtimeAssetPath(root, entry.ExplicitPath)
	if err != nil {
		return managedPythonRuntime{}, err
	}
	var manifest condaRuntimeManifest
	if _, err := readBoundedJSON(manifestPath, &manifest); err != nil {
		return managedPythonRuntime{}, fmt.Errorf("read managed Python runtime manifest: %w", err)
	}
	manifestBody := manifest
	manifestBody.ManifestSHA256 = ""
	manifestDigest, err := canonicalJSONDigest(manifestBody)
	if err != nil || manifestDigest != manifest.ManifestSHA256 || manifestDigest != entry.Generation {
		return managedPythonRuntime{}, errors.New("managed Python runtime manifest digest does not match")
	}
	if manifest.SchemaVersion != catalog.SchemaVersion || manifest.Name != entry.Name || manifest.Platform != entry.Platform ||
		manifest.PackageCount != len(manifest.Packages) || manifest.ExplicitSHA256 != entry.ExplicitSHA256 {
		return managedPythonRuntime{}, errors.New("managed Python runtime manifest does not match its catalog entry")
	}
	if manifest.SchemaVersion == 2 && (manifest.Oracle != nil || manifest.AlignmentReference != nil) {
		return managedPythonRuntime{}, errors.New("runtime manifest contains unsupported legacy metadata")
	}
	if !sameRuntimeRequirements(entry.RequiredPackages, manifest.RequiredPackages) {
		return managedPythonRuntime{}, errors.New("managed Python runtime requirements do not match the catalog entry")
	}
	explicitDigest, err := fileSHA256(explicitPath)
	if err != nil || explicitDigest != manifest.ExplicitSHA256 {
		return managedPythonRuntime{}, errors.New("managed Python explicit lock digest does not match")
	}
	packageVersions := map[string]string{}
	for _, item := range manifest.Packages {
		if item.Name == "" || item.Version == "" || item.Build == "" || !validSHA256(item.SHA256) {
			return managedPythonRuntime{}, errors.New("managed Python runtime package contract is invalid")
		}
		if _, exists := packageVersions[item.Name]; exists {
			return managedPythonRuntime{}, errors.New("managed Python runtime package names are not unique")
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
			return managedPythonRuntime{}, errors.New("managed Python runtime required package is unavailable")
		}
	}
	helperDigest, err := fileSHA256(config.ManifestPath)
	if err != nil {
		return managedPythonRuntime{}, errors.New("managed Python helper manifest is unavailable")
	}
	activationHash := sha256.Sum256([]byte(managedPythonActivationContract + "\x00" + manifestDigest + "\x00" + helperDigest))
	resolvedEntry := entryCopy(*entry)
	resolvedEntry.Name = canonicalCondaRuntimeName(resolvedEntry.Name)
	return managedPythonRuntime{
		entry: resolvedEntry, manifest: manifest, manifestPath: manifestPath, explicitPath: explicitPath,
		manifestDigest: manifestDigest, helperDigest: helperDigest,
		activationGeneration: hex.EncodeToString(activationHash[:]),
		pythonVersion:        packageVersions["python"], rdkitVersion: packageVersions["rdkit"],
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
	parts := strings.Split(strings.TrimSpace(pythonVersion), ".")
	if len(parts) < 2 {
		return "", errors.New("managed Python version is invalid")
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	if majorErr != nil || minorErr != nil || major != 3 || minor < 8 || minor > 99 {
		return "", errors.New("managed Python version is unsupported")
	}
	return filepath.Join("lib", fmt.Sprintf("python%d.%d", major, minor), "site-packages"), nil
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
	resolved, err := filepath.EvalSymlinks(prefix)
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
	python := filepath.Join(resolved, "bin", executableName("python"))
	if info, err := os.Stat(python); err != nil || !executableRegularFile(info) {
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
	python := filepath.Join(prefix, "bin", executableName("python"))
	prefixJSON, _ := json.Marshal(prefix)
	code := "import json,pathlib,tempfile;import rdkit,py3Dmol,shutil,cheminfo_render_helpers as h;out=tempfile.mkdtemp(prefix='synon-rdkit-smoke-',dir=" +
		string(prefixJSON) + ");r=h.render_molecule_images(['CCO'],['smoke'],out_dir=out);p=pathlib.Path(r['grid']);" +
		"assert p.is_file() and p.stat().st_size>0;shutil.rmtree(out);print(json.dumps({'rdkit':rdkit.__version__,'ok':True},sort_keys=True))"
	command := newWorkerProcessCommand(ctx, python, "-I", "-c", code)
	command.Env = kernelEnvironment(map[string]string{
		"CONDA_PREFIX": prefix, "CONDA_DEFAULT_ENV": runtime.entry.Name,
		"PATH": filepath.Join(prefix, "bin"), "PYTHONNOUSERSITE": "1",
	})
	output := newTailBuffer(maxDiagnosticBytes)
	command.Stdout, command.Stderr = output, output
	if err := runWorkerProcess(command); err != nil {
		return fmt.Errorf("managed Python scientific smoke failed: %w: %s", err, strings.TrimSpace(output.String()))
	}
	var response struct {
		OK    bool   `json:"ok"`
		RDKit string `json:"rdkit"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output.String())), &response); err != nil || !response.OK || response.RDKit != runtime.rdkitVersion {
		return errors.New("managed Python scientific smoke returned an invalid result")
	}
	return nil
}

func (m *Manager) activateManagedPythonGeneration(runtime managedPythonRuntime) error {
	active := filepath.Join(m.config.CondaEnvsPath, runtime.entry.Name)
	temporary := active + ".tmp-" + uuid.NewString()
	defer os.Remove(temporary)
	if info, err := os.Lstat(active); err == nil && info.Mode()&os.ModeSymlink == 0 {
		return errors.New("managed Python active path is not an atomic generation pointer")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Symlink(m.managedPythonGenerationPath(runtime), temporary); err != nil {
		return err
	}
	return os.Rename(temporary, active)
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
	if _, err := os.Stat(generationPath); errors.Is(err, os.ErrNotExist) {
		parent := filepath.Dir(generationPath)
		staging, err := os.MkdirTemp(parent, ".staging-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(staging)
		m.setManagedPythonProvisioningPhase("installing-bundled-generation")
		arguments := []string{"--no-rc", "create", "-y", "-p", staging, "-f", runtime.explicitPath}
		command := newWorkerProcessCommand(ctx, m.config.Micromamba, arguments...)
		command.Env = m.managedEnvironmentInstallerEnv()
		output := newTailBuffer(maxDiagnosticBytes)
		command.Stdout, command.Stderr = output, output
		if err := runWorkerProcess(command); err != nil {
			return fmt.Errorf("install managed Python generation: %w: %s", err, strings.TrimSpace(output.String()))
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
			if _, statErr := os.Stat(generationPath); statErr != nil {
				return fmt.Errorf("publish managed Python generation: %w", err)
			}
		}
	} else if err != nil {
		return fmt.Errorf("inspect managed Python generation: %w", err)
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
	resolved, err := filepath.EvalSymlinks(active)
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
	prefix, err := filepath.EvalSymlinks(active)
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
	return filepath.Join(prefix, "bin", executableName("python")), validator, runtime.activationGeneration, runtime.rdkitVersion, nil
}
