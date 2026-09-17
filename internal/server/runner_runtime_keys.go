package server

import (
	"strings"

	eventjournal "synon-go/internal/persistence/journal"
)

func runtimeKeyFromSessionID(sessionID string) string {
	key := strings.TrimSpace(sessionID)
	if key == "" {
		return "default"
	}
	return strings.NewReplacer(":", "-", "/", "-", "\\", "-", " ", "-").Replace(key)
}

func maxEntryIDFromEntries(entries []eventjournal.Entry) int64 {
	var maximum int64
	for _, entry := range entries {
		if entry.EventID > maximum {
			maximum = entry.EventID
		}
	}
	return maximum
}
