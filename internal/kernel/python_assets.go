package kernel

import "path/filepath"

// Shared by worker confinement and managed-environment materialization.
func pythonRuntimePackageAssets() []string {
	return []string{
		filepath.Join("synon_biomed_runtime", "__init__.py"),
		filepath.Join("synon_biomed_runtime", "cheminfo_render.py"),
		filepath.Join("synon_biomed_runtime", "matplotlib_runtime.py"),
		filepath.Join("synon_biomed_runtime", "python_code_compatibility.py"),
		filepath.Join("synon_biomed_runtime", "worker_transport.py"),
		filepath.Join("synon_biomed_runtime", "worker_streams.py"),
		filepath.Join("synon_biomed_runtime", "worker_compile.py"),
		filepath.Join("synon_biomed_runtime", "worker_execution.py"),
		filepath.Join("synon_biomed_runtime", "worker_safety.py"),
		filepath.Join("synon_biomed_runtime", "worker_reads.py"),
		filepath.Join("synon_biomed_runtime", "worker_effects.py"),
	}
}
