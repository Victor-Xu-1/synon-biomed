package kernel

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// managedRuntimePlatform is the product-level identity for a native Conda
// runtime bundle.  The identity is intentionally separate from Go's GOOS/
// GOARCH spelling and from Conda's package subdirectory spelling: both are
// external asset contracts and must not leak into user-facing storage names.
type managedRuntimePlatform struct {
	ID           string
	CondaSubdir  string
	Windows      bool
	PythonLibDir string
}

func managedRuntimePlatformFor(goos, goarch string) (managedRuntimePlatform, bool) {
	switch {
	case goos == "linux" && goarch == "amd64":
		return managedRuntimePlatform{ID: "linux-x86_64", CondaSubdir: "linux-64", PythonLibDir: "lib"}, true
	case goos == "windows" && goarch == "amd64":
		return managedRuntimePlatform{ID: "windows-x86_64", CondaSubdir: "win-64", Windows: true, PythonLibDir: "Lib"}, true
	case goos == "darwin" && goarch == "amd64":
		return managedRuntimePlatform{ID: "darwin-x86_64", CondaSubdir: "osx-64", PythonLibDir: "lib"}, true
	case goos == "darwin" && goarch == "arm64":
		return managedRuntimePlatform{ID: "darwin-arm64", CondaSubdir: "osx-arm64", PythonLibDir: "lib"}, true
	default:
		return managedRuntimePlatform{}, false
	}
}

func currentManagedRuntimePlatform() (managedRuntimePlatform, bool) {
	return managedRuntimePlatformFor(runtime.GOOS, runtime.GOARCH)
}

func currentCondaPlatform() string {
	if platform, ok := currentManagedRuntimePlatform(); ok {
		return platform.ID
	}
	return runtime.GOOS + "-" + runtime.GOARCH
}

func currentCondaSubdir() string {
	if platform, ok := currentManagedRuntimePlatform(); ok {
		return platform.CondaSubdir
	}
	return ""
}

// ManagedScientificRuntimePlatformSupported reports whether the current
// process can consume one of the native runtime bundles shipped by Synon.
// Unsupported targets remain explicit instead of silently reusing a foreign
// lock or an incompatible interpreter layout.
func ManagedScientificRuntimePlatformSupported() bool {
	_, ok := currentManagedRuntimePlatform()
	return ok
}

// ManagedScientificRuntimePlatform is the stable platform identity used by
// runtime status consumers. It deliberately reports the catalog spelling
// rather than a machine-specific path.
func ManagedScientificRuntimePlatform() string {
	return currentCondaPlatform()
}

func managedRuntimePlatformForCurrentHost() (managedRuntimePlatform, error) {
	platform, ok := currentManagedRuntimePlatform()
	if !ok {
		return managedRuntimePlatform{}, fmt.Errorf("managed scientific runtime is unsupported on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return platform, nil
}

func managedRuntimeStateHome() string {
	if configured := strings.TrimSpace(os.Getenv("SYNON_HOME")); configured != "" {
		return filepath.Clean(configured)
	}
	if runtime.GOOS == "windows" {
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			return filepath.Join(home, ".synon-go")
		}
	}
	if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
		return filepath.Join(home, ".synon-go")
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".synon-go")
	}
	return ".synon-go"
}

func managedRuntimeExecutableDirectories(prefix string) []string {
	platform, ok := currentManagedRuntimePlatform()
	if !ok || !platform.Windows {
		return []string{filepath.Join(prefix, "bin")}
	}
	// Conda on Windows places console entry points in Scripts and native DLLs
	// in Library/bin. Keep the prefix itself last for packages that expose a
	// root-level launcher, without inheriting the caller's current directory.
	return []string{
		filepath.Join(prefix, "Scripts"),
		filepath.Join(prefix, "Library", "bin"),
		prefix,
	}
}

func managedRuntimePythonPurelibRelative(pythonVersion string) (string, error) {
	parts := strings.Split(strings.TrimSpace(pythonVersion), ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("managed Python version is invalid")
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	if majorErr != nil || minorErr != nil || major != 3 || minor < 8 || minor > 99 {
		return "", fmt.Errorf("managed Python version is unsupported")
	}
	platform, ok := currentManagedRuntimePlatform()
	if !ok {
		return "", fmt.Errorf("managed Python platform is unsupported")
	}
	if platform.Windows {
		return filepath.Join(platform.PythonLibDir, "site-packages"), nil
	}
	return filepath.Join(platform.PythonLibDir, fmt.Sprintf("python%d.%d", major, minor), "site-packages"), nil
}

func managedRuntimePath(prefix string) string {
	return strings.Join(managedRuntimeExecutableDirectories(prefix), string(filepath.ListSeparator))
}
