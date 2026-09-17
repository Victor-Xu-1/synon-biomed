package releasepkg

import (
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	SBOMFile               = "SBOM.spdx.json"
	ThirdPartyLicensesFile = "THIRD_PARTY_LICENSES.json"
	ProvenanceFile         = "PROVENANCE.intoto.json"
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type SupplyChainOptions struct {
	GeneratedAt      time.Time
	GOOS             string
	GOARCH           string
	BinaryPaths      []string
	ModuleCache      string
	GoRoot           string
	LicenseOverrides string
	SourceURI        string
	SourceRevision   string
	SourceSHA256     string
	SourceDirty      bool
}

type SupplyChainReport struct {
	Valid          bool `json:"valid"`
	Binaries       int  `json:"binaries"`
	Modules        int  `json:"modules"`
	LicenseFiles   int  `json:"licenseFiles"`
	MissingLicense int  `json:"missingLicense"`
}

type spdxDocument struct {
	SPDXVersion       string             `json:"spdxVersion"`
	DataLicense       string             `json:"dataLicense"`
	SPDXID            string             `json:"SPDXID"`
	Name              string             `json:"name"`
	DocumentNamespace string             `json:"documentNamespace"`
	CreationInfo      spdxCreationInfo   `json:"creationInfo"`
	Packages          []spdxPackage      `json:"packages"`
	Relationships     []spdxRelationship `json:"relationships"`
}

type spdxCreationInfo struct {
	Created  string   `json:"created"`
	Creators []string `json:"creators"`
}

type spdxPackage struct {
	Name             string            `json:"name"`
	SPDXID           string            `json:"SPDXID"`
	VersionInfo      string            `json:"versionInfo,omitempty"`
	DownloadLocation string            `json:"downloadLocation"`
	FilesAnalyzed    bool              `json:"filesAnalyzed"`
	LicenseConcluded string            `json:"licenseConcluded"`
	LicenseDeclared  string            `json:"licenseDeclared"`
	CopyrightText    string            `json:"copyrightText"`
	ExternalRefs     []spdxExternalRef `json:"externalRefs,omitempty"`
}

type spdxExternalRef struct {
	ReferenceCategory string `json:"referenceCategory"`
	ReferenceType     string `json:"referenceType"`
	ReferenceLocator  string `json:"referenceLocator"`
}

type spdxRelationship struct {
	SPDXElementID      string `json:"spdxElementId"`
	RelationshipType   string `json:"relationshipType"`
	RelatedSPDXElement string `json:"relatedSpdxElement"`
}

type licenseReport struct {
	SchemaVersion int                   `json:"schemaVersion"`
	GeneratedAt   string                `json:"generatedAt"`
	Modules       []moduleLicenseRecord `json:"modules"`
}

type moduleLicenseRecord struct {
	Path         string        `json:"path"`
	Version      string        `json:"version"`
	GoSum        string        `json:"goSum,omitempty"`
	Status       string        `json:"status"`
	LicenseFiles []licenseFile `json:"licenseFiles"`
}

type licenseFile struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Content string `json:"content"`
}

type provenanceStatement struct {
	Type          string              `json:"_type"`
	Subject       []provenanceSubject `json:"subject"`
	PredicateType string              `json:"predicateType"`
	Predicate     provenancePredicate `json:"predicate"`
}

type provenanceSubject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

type provenancePredicate struct {
	BuildDefinition provenanceBuildDefinition `json:"buildDefinition"`
	RunDetails      provenanceRunDetails      `json:"runDetails"`
}

type provenanceBuildDefinition struct {
	BuildType            string                 `json:"buildType"`
	ExternalParameters   map[string]any         `json:"externalParameters"`
	InternalParameters   map[string]any         `json:"internalParameters"`
	ResolvedDependencies []provenanceDependency `json:"resolvedDependencies"`
}

type provenanceDependency struct {
	URI    string            `json:"uri"`
	Digest map[string]string `json:"digest,omitempty"`
}

type provenanceRunDetails struct {
	Builder  provenanceBuilder  `json:"builder"`
	Metadata provenanceMetadata `json:"metadata"`
}

