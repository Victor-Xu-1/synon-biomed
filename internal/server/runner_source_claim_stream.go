package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

func scanRunnerSourceEvidenceLedger(ctx context.Context, source io.Reader, name, corpus string,
	handles map[string]struct{}, classify bool,
) ([]string, error) {
	var selectHeader func(map[string]int) bool
	if classify {
		selectHeader = runnerEvidenceLedgerHeaderShape
	}
	var failures []string
	err := visitRunnerEvidenceRows(ctx, source, name, selectHeader, func(rowNumber int, headers map[string]int, row []string) error {
		for _, cell := range row {
			if _, exposed := handles[strings.TrimSpace(cell)]; exposed {
				failures = append(failures, fmt.Sprintf("machine_validation_internal_runtime_reference:%s row=%d", name, rowNumber))
				break
			}
		}
		failures = append(failures, validateRunnerSourceEvidenceLedgerRecordsAtRow(name, [][]string{nil, row}, headers, corpus, rowNumber)...)
		return nil
	})
	var parseError *csv.ParseError
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if errors.As(err, &parseError) {
		return []string{"source_evidence_invalid_delimited:" + name}, nil
	}
	return failures, err
}

var errRunnerSourceJSONShape = errors.New("invalid source evidence JSON shape")

// The sources array is consumed one source at a time. Unrelated metadata is
// token-scanned rather than accumulated; every source and final EOF is checked.
func scanRunnerSourceEvidenceDocument(ctx context.Context, source io.Reader, name, corpus string,
	handles map[string]struct{},
) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	decoder := json.NewDecoder(&contextReader{ctx: ctx, reader: source})
	var failures []string
	exposed := false
	inspect := func(value string) {
		if _, found := handles[strings.TrimSpace(value)]; found {
			exposed = true
		}
	}
	root, err := decoder.Token()
	if err == nil && root != nil {
		if root != json.Delim('{') {
			err = errRunnerSourceJSONShape
		} else {
			var documentFailures []string
			documentFailures, err = scanRunnerSourceObject(ctx, decoder, name, corpus, inspect, handles, &exposed)
			failures = append(failures, documentFailures...)
		}
	}
	if err == nil {
		_, trailingErr := decoder.Token()
		if !errors.Is(trailingErr, io.EOF) {
			err = trailingErr
			if err == nil {
				err = errRunnerSourceJSONShape
			}
		}
	}
	if cause := ctx.Err(); cause != nil {
		return nil, cause
	}
	if err != nil {
		var syntax *json.SyntaxError
		var shape *json.UnmarshalTypeError
		if errors.As(err, &syntax) || errors.As(err, &shape) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, errRunnerSourceJSONShape) {
			return []string{"source_evidence_invalid_json:" + name}, nil
		}
		return nil, err
	}
	if exposed {
		failures = append(failures, "machine_validation_internal_runtime_reference:"+name)
	}
	return failures, nil
}

func scanRunnerSourceObject(ctx context.Context, decoder *json.Decoder, name, corpus string,
	inspect func(string), handles map[string]struct{}, exposed *bool,
) ([]string, error) {
	var failures []string
	for decoder.More() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, errRunnerSourceJSONShape
		}
		// Preserve the existing decoder's last-value semantics for repeated
		// root fields. Validation is not a new JSON dialect.
		if key == "sources" {
			failures = nil
		}
		value, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		if key != "sources" || value != json.Delim('[') {
			if err := skipRunnerSourceJSONValue(decoder, value, inspect); err != nil {
				return nil, err
			}
			continue
		}
		for index := 0; decoder.More(); index++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			var source any
			if err := decoder.Decode(&source); err != nil {
				return nil, err
			}
			failures = append(failures, validateRunnerSourceEvidenceSources(name, []any{source}, corpus, index)...)
			*exposed = *exposed || runnerSourceValueContainsInternalHandle(source, handles)
		}
		if end, err := decoder.Token(); err != nil {
			return nil, err
		} else if end != json.Delim(']') {
			return nil, errRunnerSourceJSONShape
		}
	}
	if end, err := decoder.Token(); err != nil {
		return nil, err
	} else if end != json.Delim('}') {
		return nil, errRunnerSourceJSONShape
	}
	return failures, nil
}

func skipRunnerSourceJSONValue(decoder *json.Decoder, token json.Token, inspect func(string)) error {
	switch value := token.(type) {
	case string:
		inspect(value)
	case json.Delim:
		if value != '{' && value != '[' {
			return errRunnerSourceJSONShape
		}
		for decoder.More() {
			if value == '{' {
				if _, err := decoder.Token(); err != nil {
					return err
				}
			}
			next, err := decoder.Token()
			if err != nil {
				return err
			}
			if err := skipRunnerSourceJSONValue(decoder, next, inspect); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	}
	return nil
}
