//go:build !linux || !amd64

package kernel

import "strings"

// environmentValue is also used by cross-platform kernel tests and helpers.
// Linux/amd64 keeps the confinement-specific implementation in its native
// file; other targets need the same bounded key lookup without importing the
// Linux confinement implementation.
func environmentValue(environment []string, key string) string {
	for _, item := range environment {
		name, value, ok := strings.Cut(item, "=")
		if ok && name == key {
			return value
		}
	}
	return ""
}
