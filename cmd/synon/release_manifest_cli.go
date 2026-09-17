package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"synon-go/internal/releasepkg"
)

func runReleaseManifestCLI(args []string, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: synon-go release-manifest <create|verify> --root <package-dir>")
	}
	switch args[0] {
	case "create":
		flags := newReleaseManifestFlagSet("release-manifest create")
		root := flags.String("root", "", "release package directory")
		goos := flags.String("goos", "", "target operating system")
		goarch := flags.String("goarch", "", "target architecture")
		requireSupplyChain := flags.Bool("require-supply-chain", false, "require verified clean-source SBOM, licenses, and provenance")
		if err := parseReleaseManifestFlags(flags, args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*root) == "" {
			return errors.New("--root is required")
		}
		generatedAt, err := releaseGeneratedAt()
		if err != nil {
			return err
		}
		manifest, err := releasepkg.Generate(*root, releasepkg.Options{
			GeneratedAt:        generatedAt,
			GOOS:               *goos,
			GOARCH:             *goarch,
			RequireSupplyChain: *requireSupplyChain,
		})
		if err != nil {
			return err
		}
		return writeMigrationJSON(output, manifest)
	case "verify":
		flags := newReleaseManifestFlagSet("release-manifest verify")
		root := flags.String("root", "", "release package directory")
		if err := parseReleaseManifestFlags(flags, args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*root) == "" {
			return errors.New("--root is required")
		}
		report, err := releasepkg.Verify(*root)
		if err != nil {
			return err
		}
		return writeMigrationJSON(output, report)
	default:
		return fmt.Errorf("unknown release-manifest action %q", args[0])
	}
}

func releaseGeneratedAt() (time.Time, error) {
	raw := strings.TrimSpace(os.Getenv("SOURCE_DATE_EPOCH"))
	if raw == "" {
		return time.Time{}, nil
	}
	epoch, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || epoch < 0 {
		return time.Time{}, fmt.Errorf("SOURCE_DATE_EPOCH must be a non-negative Unix timestamp")
	}
	return time.Unix(epoch, 0).UTC(), nil
}

func newReleaseManifestFlagSet(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}

func parseReleaseManifestFlags(flags *flag.FlagSet, args []string) error {
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	return nil
}
