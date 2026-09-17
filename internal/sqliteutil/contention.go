package sqliteutil

import "strings"

// IsTransientContention reports whether SQLite temporarily refused work
// because another connection owns the database or table lock. Callers may
// retry idempotent operations, but must continue to surface every other error.
func IsTransientContention(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(message, "sqlite_busy") ||
		strings.Contains(message, "sqlite_locked") ||
		strings.Contains(message, "database is locked") ||
		strings.Contains(message, "database table is locked")
}