type provenanceBuilder struct {
	ID string `json:"id"`
}

type provenanceMetadata struct {
	BuildStartedOn  string `json:"buildStartedOn"`
	BuildFinishedOn string `json:"buildFinishedOn"`
}

type buildModule struct {
	Path    string
	Version string
	Sum     string
}

func GenerateSupplyChain(root string, options SupplyChainOptions) (SupplyChainReport, error) {
	root, err := validatedRoot(root)
	if err != nil {
		return SupplyChainReport{}, err
	}
	options, err = validateSupplyChainOptions(options)
	if err != nil {
		return SupplyChainReport{}, err
	}
	subjects, modules, goVersion, err := inspectReleaseBinaries(root, options.BinaryPaths)
	if err != nil {
		return SupplyChainReport{}, err
	}
	licenses, licenseFiles, missing, err := collectModuleLicenses(modules, goVersion, options.ModuleCache, options.GoRoot, options.LicenseOverrides)
	if err != nil {
		return SupplyChainReport{}, err
	}
	if missing != 0 {
		return SupplyChainReport{}, fmt.Errorf("%d compiled modules have no auditable license file", missing)
	}
	created := options.GeneratedAt.UTC().Format(time.RFC3339)
	sbom := makeSPDXDocument(modules, goVersion, subjects, created)
	licenseDocument := licenseReport{SchemaVersion: 1, GeneratedAt: created, Modules: licenses}
	provenance := makeProvenance(options, modules, subjects, created)
	for path, document := range map[string]any{
		SBOMFile: sbom, ThirdPartyLicensesFile: licenseDocument, ProvenanceFile: provenance,
	} {
		if err := writeJSONArtifact(root, path, document); err != nil {
			return SupplyChainReport{}, err
		}
	}
	report, err := VerifySupplyChain(root)
	if err != nil {
		return SupplyChainReport{}, err
	}
	report.LicenseFiles = licenseFiles
	return report, nil
}

