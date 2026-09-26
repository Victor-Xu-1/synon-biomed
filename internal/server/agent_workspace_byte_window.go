package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

func agentWorkspaceByteOffset(raw any) (int, bool) {
	switch value := raw.(type) {
	case int:
		if value == 0 {
			return 0, true
		}
	case int64:
		if value == 0 {
			return 0, true
		}
	case float64:
		if value == 0 {
			return 0, true
		}
	case json.Number:
		if value == "0" {
			return 0, true
		}
	}
	return strictPositiveAgentWorkspaceInteger(raw)
}

func validateAgentWorkspaceByteWindow(input map[string]any) error {
	offset, hasOffset := input["byte_offset"]
	limit, hasLimit := input["byte_limit"]
	if !hasOffset && !hasLimit {
		return nil
	}
	if _, valid := agentWorkspaceByteOffset(offset); !hasOffset || !valid {
		return errors.New("read_file.byte_offset must be a non-negative integer")
	}
	if hasLimit {
		if _, valid := strictPositiveAgentWorkspaceInteger(limit); !valid {
			return errors.New("read_file.byte_limit must be a positive integer")
		}
	}
	for _, key := range []string{"offset", "limit", "json_pointer", "pages", "recovery_condition_id"} {
		if _, present := input[key]; present {
			return errors.New("read_file byte windows cannot be combined with line, JSON, PDF or recovery-condition selectors")
		}
	}
	return nil
}

func readAgentWorkspaceByteWindow(ctx context.Context, reader io.ReadSeeker, filename, contentType string, size int64, input map[string]any) (map[string]any, error) {
	if err := validateAgentWorkspaceByteWindow(input); err != nil {
		return nil, err
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	offset, _ := agentWorkspaceByteOffset(input["byte_offset"])
	if int64(offset) > size || size < 0 {
		return nil, errors.New("read_file.byte_offset exceeds the source size")
	}
	limit := agentWorkspaceReadMaxBytes
	if raw, found := input["byte_limit"]; found {
		requested, _ := strictPositiveAgentWorkspaceInteger(raw)
		limit = min(limit, requested)
	}
	limit = int(min(int64(limit), size-int64(offset)))
	if _, err := reader.Seek(int64(offset), io.SeekStart); err != nil {
		return nil, err
	}
	raw := make([]byte, limit)
	if _, err := io.ReadFull(agentWorkspaceJSONContextReader{ctx: ctx, reader: reader}, raw); err != nil {
		return nil, err
	}
	makeResult := func(length int) map[string]any {
		page := raw[:length]
		digest := sha256.Sum256(page)
		result := map[string]any{"filename": filename, "content_type": contentType, "size_bytes": size,
			"byte_offset": offset, "bytes_read": length, "page_sha256": hex.EncodeToString(digest[:]),
			"eof": int64(offset)+int64(length) == size, "snapshot_consistency": "mutable_path_no_snapshot"}
		if stringValue(input["version_id"]) != "" {
			result["snapshot_consistency"] = "immutable_version"
		}
		if utf8.Valid(page) {
			result["encoding"], result["content"] = "utf-8", string(page)
		} else {
			result["encoding"], result["content"] = "base64", base64.StdEncoding.EncodeToString(page)
		}
		if result["eof"] != true {
			result["next_byte_offset"] = offset + length
		}
		return result
	}
	result := makeResult(limit)
	for !agentWorkspaceReadResultFits(ctx, result) {
		if limit <= 1 {
			return nil, errors.New("read_file output budget cannot fit a byte window")
		}
		limit /= 2
		result = makeResult(limit)
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	return result, nil
}
