//go:build linux

package detached

import (
	"errors"
	"strings"
	"testing"
)

func TestDetachedExecutionErrorPreservesBoundedSubmitCause(t *testing.T) {
	got := detachedExecutionError(errors.New("a background python cell is already running\nfor environment science"))
	if got != "kernel execution failed: a background python cell is already running for environment science" {
		t.Fatalf("diagnostic=%q", got)
	}
	long := detachedExecutionError(errors.New(strings.Repeat("错", 700)))
	if !strings.HasSuffix(long, "…") || len([]rune(long)) > 540 {
		t.Fatalf("bounded diagnostic runes=%d", len([]rune(long)))
	}
}