func VerifySupplyChain(root string) (SupplyChainReport, error) {
	root, err := validatedRoot(root)
	if err != nil {
		return SupplyChainReport{}, err
	}
	var sbom spdxDocument
	if err := readStrictJSON(filepath.Join(root, SBOMFile), &sbom); err != nil {
		return SupplyChainReport{}, fmt.Errorf("verify SBOM: %w", err)
	}
	if sbom.SPDXVersion != "SPDX-2.3" || sbom.DataLicense != "CC0-1.0" || sbom.SPDXID != "SPDXRef-DOCUMENT" {
		return SupplyChainReport{}, errors.New("verify SBOM: unsupported document identity")
	}
	var licenses licenseReport
	if err := readStrictJSON(filepath.Join(root, ThirdPartyLicensesFile), &licenses); err != nil {
		return SupplyChainReport{}, fmt.Errorf("verify licenses: %w", err)
	}
	licenseIndex := make(map[string]moduleLicenseRecord, len(licenses.Modules))
	licenseFiles := 0
	for _, module := range licenses.Modules {
		key := module.Path + "@" + module.Version
		if _, duplicate := licenseIndex[key]; duplicate {
			return SupplyChainReport{}, fmt.Errorf("verify licenses: duplicate module %s", key)
		}
		if module.Status != "present" || len(module.LicenseFiles) == 0 {
			return SupplyChainReport{}, fmt.Errorf("verify licenses: module %s has no license evidence", key)
		}
		for _, file := range module.LicenseFiles {
			digest := sha256.Sum256([]byte(file.Content))
			if file.Path == "" || hex.EncodeToString(digest[:]) != file.SHA256 {
				return SupplyChainReport{}, fmt.Errorf("verify licenses: invalid content digest for %s/%s", key, file.Path)
			}
			licenseFiles++
		}
		licenseIndex[key] = module
	}
	var provenance provenanceStatement
	if err := readStrictJSON(filepath.Join(root, ProvenanceFile), &provenance); err != nil {
		return SupplyChainReport{}, fmt.Errorf("verify provenance: %w", err)
	}
	if provenance.Type != "https://in-toto.io/Statement/v1" || provenance.PredicateType != "https://slsa.dev/provenance/v1" {
		return SupplyChainReport{}, errors.New("verify provenance: unsupported statement identity")
	}
	if len(provenance.Subject) == 0 {
		return SupplyChainReport{}, errors.New("verify provenance: no binary subjects")
	}
	seenSubjects := map[string]struct{}{}
	for _, subject := range provenance.Subject {
		if _, duplicate := seenSubjects[subject.Name]; duplicate {
			return SupplyChainReport{}, fmt.Errorf("verify provenance: duplicate subject %s", subject.Name)
		}
		seenSubjects[subject.Name] = struct{}{}
		path, err := resolvePackageFile(root, subject.Name)
		if err != nil {
			return SupplyChainReport{}, fmt.Errorf("verify provenance subject: %w", err)
		}
		digest, err := hashFile(path)
		if err != nil {
			return SupplyChainReport{}, err
		}
		if subject.Digest["sha256"] != digest {
			return SupplyChainReport{}, fmt.Errorf("verify provenance: subject digest mismatch for %s", subject.Name)
		}
	}
	moduleCount := 0
	seenPackages := map[string]struct{}{}
	rootPackages := 0
	for _, pkg := range sbom.Packages {
		if pkg.Name == releaseName {
			rootPackages++
			continue
		}
		key := pkg.Name + "@" + pkg.VersionInfo
		if _, duplicate := seenPackages[key]; duplicate {
			return SupplyChainReport{}, fmt.Errorf("verify SBOM: duplicate package %s", key)
		}
		seenPackages[key] = struct{}{}
		if _, exists := licenseIndex[key]; !exists {
			return SupplyChainReport{}, fmt.Errorf("verify SBOM: package %s lacks license evidence", key)
		}
		moduleCount++
	}
	if rootPackages != 1 {
		return SupplyChainReport{}, fmt.Errorf("verify SBOM: root package count=%d, want 1", rootPackages)
	}
	if moduleCount != len(licenseIndex) {
		return SupplyChainReport{}, errors.New("verify supply chain: SBOM and license module sets differ")
	}
	if provenance.Predicate.BuildDefinition.BuildType == "" || provenance.Predicate.RunDetails.Builder.ID == "" {
		return SupplyChainReport{}, errors.New("verify provenance: build type and builder identity are required")
	}
	if _, ok := provenance.Predicate.BuildDefinition.InternalParameters["sourceDirty"].(bool); !ok {
		return SupplyChainReport{}, errors.New("verify provenance: sourceDirty must be boolean")
	}
	sourceMaterials := 0
	for _, dependency := range provenance.Predicate.BuildDefinition.ResolvedDependencies {
		if digest := dependency.Digest["sha256"]; digest != "" {
			if dependency.URI == "" || !sha256Pattern.MatchString(digest) {
				return SupplyChainReport{}, errors.New("verify provenance: invalid source material")
			}
			sourceMaterials++
		}
	}
	if sourceMaterials != 1 {
		return SupplyChainReport{}, fmt.Errorf("verify provenance: source material count=%d, want 1", sourceMaterials)
	}
	return SupplyChainReport{Valid: true, Binaries: len(provenance.Subject), Modules: moduleCount, LicenseFiles: licenseFiles}, nil
}

func VerifySupplyChainForRelease(root string) (SupplyChainReport, error) {
	report, err := VerifySupplyChain(root)
	if err != nil {
		return SupplyChainReport{}, err
	}
	root, err = validatedRoot(root)
	if err != nil {
		return SupplyChainReport{}, err
	}
	var provenance provenanceStatement
	if err := readStrictJSON(filepath.Join(root, ProvenanceFile), &provenance); err != nil {
		return SupplyChainReport{}, fmt.Errorf("verify release provenance: %w", err)
	}
	dirty, ok := provenance.Predicate.BuildDefinition.InternalParameters["sourceDirty"].(bool)
	if !ok {
		return SupplyChainReport{}, errors.New("release provenance sourceDirty must be boolean")
	}
	if dirty {
		return SupplyChainReport{}, errors.New("formal release requires a clean source revision")
	}
	return report, nil
}

