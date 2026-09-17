package server

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxWebConversationToolDetailDepth = 64
const maxWebConversationToolDetailScalarBytes = 16 << 10
const maxWebConversationToolDetailKeyBytes = 128

type webConversationToolDetailNode struct {
	Path          string `json:"path"`
	Kind          string `json:"kind"`
	Key           string `json:"key,omitempty"`
	Index         *int   `json:"index,omitempty"`
	Value         any    `json:"value,omitempty"`
	Total         int    `json:"total,omitempty"`
	Truncated     bool   `json:"truncated,omitempty"`
	OriginalBytes int    `json:"original_bytes,omitempty"`
}

type webConversationToolDetailPage struct {
	Kind           string
	Total          int
	Items          []webConversationToolDetailNode
	NextByteOffset int64
}

func readWebConversationToolDetailPage(
	reader io.Reader,
	segments []string,
	path string,
	offset, limit int,
) (webConversationToolDetailPage, error) {
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	return readWebConversationToolDetailPath(decoder, segments, path, offset, limit)
}

func readWebConversationToolDetailContinuation(
	reader io.ReadSeeker,
	path string,
	cursor webConversationToolDetailCursor,
	limit int,
) (webConversationToolDetailPage, error) {
	continuation, absoluteBase, err := webConversationToolDetailContinuationReader(reader, cursor)
	if err != nil {
		return webConversationToolDetailPage{}, err
	}
	decoder := json.NewDecoder(continuation)
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return webConversationToolDetailPage{}, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || (cursor.Kind == "array" && delimiter != '[') || (cursor.Kind == "object" && delimiter != '{') {
		return webConversationToolDetailPage{}, errors.New("tool detail continuation kind changed")
	}
	page := webConversationToolDetailPage{Kind: cursor.Kind, Total: cursor.Total, Items: []webConversationToolDetailNode{}}
	expected := min(limit, cursor.Total-cursor.Offset)
	switch cursor.Kind {
	case "array":
		for len(page.Items) < expected && decoder.More() {
			index := cursor.Offset + len(page.Items)
			node, err := readWebConversationToolDetailNode(
				decoder, appendWebConversationToolDetailPath(path, strconv.Itoa(index)),
			)
			if err != nil {
				return webConversationToolDetailPage{}, err
			}
			node.Index = &index
			page.Items = append(page.Items, node)
			page.NextByteOffset = absoluteBase + decoder.InputOffset()
		}
	case "object":
		for len(page.Items) < expected && decoder.More() {
			key, err := readWebConversationToolDetailObjectKey(decoder)
			if err != nil {
				return webConversationToolDetailPage{}, err
			}
			node, err := readWebConversationToolDetailNode(
				decoder, appendWebConversationToolDetailPath(path, escapeWebConversationToolDetailPath(key)),
			)
			if err != nil {
				return webConversationToolDetailPage{}, err
			}
			node.Key = key
			page.Items = append(page.Items, node)
			page.NextByteOffset = absoluteBase + decoder.InputOffset()
		}
	default:
		return webConversationToolDetailPage{}, errors.New("invalid tool detail continuation kind")
	}
	if len(page.Items) != expected {
		return webConversationToolDetailPage{}, errors.New("tool detail continuation ended before its cursor total")
	}
	if cursor.Offset+len(page.Items) == cursor.Total {
		if _, err := decoder.Token(); err != nil {
			return webConversationToolDetailPage{}, err
		}
	}
	return page, nil
}

func webConversationToolDetailContinuationReader(
	reader io.ReadSeeker,
	cursor webConversationToolDetailCursor,
) (io.Reader, int64, error) {
	if _, err := reader.Seek(cursor.ByteOffset, io.SeekStart); err != nil {
		return nil, 0, err
	}
	position := cursor.ByteOffset
	var buffer [1]byte
	for {
		count, err := reader.Read(buffer[:])
		if err != nil {
			return nil, 0, err
		}
		if count != 1 {
			return nil, 0, errors.New("tool detail continuation is unavailable")
		}
		position++
		if buffer[0] == ',' || buffer[0] == ' ' || buffer[0] == '\t' || buffer[0] == '\r' || buffer[0] == '\n' {
			continue
		}
		if _, err := reader.Seek(-1, io.SeekCurrent); err != nil {
			return nil, 0, err
		}
		position--
		break
	}
	opener := "["
	if cursor.Kind == "object" {
		opener = "{"
	}
	return io.MultiReader(strings.NewReader(opener), reader), position - 1, nil
}

