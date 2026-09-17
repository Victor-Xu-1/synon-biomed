package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"synon-go/internal/releasepkg"
)

func runReleaseSupplyChainCLI(args []string, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: synon-go release-supply-chain <create|verify> --root <package-dir>")
	}
	switch args[0] {
	case "create":
		flags := newReleaseManifestFlagSet("release-supply-chain create")
		root := flags.String("root", "", "release package directory")
		goos := flags.String("goos", "", "target operating system")
		goarch := flags.String("goarch", "", "target architecture")
		binaries := flags.String("binaries", "", "comma-separated package-relative Go binaries")
		moduleCache := flags.String("module-cache", "", "Go module cache used for license evidence")
		goRoot := flags.String("go-root", "", "Go root used for standard-library license evidence")
		licenseOverrides := flags.String("license-overrides", "", "version-pinned license evidence used only when a module archive lacks a license file")
		sourceURI := flags.String("source-uri", "", "source identity without local credentials")
		sourceRevision := flags.String("source-revision", "", "source revision identity")
		sourceSHA256 := flags.String("source-sha256", "", "SHA-256 of the source tree used for the build")
		sourceDirty := flags.Bool("source-dirty", false, "record that the source tree differs from its revision")
		if err := parseReleaseManifestFlags(flags, args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*root) == "" {
			return errors.New("--root is required")
		}
		binaryPaths := splitNonEmpty(*binaries)
		generatedAt, err := releaseGeneratedAt()
		if err != nil {
			return err
		}
		if generatedAt.IsZero() {
			return errors.New("SOURCE_DATE_EPOCH is required for reproducible supply-chain artifacts")
		}
		report, err := releasepkg.GenerateSupplyChain(*root, releasepkg.SupplyChainOptions{
			GeneratedAt: generatedAt, GOOS: *goos, GOARCH: *goarch,
			BinaryPaths: binaryPaths, ModuleCache: *moduleCache, GoRoot: *goRoot, LicenseOverrides: *licenseOverrides,
			SourceURI: *sourceURI, SourceRevision: *sourceRevision,
			SourceSHA256: strings.TrimSpace(*sourceSHA256), SourceDirty: *sourceDirty,
		})
		if err != nil {
			return err
		}
		return writeMigrationJSON(output, report)
	case "verify":
		flags := newReleaseManifestFlagSet("release-supply-chain verify")
		root := flags.String("root", "", "release package directory")
		if err := parseReleaseManifestFlags(flags, args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*root) == "" {
			return errors.New("--root is required")
		}
		report, err := releasepkg.VerifySupplyChain(*root)
		if err != nil {
			return err
		}
		return writeMigrationJSON(output, report)
	default:
		return fmt.Errorf("unknown release-supply-chain action %q", args[0])
	}
}

func splitNonEmpty(raw string) []string {
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}