func validateSupplyChainOptions(options SupplyChainOptions) (SupplyChainOptions, error) {
	if options.GeneratedAt.IsZero() {
		return options, errors.New("supply chain generated time is required")
	}
	if len(options.BinaryPaths) == 0 {
		return options, errors.New("at least one release binary is required")
	}
	if strings.TrimSpace(options.ModuleCache) == "" || strings.TrimSpace(options.GoRoot) == "" {
		return options, errors.New("module cache and Go root are required for license evidence")
	}
	if strings.TrimSpace(options.LicenseOverrides) != "" {
		overrides, err := filepath.Abs(options.LicenseOverrides)
		if err != nil {
			return options, fmt.Errorf("resolve license overrides: %w", err)
		}
		info, err := os.Lstat(overrides)
		if err != nil {
			return options, fmt.Errorf("inspect license overrides: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return options, errors.New("license overrides must be a non-symlink directory")
		}
		options.LicenseOverrides = overrides
	}
	if strings.TrimSpace(options.SourceURI) == "" || strings.TrimSpace(options.SourceRevision) == "" || !sha256Pattern.MatchString(options.SourceSHA256) {
		return options, errors.New("source URI, revision, and SHA-256 are required for provenance")
	}
	options.GOOS = strings.TrimSpace(options.GOOS)
	options.GOARCH = strings.TrimSpace(options.GOARCH)
	return options, nil
}

func inspectReleaseBinaries(root string, binaryPaths []string) ([]provenanceSubject, []buildModule, string, error) {
	subjects := make([]provenanceSubject, 0, len(binaryPaths))
	moduleIndex := map[string]buildModule{}
	goVersion := ""
	seenBinaries := map[string]struct{}{}
	for _, relative := range binaryPaths {
		path, err := resolvePackageFile(root, relative)
		if err != nil {
			return nil, nil, "", err
		}
		relative = filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative)))
		if _, duplicate := seenBinaries[relative]; duplicate {
			return nil, nil, "", fmt.Errorf("duplicate release binary %s", relative)
		}
		seenBinaries[relative] = struct{}{}
		info, err := buildinfo.ReadFile(path)
		if err != nil {
			return nil, nil, "", fmt.Errorf("read Go build info from %s: %w", relative, err)
		}
		if goVersion == "" {
			goVersion = info.GoVersion
		} else if goVersion != info.GoVersion {
			return nil, nil, "", errors.New("release binaries use different Go versions")
		}
		for _, dependency := range info.Deps {
			module := effectiveBuildModule(dependency)
			if module.Path == "" || module.Version == "" {
				return nil, nil, "", fmt.Errorf("binary %s contains an unversioned module dependency", relative)
			}
			key := module.Path + "@" + module.Version
			if existing, exists := moduleIndex[key]; exists && existing.Sum != module.Sum {
				return nil, nil, "", fmt.Errorf("module sum mismatch for %s", key)
			}
			moduleIndex[key] = module
		}
		digest, err := hashFile(path)
		if err != nil {
			return nil, nil, "", err
		}
		subjects = append(subjects, provenanceSubject{Name: relative, Digest: map[string]string{"sha256": digest}})
	}
	sort.Slice(subjects, func(i, j int) bool { return subjects[i].Name < subjects[j].Name })
	modules := make([]buildModule, 0, len(moduleIndex)+1)
	modules = append(modules, buildModule{Path: "go.dev/stdlib", Version: goVersion})
	for _, module := range moduleIndex {
		modules = append(modules, module)
	}
	sort.Slice(modules, func(i, j int) bool {
		if modules[i].Path == modules[j].Path {
			return modules[i].Version < modules[j].Version
		}
		return modules[i].Path < modules[j].Path
	})
	return subjects, modules, goVersion, nil
}

