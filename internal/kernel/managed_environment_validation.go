package kernel

import (
	"bytes"
	"context"

	"encoding/json"
	"errors"
	"fmt"

	"os"
	"path/filepath"

	"strings"
)

// ErrManagedEnvironmentBinaryABI marks an environment generation that was
// assembled successfully but cannot be admitted because native extensions and
// their runtime libraries expose incompatible ABIs. Callers can use this
// sentinel to offer a changed-spec recovery without parsing diagnostics.
var ErrManagedEnvironmentBinaryABI = errors.New("managed environment binary ABI mismatch")

// validateManagedEnvironmentBinaryCompatibility rejects deterministic binary
// ABI mismatches before an immutable environment is advertised or admitted to
// a worker. This is intentionally a small, explicit set of package contracts:
// arbitrary stderr warnings are not failures, while a library reporting that
// it was compiled against a different native ABI is unsafe for scientific
// data processing even if a trivial import happens to return exit 0.
func validateManagedEnvironmentBinaryCompatibility(
	ctx context.Context,
	language, prefix string,
	packages []string,
) error {
	if language != "python" || !managedPackageSetContains(packages, "h5py") {
		return nil
	}
	python, err := managedPythonExecutableAtPrefix(prefix)
	if err != nil {
		return err
	}
	script := strings.Join([]string{
		"import json,h5py",
		"built=tuple(h5py.version.hdf5_built_version_tuple)",
		"runtime=tuple(h5py.version.hdf5_version_tuple)",
		"print(json.dumps({'ok':built==runtime,'built':list(built),'runtime':list(runtime)},sort_keys=True))",
	}, ";")
	command := newWorkerProcessCommand(ctx, python, "-I", "-c", script)
	command.Env = managedEnvironmentRuntimeEnv(prefix)
	var stdout bytes.Buffer
	stderr := newTailBuffer(maxDiagnosticBytes)
	command.Stdout, command.Stderr = &stdout, stderr
	if err := runWorkerProcess(command); err != nil {
		return fmt.Errorf("managed environment binary compatibility witness failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var result struct {
		OK      bool  `json:"ok"`
		Built   []int `json:"built"`
		Runtime []int `json:"runtime"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &result); err != nil || len(result.Built) != 3 || len(result.Runtime) != 3 {
		return errors.New("managed environment binary compatibility witness returned an invalid result")
	}
	if !result.OK {
		return fmt.Errorf(
			"%w: h5py was built against HDF5 %d.%d.%d but loaded %d.%d.%d; install a compatible h5py/HDF5 combination and retry",
			ErrManagedEnvironmentBinaryABI,
			result.Built[0], result.Built[1], result.Built[2], result.Runtime[0], result.Runtime[1], result.Runtime[2],
		)
	}
	return nil
}

func writeManagedEnvironmentMarker(path string, marker managedEnvironmentMarker) error {
	raw, err := json.MarshalIndent(marker, "", "  ")
	if err != nil || len(raw) > maxManagedEnvironmentInputBytes {
		return errors.New("managed environment marker is invalid")
	}
	raw = append(raw, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".managed-environment-*.tmp")
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

func readManagedEnvironmentMarker(prefix string) (managedEnvironmentMarker, error) {
	var marker managedEnvironmentMarker
	if _, err := readBoundedJSON(filepath.Join(prefix, managedEnvironmentMarkerName), &marker); err != nil {
		return managedEnvironmentMarker{}, err
	}
	if marker.SchemaVersion != managedEnvironmentLegacyVersion && marker.SchemaVersion != managedEnvironmentMarkerVersion ||
		!ValidEnvironmentName(marker.Name) || !validSHA256(marker.Generation) || len(marker.Packages) == 0 {
		return managedEnvironmentMarker{}, errors.New("managed environment marker contract is invalid")
	}
	if _, err := validateManagedLanguage(marker.Language, false); err != nil {
		return managedEnvironmentMarker{}, err
	}
	if marker.SpecDigest != "" && !validSHA256(marker.SpecDigest) {
		return managedEnvironmentMarker{}, errors.New("managed environment marker specification is invalid")
	}
	if marker.ValidationRevision < 0 || marker.ValidationRevision > managedEnvironmentValidationRevision {
		return marker, errors.New("managed environment verification revision is unsupported")
	}
	if marker.SchemaVersion == managedEnvironmentLegacyVersion && marker.ValidationRevision != 0 {
		return marker, errors.New("legacy environment receipt cannot assert current installation verification")
	}
	if _, err := validateManagedImportNames(marker.ImportNames); err != nil {
		return managedEnvironmentMarker{}, errors.New("managed environment marker import witness is invalid")
	}
	if err := validateManagedPipReplay(marker.PipReplay); err != nil {
		return managedEnvironmentMarker{}, err
	}
	switch marker.PipReplayRevision {
	case 0:
		if len(marker.PipReplay) != 0 || marker.PipReplayBaseDigest != "" {
			return managedEnvironmentMarker{}, errors.New("legacy environment marker has unbound pip replay")
		}
	case 1:
		if marker.PipReplayBaseDigest != "" && !validSHA256(marker.PipReplayBaseDigest) {
			return managedEnvironmentMarker{}, errors.New("managed environment pip replay base digest is invalid")
		}
		boundDigest, err := managedPipReplaySpecDigest(marker.PipReplayBaseDigest, marker.PipReplay)
		if err != nil || boundDigest != marker.SpecDigest {
			return managedEnvironmentMarker{}, errors.New("managed environment pip replay digest is invalid")
		}
	default:
		return managedEnvironmentMarker{}, errors.New("managed environment pip replay revision is unsupported")
	}
	if marker.Kind == "" {
		marker.Kind = "conda"
	}
	switch marker.Kind {
	case "conda":
		wantGeneration := managedEnvironmentGenerationAtValidation(marker.Name, marker.Language, marker.Packages, marker.SpecDigest, marker.ValidationRevision)
		if marker.SchemaVersion == managedEnvironmentLegacyVersion {
			wantGeneration = managedEnvironmentLegacyGeneration(marker.Name, marker.Language, marker.Packages, marker.SpecDigest)
		}
		if marker.Generation != wantGeneration {
			return managedEnvironmentMarker{}, errors.New("managed environment marker generation is invalid")
		}
	case "path-venv":
		sourcePath, sourceErr := canonicalManagedRegistrationPath(marker.SourcePath)
		runtimePath, runtimeErr := canonicalManagedRegistrationPath(marker.RuntimePath)
		if sourceErr != nil || runtimeErr != nil || sourcePath != marker.SourcePath || runtimePath != marker.RuntimePath ||
			marker.Generation != managedRegisteredEnvironmentGeneration(marker.Name, marker.Language, sourcePath, runtimePath, marker.Packages) {
			return managedEnvironmentMarker{}, errors.New("registered environment marker generation is invalid")
		}
	default:
		return managedEnvironmentMarker{}, errors.New("managed environment marker kind is invalid")
	}
	return marker, nil
}
