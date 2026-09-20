package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"sort"
	"strings"
)

type runnerArtifactContractEnvelope struct {
	matched                bool
	valid                  bool
	tablesStart, tablesEnd int64
	count                  int
}

// Probe only the envelope and the last tables range. Unknown large metadata
// is token-walked, and recognized contracts are decoded one table at a time.
func probeRunnerArtifactContract(ctx context.Context, source io.ReadSeeker) (runnerArtifactContractEnvelope, error) {
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return runnerArtifactContractEnvelope{}, err
	}
	decoder := json.NewDecoder(&contextReader{ctx: ctx, reader: source})
	first, err := decoder.Token()
	if err != nil {
		if runnerJSONDataError(err) {
			return runnerArtifactContractEnvelope{}, nil
		}
		return runnerArtifactContractEnvelope{}, err
	}
	if first != json.Delim('{') {
		return runnerArtifactContractEnvelope{}, nil
	}
	schema, unknown, schemaTypeError, tablesTypeError := "", false, false, false
	envelope := runnerArtifactContractEnvelope{}
	for decoder.More() {
		if err := ctx.Err(); err != nil {
			return envelope, err
		}
		keyToken, err := decoder.Token()
		if err != nil {
			return envelope, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return envelope, nil
		}
		value, err := decoder.Token()
		if err != nil {
			return envelope, err
		}
		switch {
		case strings.EqualFold(key, "schema"):
			if text, ok := value.(string); ok {
				schema = text
			} else if value != nil {
				schemaTypeError = true
			}
			if err := skipRunnerSourceJSONValue(decoder, value, func(string) {}); err != nil {
				return envelope, err
			}
		case strings.EqualFold(key, "tables"):
			envelope.count = 0
			envelope.tablesStart = 0
			envelope.tablesEnd = 0
			if value != json.Delim('[') {
				if value != nil {
					tablesTypeError = true
				}
				if err := skipRunnerSourceJSONValue(decoder, value, func(string) {}); err != nil {
					return envelope, err
				}
				continue
			}
			envelope.tablesStart = decoder.InputOffset()
			for decoder.More() {
				token, err := decoder.Token()
				if err != nil {
					return envelope, err
				}
				if err := skipRunnerSourceJSONValue(decoder, token, func(string) {}); err != nil {
					return envelope, err
				}
				envelope.count++
			}
			if _, err := decoder.Token(); err != nil {
				return envelope, err
			}
			envelope.tablesEnd = decoder.InputOffset()
		default:
			unknown = true
			if err := skipRunnerSourceJSONValue(decoder, value, func(string) {}); err != nil {
				return envelope, err
			}
		}
	}
	if _, err := decoder.Token(); err != nil {
		return envelope, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil && !runnerJSONDataError(err) {
			return envelope, err
		}
		return runnerArtifactContractEnvelope{}, nil
	}
	envelope.matched = schema == runnerArtifactTableContractSchema && !schemaTypeError
	envelope.valid = envelope.matched && !unknown && !tablesTypeError && envelope.count > 0 && envelope.count <= 16
	return envelope, nil
}

func inspectRunnerArtifactContracts(store *runnerArtifactScanStore, inputs []runnerArtifactScanInput, byArtifact map[string][]runnerCrossArtifactTable) (runnerArtifactTableContractValidation, error) {
	result := runnerArtifactTableContractValidation{covered: make(map[string]struct{})}
	seen := make(map[string]struct{})
	for _, input := range inputs {
		reader, err := input.openText(store.ctx)
		if err != nil {
			return result, err
		}
		validation, ids, err := inspectRunnerArtifactContractInput(store, input.name, reader, byArtifact, seen)
		closeErr := reader.Close()
		if err != nil || closeErr != nil {
			return result, errors.Join(err, closeErr)
		}
		result.failures = append(result.failures, validation.failures...)
		for pair := range validation.covered {
			result.covered[pair] = struct{}{}
		}
		for id := range ids {
			seen[id] = struct{}{}
		}
	}
	sort.Strings(result.failures)
	return result, nil
}

func inspectRunnerArtifactContractInput(store *runnerArtifactScanStore, name string, source io.ReadSeeker, tables map[string][]runnerCrossArtifactTable, seen map[string]struct{}) (runnerArtifactTableContractValidation, map[string]struct{}, error) {
	result := runnerArtifactTableContractValidation{covered: make(map[string]struct{})}
	ids := make(map[string]struct{})
	envelope, err := probeRunnerArtifactContract(store.ctx, source)
	if err != nil {
		if runnerJSONDataError(err) {
			return result, ids, nil
		}
		return result, nil, err
	}
	if !envelope.matched {
		return result, ids, nil
	}
	invalidSchema := func() (runnerArtifactTableContractValidation, map[string]struct{}, error) {
		return runnerArtifactTableContractValidation{failures: []string{"artifact_table_contract_invalid:" + filepath.Base(name) + " reason=invalid_schema"}}, nil, nil
	}
	if !envelope.valid {
		return invalidSchema()
	}
	if _, err := source.Seek(envelope.tablesStart, io.SeekStart); err != nil {
		return result, nil, err
	}
	tracked := &runnerErrorTrackingReader{source: &contextReader{ctx: store.ctx, reader: source}}
	decoder := json.NewDecoder(io.MultiReader(strings.NewReader("["), io.LimitReader(tracked, envelope.tablesEnd-envelope.tablesStart)))
	decoder.DisallowUnknownFields()
	if _, err := decoder.Token(); err != nil {
		return result, nil, err
	}
	for decoder.More() {
		var contract runnerArtifactTableContract
		if err := decoder.Decode(&contract); err != nil {
			if tracked.err != nil {
				return result, nil, tracked.err
			}
			return invalidSchema()
		}
		id := strings.ToLower(strings.TrimSpace(contract.ID))
		if !validRunnerArtifactTableContract(contract) {
			result.failures = append(result.failures, "artifact_table_contract_invalid:"+filepath.Base(name)+" reason=invalid_table")
			continue
		}
		_, prior := seen[id]
		_, local := ids[id]
		if prior || local {
			result.failures = append(result.failures, "artifact_table_contract_invalid:"+filepath.Base(name)+" reason=duplicate_id")
			continue
		}
		ids[id] = struct{}{}
		if err := result.inspectTable(store, contract, tables); err != nil {
			return result, nil, err
		}
	}
	if _, err := decoder.Token(); err != nil {
		if tracked.err != nil {
			return result, nil, tracked.err
		}
		return invalidSchema()
	}
	return result, ids, nil
}