func effectiveBuildModule(module *debug.Module) buildModule {
	if module.Replace != nil {
		return buildModule{Path: module.Replace.Path, Version: module.Replace.Version, Sum: module.Replace.Sum}
	}
	return buildModule{Path: module.Path, Version: module.Version, Sum: module.Sum}
}

func collectModuleLicenses(modules []buildModule, goVersion, moduleCache, goRoot, overridesRoot string) ([]moduleLicenseRecord, int, int, error) {
	records := make([]moduleLicenseRecord, 0, len(modules))
	totalFiles := 0
	missing := 0
	for _, module := range modules {
		directory := ""
		if module.Path == "go.dev/stdlib" {
			directory = goRoot
		} else {
			directory = filepath.Join(moduleCache, escapeModuleCache(module.Path)+"@"+escapeModuleCache(module.Version))
		}
		files, err := readLicenseFiles(directory)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("collect license for %s@%s: %w", module.Path, module.Version, err)
		}
		if len(files) == 0 && strings.TrimSpace(overridesRoot) != "" && module.Path != "go.dev/stdlib" {
			overrideDirectory := filepath.Join(overridesRoot, escapeModuleCache(module.Path)+"@"+escapeModuleCache(module.Version))
			overrides, overrideErr := readLicenseFiles(overrideDirectory)
			if overrideErr != nil && !errors.Is(overrideErr, os.ErrNotExist) {
				return nil, 0, 0, fmt.Errorf("collect license override for %s@%s: %w", module.Path, module.Version, overrideErr)
			}
			for index := range overrides {
				overrides[index].Path = "curated/" + overrides[index].Path
			}
			files = overrides
		}
		status := "present"
		if len(files) == 0 {
			status = "missing"
			missing++
		}
		totalFiles += len(files)
		records = append(records, moduleLicenseRecord{
			Path: module.Path, Version: module.Version, GoSum: module.Sum,
			Status: status, LicenseFiles: files,
		})
	}
	return records, totalFiles, missing, nil
}