func readWebConversationToolDetailPath(
	decoder *json.Decoder,
	segments []string,
	path string,
	offset, limit int,
) (webConversationToolDetailPage, error) {
	token, err := decoder.Token()
	if err != nil {
		return webConversationToolDetailPage{}, err
	}
	if len(segments) == 0 {
		return pageWebConversationToolDetailToken(decoder, token, path, offset, limit)
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return webConversationToolDetailPage{}, errWebConversationToolDetailPathNotFound
	}
	switch delimiter {
	case '{':
		for decoder.More() {
			key, tokenErr := readWebConversationToolDetailObjectKey(decoder)
			if tokenErr != nil {
				return webConversationToolDetailPage{}, tokenErr
			}
			if key == segments[0] {
				return readWebConversationToolDetailPath(decoder, segments[1:], path, offset, limit)
			}
			if err := skipWebConversationToolDetailValue(decoder); err != nil {
				return webConversationToolDetailPage{}, err
			}
		}
	case '[':
		index, parseErr := strconv.Atoi(segments[0])
		if parseErr != nil || index < 0 {
			return webConversationToolDetailPage{}, errWebConversationToolDetailPathNotFound
		}
		for current := 0; decoder.More(); current++ {
			if current == index {
				return readWebConversationToolDetailPath(decoder, segments[1:], path, offset, limit)
			}
			if err := skipWebConversationToolDetailValue(decoder); err != nil {
				return webConversationToolDetailPage{}, err
			}
		}
	}
	return webConversationToolDetailPage{}, errWebConversationToolDetailPathNotFound
}

func pageWebConversationToolDetailToken(
	decoder *json.Decoder,
	token any,
	path string,
	offset, limit int,
) (webConversationToolDetailPage, error) {
	delimiter, container := token.(json.Delim)
	if !container {
		if offset > 0 {
			return webConversationToolDetailPage{Kind: "scalar", Total: 1, Items: []webConversationToolDetailNode{}}, nil
		}
		return webConversationToolDetailPage{
			Kind: "scalar", Total: 1, Items: []webConversationToolDetailNode{webConversationToolDetailScalarNode(path, token)},
		}, nil
	}
	page := webConversationToolDetailPage{Items: []webConversationToolDetailNode{}}
	switch delimiter {
	case '[':
		page.Kind = "array"
		for index := 0; decoder.More(); index++ {
			if index >= offset && len(page.Items) < limit {
				node, err := readWebConversationToolDetailNode(decoder, appendWebConversationToolDetailPath(path, strconv.Itoa(index)))
				if err != nil {
					return webConversationToolDetailPage{}, err
				}
				itemIndex := index
				node.Index = &itemIndex
				page.Items = append(page.Items, node)
				if len(page.Items) == limit {
					page.NextByteOffset = decoder.InputOffset()
				}
			} else if err := skipWebConversationToolDetailValue(decoder); err != nil {
				return webConversationToolDetailPage{}, err
			}
			page.Total++
		}
	case '{':
		page.Kind = "object"
		seenKeys := map[string]struct{}{}
		for decoder.More() {
			key, err := readWebConversationToolDetailObjectKey(decoder)
			if err != nil {
				return webConversationToolDetailPage{}, err
			}
			if _, duplicate := seenKeys[key]; duplicate {
				return webConversationToolDetailPage{}, errors.New("duplicate object key")
			}
			seenKeys[key] = struct{}{}
			if page.Total >= offset && len(page.Items) < limit {
				node, err := readWebConversationToolDetailNode(decoder, appendWebConversationToolDetailPath(path, escapeWebConversationToolDetailPath(key)))
				if err != nil {
					return webConversationToolDetailPage{}, err
				}
				node.Key = key
				page.Items = append(page.Items, node)
				if len(page.Items) == limit {
					page.NextByteOffset = decoder.InputOffset()
				}
			} else if err := skipWebConversationToolDetailValue(decoder); err != nil {
				return webConversationToolDetailPage{}, err
			}
			page.Total++
		}
	default:
		return webConversationToolDetailPage{}, errors.New("invalid JSON delimiter")
	}
	if _, err := decoder.Token(); err != nil {
		return webConversationToolDetailPage{}, err
	}
	return page, nil
}

