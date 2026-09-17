package sqliteutil

import (
	"errors"
	"testing"
)

func TestIsTransientContention(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "busy code", err: errors.New("SQLITE_BUSY"), want: true},
		{name: "locked database", err: errors.New("wrapped: database is locked (5)"), want: true},
		{name: "locked table", err: errors.New("database table is locked"), want: true},
		{name: "permanent", err: errors.New("schema is unavailable"), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsTransientContention(test.err); got != test.want {
				t.Fatalf("IsTransientContention(%v)=%t want %t", test.err, got, test.want)
			}
		})
	}
}