func readLicenseFiles(directory string) ([]licenseFile, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	files := []licenseFile{}
	for _, entry := range entries {
		upper := strings.ToUpper(entry.Name())
		if entry.IsDir() || !(strings.HasPrefix(upper, "LICENSE") || strings.HasPrefix(upper, "COPYING") || strings.HasPrefix(upper, "NOTICE")) {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return nil, fmt.Errorf("license candidate %s is not a bounded regular file", entry.Name())
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(content)
		files = append(files, licenseFile{Path: entry.Name(), SHA256: hex.EncodeToString(digest[:]), Content: string(content)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func makeSPDXDocument(modules []buildModule, goVersion string, subjects []provenanceSubject, created string) spdxDocument {
	rootID := "SPDXRef-Package-synon-go"
	packages := []spdxPackage{{
		Name: releaseName, SPDXID: rootID, VersionInfo: releaseVersion,
		DownloadLocation: "NOASSERTION", FilesAnalyzed: false,
		LicenseConcluded: "NOASSERTION", LicenseDeclared: "NOASSERTION", CopyrightText: "NOASSERTION",
	}}
	relationships := []spdxRelationship{{SPDXElementID: "SPDXRef-DOCUMENT", RelationshipType: "DESCRIBES", RelatedSPDXElement: rootID}}
	for _, module := range modules {
		id := spdxModuleID(module.Path, module.Version)
		references := []spdxExternalRef{{
			ReferenceCategory: "PACKAGE-MANAGER", ReferenceType: "purl",
			ReferenceLocator: "pkg:golang/" + module.Path + "@" + url.QueryEscape(module.Version),
		}}
		if module.Sum != "" {
			references = append(references, spdxExternalRef{ReferenceCategory: "OTHER", ReferenceType: "go-module-sum", ReferenceLocator: module.Sum})
		}
		packages = append(packages, spdxPackage{
			Name: module.Path, SPDXID: id, VersionInfo: module.Version,
			DownloadLocation: "NOASSERTION", FilesAnalyzed: false,
			LicenseConcluded: "NOASSERTION", LicenseDeclared: "NOASSERTION", CopyrightText: "NOASSERTION",
			ExternalRefs: references,
		})
		relationships = append(relationships, spdxRelationship{SPDXElementID: rootID, RelationshipType: "DEPENDS_ON", RelatedSPDXElement: id})
	}
	namespaceDigest := sha256.Sum256([]byte(strings.Join(subjectDigests(subjects), ",") + "|" + goVersion))
	return spdxDocument{
		SPDXVersion: "SPDX-2.3", DataLicense: "CC0-1.0", SPDXID: "SPDXRef-DOCUMENT",
		Name:              releaseName + "-" + releaseVersion,
		DocumentNamespace: "https://synon.invalid/spdx/" + releaseName + "/" + releaseVersion + "/" + hex.EncodeToString(namespaceDigest[:]),
		CreationInfo:      spdxCreationInfo{Created: created, Creators: []string{"Tool: " + releaseName + "-releasepkg-" + releaseVersion}},
		Packages:          packages, Relationships: relationships,
	}
}

func makeProvenance(options SupplyChainOptions, modules []buildModule, subjects []provenanceSubject, created string) provenanceStatement {
	dependencies := []provenanceDependency{{
		URI:    options.SourceURI + "#" + options.SourceRevision,
		Digest: map[string]string{"sha256": options.SourceSHA256},
	}}
	for _, module := range modules {
		digest := map[string]string{}
		if module.Sum != "" {
			digest["goSum"] = module.Sum
		}
		dependencies = append(dependencies, provenanceDependency{
			URI: "pkg:golang/" + module.Path + "@" + url.QueryEscape(module.Version), Digest: digest,
		})
	}
	return provenanceStatement{
		Type: "https://in-toto.io/Statement/v1", Subject: subjects,
		PredicateType: "https://slsa.dev/provenance/v1",
		Predicate: provenancePredicate{
			BuildDefinition: provenanceBuildDefinition{
				BuildType:            "https://synon.invalid/buildtypes/go-release/v1",
				ExternalParameters:   map[string]any{"goos": options.GOOS, "goarch": options.GOARCH, "version": releaseVersion},
				InternalParameters:   map[string]any{"sourceDirty": options.SourceDirty, "trimpath": true, "cgoEnabled": false},
				ResolvedDependencies: dependencies,
			},
			RunDetails: provenanceRunDetails{
				Builder:  provenanceBuilder{ID: "https://synon.invalid/builders/releasepkg/v1"},
				Metadata: provenanceMetadata{BuildStartedOn: created, BuildFinishedOn: created},
			},
		},
	}
}

func resolvePackageFile(root, relative string) (string, error) {
	if !validRelativePath(filepath.ToSlash(relative)) {
		return "", fmt.Errorf("invalid package-relative file %q", relative)
	}
	path := filepath.Join(root, filepath.FromSlash(relative))
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("package file %s must be a non-symlink regular file", relative)
	}
	return path, nil
}

func writeJSONArtifact(root, name string, document any) error {
	content, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	path := filepath.Join(root, name)
	temporary, err := os.CreateTemp(root, ".supply-chain-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
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
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("commit %s: %w", name, err)
	}
	return nil
}

func readStrictJSON(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 32<<20 {
		return errors.New("JSON artifact exceeds size limit")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 32<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON content")
		}
		return err
	}
	return nil
}

func escapeModuleCache(value string) string {
	var builder strings.Builder
	for _, char := range value {
		if unicode.IsUpper(char) {
			builder.WriteByte('!')
			builder.WriteRune(unicode.ToLower(char))
			continue
		}
		builder.WriteRune(char)
	}
	return builder.String()
}

func spdxModuleID(path, version string) string {
	digest := sha256.Sum256([]byte(path + "@" + version))
	return "SPDXRef-Package-" + hex.EncodeToString(digest[:12])
}

func subjectDigests(subjects []provenanceSubject) []string {
	values := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		values = append(values, subject.Name+":"+subject.Digest["sha256"])
	}
	sort.Strings(values)
	return values
}
