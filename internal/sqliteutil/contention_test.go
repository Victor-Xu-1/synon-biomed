package sqliteutil

import (
	"errors"
	"testing"
)

type codedStoreError struct {
	code    int
	message string
}

func (e codedStoreError) Code() int     { return e.code }
func (e codedStoreError) Error() string { return e.message }

func TestContentionUsesPrimaryDriverCodeBeforeMessage(t *testing.T) {
	for _, test := range []struct {
		code    int
		message string
		want    bool
	}{
		{5, "opaque busy", true}, {517, "snapshot", true}, {262, "shared cache", true},
		{11, "corrupt page containing database is locked", false}, {13, "disk full", false},
	} {
		if got := IsTransientContention(errors.Join(errors.New("operation"), codedStoreError{test.code, test.message})); got != test.want {
			t.Fatalf("code %d contention=%v", test.code, got)
		}
	}
}

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
