package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type agentWorkspaceJSONContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r agentWorkspaceJSONContextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

// RFC 6901 paths address data, not workspace paths. Decode escapes exactly
// once; percent escapes belong to URI fragments and are not interpreted here.
func agentWorkspaceJSONPointerTokens(pointer string) ([]string, error) {
	if pointer == "" {
		return nil, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, errors.New("json_pointer must be empty or start with /")
	}
	parts := strings.Split(pointer[1:], "/")
	for i, part := range parts {
		var decoded strings.Builder
		for j := 0; j < len(part); j++ {
			if part[j] != '~' {
				decoded.WriteByte(part[j])
				continue
			}
			j++
			if j >= len(part) || (part[j] != '0' && part[j] != '1') {
				return nil, errors.New("json_pointer contains an invalid escape")
			}
			if part[j] == '0' {
				decoded.WriteByte('~')
			} else {
				decoded.WriteByte('/')
			}
		}
		parts[i] = decoded.String()
	}
	return parts, nil
}

// Traverse unselected containers token by token rather than building the
// entire document tree. Validate the complete JSON envelope, including the
// suffix after the selected value, so a corrupt source cannot appear valid.
func selectAgentWorkspaceJSONValue(ctx context.Context, reader io.Reader, pointer string) (json.RawMessage, error) {
	parts, err := agentWorkspaceJSONPointerTokens(pointer)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(agentWorkspaceJSONContextReader{ctx: ctx, reader: reader})
	decoder.UseNumber()
	var walk func([]string, bool, int) (json.RawMessage, bool, error)
	walk = func(path []string, selected bool, depth int) (json.RawMessage, bool, error) {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		// Match encoding/json's nesting bound. Token-wise skipping must not
		// bypass the decoder's structural protection for untrusted documents.
		if depth > 10000 {
			return nil, false, errors.New("JSON nesting exceeds the decoder limit")
		}
		if selected && len(path) == 0 {
			var value json.RawMessage
			err := decoder.Decode(&value)
			return value, err == nil, err
		}
		token, err := decoder.Token()
		if err != nil {
			return nil, false, err
		}
		delimiter, container := token.(json.Delim)
		if !container {
			return nil, false, nil
		}
		if delimiter != '{' && delimiter != '[' {
			return nil, false, errors.New("invalid JSON container")
		}
		var value json.RawMessage
		found := false
		matched := false
		arrayIndex := 0
		for decoder.More() {
			key := strconv.Itoa(arrayIndex)
			if delimiter == '{' {
				token, err = decoder.Token()
				if err != nil {
					return nil, false, err
				}
				var ok bool
				key, ok = token.(string)
				if !ok {
					return nil, false, errors.New("invalid JSON object key")
				}
			}
			match := selected && key == path[0]
			if match && matched {
				return nil, false, errors.New("json_pointer is ambiguous because its object key is duplicated")
			}
			matched = matched || match
			childPath := []string(nil)
			if match {
				childPath = path[1:]
			}
			child, childFound, err := walk(childPath, match, depth+1)
			if err != nil {
				return nil, false, err
			}
			if childFound {
				value, found = child, true
			}
			arrayIndex++
		}
		if _, err := decoder.Token(); err != nil {
			return nil, false, err
		}
		return value, found, nil
	}
	value, found, err := walk(parts, true, 0)
	if err != nil {
		return nil, fmt.Errorf("read JSON selection: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return nil, fmt.Errorf("read JSON selection: %w", err)
	}
	if !found {
		return nil, errors.New("json_pointer does not identify a value in this document")
	}
	return value, nil
}