func readWebConversationToolDetailNode(decoder *json.Decoder, path string) (webConversationToolDetailNode, error) {
	return readWebConversationToolDetailNodeAtDepth(decoder, path, 0)
}

func readWebConversationToolDetailNodeAtDepth(
	decoder *json.Decoder,
	path string,
	depth int,
) (webConversationToolDetailNode, error) {
	if depth > maxWebConversationToolDetailDepth {
		return webConversationToolDetailNode{}, errors.New("tool detail nesting exceeds the supported depth")
	}
	token, err := decoder.Token()
	if err != nil {
		return webConversationToolDetailNode{}, err
	}
	delimiter, container := token.(json.Delim)
	if !container {
		return webConversationToolDetailScalarNode(path, token), nil
	}
	node := webConversationToolDetailNode{Path: path}
	switch delimiter {
	case '[':
		node.Kind = "array"
		for decoder.More() {
			if err := skipWebConversationToolDetailValueAtDepth(decoder, depth+1); err != nil {
				return webConversationToolDetailNode{}, err
			}
			node.Total++
		}
	case '{':
		node.Kind = "object"
		seenKeys := map[string]struct{}{}
		for decoder.More() {
			key, err := readWebConversationToolDetailObjectKey(decoder)
			if err != nil {
				return webConversationToolDetailNode{}, err
			}
			if _, duplicate := seenKeys[key]; duplicate {
				return webConversationToolDetailNode{}, errors.New("duplicate object key")
			}
			seenKeys[key] = struct{}{}
			if err := skipWebConversationToolDetailValueAtDepth(decoder, depth+1); err != nil {
				return webConversationToolDetailNode{}, err
			}
			node.Total++
		}
	default:
		return webConversationToolDetailNode{}, errors.New("invalid JSON delimiter")
	}
	_, err = decoder.Token()
	return node, err
}

func webConversationToolDetailScalarNode(path string, value any) webConversationToolDetailNode {
	text, boundedText := value.(string)
	if number, ok := value.(json.Number); ok && !webConversationToolDetailJSONNumberSafe(number) {
		text, boundedText = number.String(), true
		value = text
	}
	if !boundedText || len(text) <= maxWebConversationToolDetailScalarBytes {
		return webConversationToolDetailNode{Path: path, Kind: "scalar", Value: value}
	}
	end := maxWebConversationToolDetailScalarBytes
	for end > 0 && !utf8.ValidString(text[:end]) {
		end--
	}
	return webConversationToolDetailNode{
		Path: path, Kind: "scalar", Value: text[:end] + "…", Truncated: true, OriginalBytes: len(text),
	}
}

func webConversationToolDetailJSONNumberSafe(value json.Number) bool {
	raw := value.String()
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsInf(parsed, 0) || math.IsNaN(parsed) {
		return false
	}
	if strings.ContainsAny(raw, ".eE") {
		return true
	}
	integer, err := strconv.ParseInt(raw, 10, 64)
	return err == nil && integer >= -9_007_199_254_740_991 && integer <= 9_007_199_254_740_991
}

func skipWebConversationToolDetailValue(decoder *json.Decoder) error {
	return skipWebConversationToolDetailValueAtDepth(decoder, 0)
}

func skipWebConversationToolDetailValueAtDepth(decoder *json.Decoder, depth int) error {
	if depth > maxWebConversationToolDetailDepth {
		return errors.New("tool detail nesting exceeds the supported depth")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, container := token.(json.Delim)
	if !container {
		return nil
	}
	if delimiter != '[' && delimiter != '{' {
		return errors.New("invalid JSON delimiter")
	}
	for decoder.More() {
		if delimiter == '{' {
			if _, err := readWebConversationToolDetailObjectKey(decoder); err != nil {
				return err
			}
		}
		if err := skipWebConversationToolDetailValueAtDepth(decoder, depth+1); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

func readWebConversationToolDetailObjectKey(decoder *json.Decoder) (string, error) {
	token, err := decoder.Token()
	if err != nil {
		return "", err
	}
	key, ok := token.(string)
	if !ok || len(escapeWebConversationToolDetailPath(key)) > maxWebConversationToolDetailKeyBytes {
		return "", errors.New("invalid object key")
	}
	for _, character := range key {
		if character < 0x20 || character == 0x7f {
			return "", errors.New("invalid object key")
		}
	}
	return key, nil
}
