package sqliteutil

import (
	"errors"
	"strings"
)

// IsTransientContention reports whether SQLite temporarily refused work
// because another connection owns the database or table lock. Callers may
// retry idempotent operations, but must continue to surface every other error.
func IsTransientContention(err error) bool {
	if err == nil {
		return false
	}
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		// Extended SQLite codes keep the primary result in the low byte.
		// A typed permanent error must not be reclassified by quoted text.
		primary := coded.Code() & 0xff
		return primary == 5 || primary == 6
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(message, "sqlite_busy") ||
		strings.Contains(message, "sqlite_locked") ||
		strings.Contains(message, "database is locked") ||
		strings.Contains(message, "database table is locked")
}
