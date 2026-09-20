package server

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

type runnerTemplateCursor struct {
	ctx             context.Context
	source          io.ReadSeeker
	reader          *bufio.Reader
	position        int64
	previous        rune
	observe         func(rune)
	stripArtifacts  bool
	probingArtifact bool
}

func newRunnerTemplateCursor(ctx context.Context, source io.ReadSeeker) (*runnerTemplateCursor, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cursor := &runnerTemplateCursor{ctx: ctx, source: source}
	return cursor, cursor.seek(0)
}

func (cursor *runnerTemplateCursor) seek(position int64) error {
	if err := cursor.ctx.Err(); err != nil {
		return err
	}
	if _, err := cursor.source.Seek(position, io.SeekStart); err != nil {
		return err
	}
	cursor.position = position
	input := &contextReader{ctx: cursor.ctx, reader: cursor.source}
	if cursor.reader == nil {
		cursor.reader = bufio.NewReader(input)
	} else {
		cursor.reader.Reset(input)
	}
	return nil
}

func (cursor *runnerTemplateCursor) read() (rune, error) {
	if err := cursor.skipArtifacts(); err != nil {
		return 0, err
	}
	value, size, err := cursor.reader.ReadRune()
	if err == nil {
		cursor.position += int64(size)
		cursor.previous = value
		if cursor.observe != nil {
			cursor.observe(value)
		}
	}
	return value, err
}

func (cursor *runnerTemplateCursor) peek() (rune, error) {
	if err := cursor.skipArtifacts(); err != nil {
		return 0, err
	}
	data, err := cursor.reader.Peek(1)
	if len(data) == 0 {
		return 0, err
	}
	if data[0] < utf8.RuneSelf {
		return rune(data[0]), nil
	}
	data, _ = cursor.reader.Peek(utf8.UTFMax)
	value, _ := utf8.DecodeRune(data)
	return value, nil
}

// Remove artifact placeholders before every lexical read, including reads
// inside identifiers and math delimiters. This preserves adjacency semantics
// without materializing a second copy of the complete document.
func (cursor *runnerTemplateCursor) skipArtifacts() error {
	if !cursor.stripArtifacts || cursor.probingArtifact {
		return nil
	}
	cursor.probingArtifact = true
	defer func() { cursor.probingArtifact = false }()
	for {
		value, err := cursor.peek()
		if err != nil {
			return err
		}
		if value != '{' {
			return nil
		}
		position, previous := cursor.position, cursor.previous
		_, _ = cursor.read()
		next, err := cursor.peek()
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if next == '{' {
			_, _ = cursor.read()
			token, err := readRunnerTemplateToken(cursor)
			if err != nil {
				return err
			}
			if token.matched && strings.HasPrefix(strings.ToLower(token.text), "artifact:") {
				cursor.previous = previous
				continue
			}
		}
		if err := cursor.seek(position); err != nil {
			return err
		}
		cursor.previous = previous
		return nil
	}
}

// A valid artifact placeholder has a fixed UUID grammar. Long surrounding
// whitespace is ignored without accumulation. Invalid unbounded tokens carry
// an exact digest, instead of allocating their entire diagnostic value.
type runnerTemplateToken struct {
	text      string
	oversized bool
	digest    string
	matched   bool
}

