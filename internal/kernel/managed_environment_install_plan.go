package kernel

import "context"

// Compile validated phases once. Both dependency provisioning and execution
// consume this plan; a native-only environment has no Python installation stage.
// It stays inside the existing supervised, immutable publication transaction.
func planManagedPipInstall(phases [][]string, options, findLinks, extraIndexes []string, lockedPath string) [][]string {
	inputs := phases
	if lockedPath != "" {
		inputs = [][]string{{"--require-hashes", "-r", lockedPath}}
	}
	plan := make([][]string, 0, len(inputs))
	for _, packages := range inputs {
		arguments := []string{"-I", "-m", "pip", "install", "--disable-pip-version-check", "--no-input", "--progress-bar", "on"}
		arguments = append(arguments, options...)
		arguments = append(arguments, managedPipSourceArguments(findLinks, extraIndexes)...)
		arguments = append(arguments, packages...)
		plan = append(plan, arguments)
	}
	return plan
}

func (m *Manager) runManagedPipInstallPlan(ctx context.Context, prefix string, plan [][]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(plan) == 0 {
		return nil
	}
	for _, arguments := range plan {
		if err := m.runManagedPipCommand(ctx, prefix, arguments); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) runManagedPipCommand(ctx context.Context, prefix string, arguments []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	python, err := managedPythonExecutableAtPrefix(prefix)
	if err != nil {
		return err
	}
	return m.runManagedEnvironmentProcessWithEnv(ctx, python, managedEnvironmentInstallerRuntimeEnv(prefix, m.config.InstallerProxy), arguments...)
}

// A newly requested installer stage may need capabilities absent from a native
// source runtime. Provision only missing packages in the unpublished successor;
// preserve existing interpreter pins and let additive resolution fence changes.
func managedPipBootstrapPackages(installed []string) []string {
	var missing []string
	if !managedPackageSetContains(installed, "python") {
		missing = append(missing, "python="+defaultManagedPythonVersion)
	}
	if !managedPackageSetContains(installed, "pip") {
		missing = append(missing, "pip")
	}
	return missing
}
