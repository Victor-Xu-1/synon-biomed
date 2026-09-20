package server

import (
	"context"
	"errors"
	"io"
	"strings"
)

func runnerStreamHasFormatField(ctx context.Context, source io.ReadSeeker) (bool, error) {
	cursor, err := newRunnerTemplateCursor(ctx, source)
	if err != nil {
		return false, err
	}
	cursor.stripArtifacts = true
	for {
		previous := cursor.previous
		value, err := cursor.read()
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if value == '$' && previous != '\\' {
			double := false
			if next, _ := cursor.peek(); next == '$' {
				double = true
				_, _ = cursor.read()
			}
			body := cursor.position
			closed, err := runnerTemplateSeekMathEnd(cursor, double)
			if err != nil {
				return false, err
			}
			if closed {
				cursor.previous = ' '
				continue
			}
			if err := cursor.seek(body); err != nil {
				return false, err
			}
			cursor.previous = '$'
			continue
		}
		if value != '{' {
			continue
		}
		matched, err := runnerTemplateReadFormatBody(cursor)
		if err != nil {
			return false, err
		}
		if matched && !runnerTemplateFormatPrefixExempt(previous) {
			return true, nil
		}
	}
}

func runnerTemplateSeekMathEnd(cursor *runnerTemplateCursor, double bool) (bool, error) {
	for {
		value, err := cursor.read()
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if value != '$' {
			continue
		}
		if !double {
			return true, nil
		}
		if next, _ := cursor.peek(); next == '$' {
			_, _ = cursor.read()
			return true, nil
		}
	}
}

func runnerTemplateIdentifierRune(value rune, first bool) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value == '_' || !first && value >= '0' && value <= '9'
}

func runnerTemplateReadFormatBody(cursor *runnerTemplateCursor) (bool, error) {
	value, err := cursor.peek()
	if errors.Is(err, io.EOF) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !runnerTemplateIdentifierRune(value, true) {
		return false, nil
	}
	for {
		value, err = cursor.peek()
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !runnerTemplateIdentifierRune(value, false) {
			break
		}
		_, _ = cursor.read()
	}
	if value == '!' {
		_, _ = cursor.read()
		value, err = cursor.peek()
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		if value != 'r' && value != 's' && value != 'a' {
			return false, nil
		}
		_, _ = cursor.read()
		value, err = cursor.peek()
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
	}
	if value == ':' {
		_, _ = cursor.read()
		length := 0
		for {
			value, err = cursor.peek()
			if errors.Is(err, io.EOF) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			if value == '}' {
				if length == 0 {
					return false, nil
				}
				break
			}
			if value == '{' || value == '\r' || value == '\n' || length == 40 {
				return false, nil
			}
			_, _ = cursor.read()
			length++
		}
	}
	if value != '}' {
		return false, nil
	}
	_, _ = cursor.read()
	return true, nil
}

func runnerTemplateFormatPrefixExempt(value rune) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || strings.ContainsRune(`_\\^~`, value)
}