func readRunnerTemplateToken(cursor *runnerTemplateCursor) (runnerTemplateToken, error) {
	const captureBytes = 128 // Covers every accepted UUID spelling plus prefix.
	var token, whitespace strings.Builder
	hash := sha256.New()
	anyRune, pendingOverflow, oversized := false, false, false
	for {
		value, err := cursor.peek()
		if errors.Is(err, io.EOF) {
			return runnerTemplateToken{}, nil
		}
		if err != nil {
			return runnerTemplateToken{}, err
		}
		if value == '{' {
			return runnerTemplateToken{}, nil
		}
		if value == '}' {
			_, _ = cursor.read()
			closing, err := cursor.peek()
			if err != nil && !errors.Is(err, io.EOF) {
				return runnerTemplateToken{}, err
			}
			if err == nil && closing == '}' && anyRune {
				_, _ = cursor.read()
				return runnerTemplateToken{text: token.String(), oversized: oversized, digest: hex.EncodeToString(hash.Sum(nil)), matched: true}, nil
			}
			return runnerTemplateToken{}, nil
		}
		value, err = cursor.read()
		if err != nil {
			return runnerTemplateToken{}, err
		}
		anyRune = true
		_, _ = io.WriteString(hash, string(value))
		if unicode.IsSpace(value) {
			if token.Len() > 0 {
				if whitespace.Len()+utf8.RuneLen(value) <= captureBytes {
					whitespace.WriteRune(value)
				} else {
					pendingOverflow = true
				}
			}
			continue
		}
		if pendingOverflow || token.Len()+whitespace.Len()+utf8.RuneLen(value) > captureBytes {
			oversized = true
		}
		if !oversized {
			token.WriteString(whitespace.String())
			token.WriteRune(value)
		}
		whitespace.Reset()
		pendingOverflow = false
	}
}

func runnerTemplateScanName(name string) bool {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(name))) {
	case ".md", ".txt", ".html", ".htm", ".csv", ".tsv":
		return true
	default:
		return false
	}
}

// Both save and completion use this complete two-pass scanner. The second
// pass can seek over matched math spans, preserving the distinction between
// rendered mathematical braces and unresolved format fields at any file size.
func scanRunnerTemplateFailures(ctx context.Context, source io.ReadSeeker, name string) ([]string, error) {
	if !runnerTemplateScanName(name) {
		return nil, nil
	}
	cursor, err := newRunnerTemplateCursor(ctx, source)
	if err != nil {
		return nil, err
	}
	var prefix []rune
	var tail, lastNonSpace [3]rune
	previous, jinja := rune(0), false
	cursor.observe = func(value rune) {
		if previous == '{' && value == '%' || previous == '%' && value == '}' {
			jinja = true
		}
		previous = value
		if len(prefix) > 0 || !unicode.IsSpace(value) {
			if len(prefix) < 5 {
				prefix = append(prefix, value)
			}
		}
		tail[0], tail[1], tail[2] = tail[1], tail[2], value
		if !unicode.IsSpace(value) {
			lastNonSpace = tail
		}
	}
	firstFailure := ""
	for {
		value, err := cursor.read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if value != '{' {
			continue
		}
		next, err := cursor.peek()
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if next != '{' {
			continue
		}
		_, _ = cursor.read()
		// For a run of opening braces the regexp grammar starts at the final
		// pair. Retain that overlap instead of consuming the only valid opener.
		for {
			if next, _ := cursor.peek(); next != '{' {
				break
			}
			_, _ = cursor.read()
		}
		token, err := readRunnerTemplateToken(cursor)
		if err != nil {
			return nil, err
		}
		if !token.matched || firstFailure != "" {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(token.text), "artifact:") {
			firstFailure = "unresolved_template_marker:" + name
		} else {
			reference := strings.TrimSpace(token.text[len("artifact:"):])
			if _, err := uuid.Parse(reference); err != nil || token.oversized {
				if token.oversized {
					firstFailure = "malformed_artifact_placeholder:" + name + " value_sha256=" + token.digest
				} else {
					firstFailure = "malformed_artifact_placeholder:" + name + " value=" + reference
				}
			}
		}
	}
	lowerPrefix := strings.ToLower(string(prefix))
	lastNonSpaceTail := string(lastNonSpace[:])
	script := (strings.HasPrefix(lowerPrefix, "<?php") || strings.HasPrefix(lowerPrefix, "<?=")) && strings.HasSuffix(lastNonSpaceTail, "?>") || strings.HasPrefix(lowerPrefix, "<%") && strings.HasSuffix(lastNonSpaceTail, "%>")
	if script || jinja {
		return []string{"unresolved_template_marker:" + name}, nil
	}
	format, err := runnerStreamHasFormatField(ctx, source)
	if err != nil {
		return nil, err
	}
	if format {
		return []string{"unresolved_template_marker:" + name}, nil
	}
	if firstFailure != "" {
		return []string{firstFailure}, nil
	}
	return nil, nil
}
