//go:build !linux

package detached

// The detached execution backend is intentionally unavailable on non-Linux
// platforms until an equally fenced process supervisor exists there.
