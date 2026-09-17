package kernel

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

var managedExecutableWitnessName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._+-]{0,127}$`)

// VerifyManagedEnvironmentExecutable proves that an executable resolves from
// the active immutable generation without exposing its host path.
func (m *Manager) VerifyManagedEnvironmentExecutable(environment, executable string) error {
	executable = strings.TrimSpace(executable)
	if !managedExecutableWitnessName.MatchString(executable) || filepath.Base(executable) != executable {
		return errors.New("managed environment executable witness must be a path-free program name")
	}
	if _, err := m.managedEnvironmentExecutable(environment, executable); err != nil {
		return err
	}
	return nil
}

// VerifyManagedEnvironmentImports validates request-specific import witnesses
// against the active immutable generation. Import names are evidence checks,
// not installed content, so callers can reuse one content-addressed software
// environment while independently proving every requested module.
func (m *Manager) VerifyManagedEnvironmentImports(ctx context.Context, environment string, imports []string) error {
	if err := validateManagedEnvironmentName(environment); err != nil {
		return err
	}
	validated, err := validateManagedImportNames(imports)
	if err != nil {
		return err
	}
	if len(validated) == 0 {
		return nil
	}
	prefix, language, err := m.managedWitnessRuntime(environment)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, managedEnvironmentHealthTimeout)
		defer cancel()
	}
	return validateManagedEnvironmentImports(ctx, language, prefix, validated)
}

func (m *Manager) managedPythonWitnessPrefix(environment string) (string, error) {
	prefix, language, err := m.managedWitnessRuntime(environment)
	if err != nil {
		return "", err
	}
	if language != "python" {
		return "", errors.New("managed Python environment generation marker is invalid")
	}
	return prefix, nil
}

func (m *Manager) managedWitnessRuntime(environment string) (string, string, error) {
	if err := validateManagedEnvironmentName(environment); err != nil {
		return "", "", err
	}
	// The service-owned bundled Python runtime is content-addressed and uses
	// the stricter .synon-runtime.json marker contract. Do not route it through
	// the generic Conda marker reader.
	if strings.TrimSpace(environment) == m.ManagedPythonEnvironmentName() &&
		strings.TrimSpace(m.config.CondaRuntimeCatalog) != "" {
		prefix, err := m.ManagedPythonActivePrefix()
		return prefix, "python", err
	}
	root, err := m.managedEnvironmentRoot()
	if err != nil {
		return "", "", err
	}
	prefix, err := filepath.EvalSymlinks(filepath.Join(root, environment))
	if err != nil {
		return "", "", errors.New("managed environment generation is unavailable")
	}
	if filepath.Dir(prefix) != filepath.Join(root, ".generations", environment) {
		return "", "", errors.New("managed environment witness is outside its trusted generation root")
	}
	marker, err := readManagedEnvironmentMarker(prefix)
	if err != nil || marker.Name != environment || marker.Generation != filepath.Base(prefix) {
		return "", "", errors.New("managed environment generation marker is invalid")
	}
	if needsRebuild, err := managedEnvironmentNeedsRebuild(prefix, marker); err != nil || needsRebuild {
		return "", "", ErrManagedEnvironmentRebuildRequired
	}
	if marker.Kind == "path-venv" {
		prefix = marker.RuntimePath
	}
	return prefix, marker.Language, nil
}
