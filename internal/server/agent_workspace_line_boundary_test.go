package server

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestWorkspaceBoundedLinePreservesUTF8AndLinePosition(t *testing.T) {
	for _, ending := range []string{"", "\nnext\n"} {
		for _, source := range []string{"Aθ中🧬末尾", strings.Repeat("a", 4095) + "中🧬末尾"} {
			for limit := max(1, len(source)-15); limit < len(source); limit++ {
				reader := bufio.NewReaderSize(strings.NewReader(source+ending), 16)
				got, truncated, err := readAgentWorkspaceBoundedLine(reader, limit)
				if (err != nil && !errors.Is(err, io.EOF)) || !truncated || !utf8.ValidString(got) || len(got) > limit || !strings.HasPrefix(source, got) {
					t.Fatalf("limit=%d preview=%q truncated=%t err=%v", limit, got, truncated, err)
				}
				if ending != "" {
					next, truncated, err := readAgentWorkspaceBoundedLine(reader, 16)
					if next != "next" || truncated || err != nil {
						t.Fatalf("line cursor lost: %q %t %v", next, truncated, err)
					}
				}
			}
		}
	}
	reader := bufio.NewReader(strings.NewReader("a\xff" + strings.Repeat("中", 100)))
	got, _, _ := readAgentWorkspaceBoundedLine(reader, 6)
	if utf8.ValidString(got) {
		t.Fatal("corrupt interior must not silently become valid text")
	}
}

func TestWorkspaceLongUnicodeLineRemainsReadable(t *testing.T) {
	raw := []byte(strings.Repeat("中", agentWorkspaceReadMaxBytes) + "\nNEXT_LINE\n")
	value, err := readAgentWorkspaceFile(context.Background(), bytes.NewReader(raw), "source.txt", "text/plain", int64(len(raw)), map[string]any{"offset": 1, "limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	result := mapValue(value)
	if boolValue(result["_policy_skip"], false) || stringValue(result["content"]) == "" || numberValue(result["truncated_lines"]) != 1 {
		t.Fatalf("long UTF-8 line lost: %#v", result)
	}
}

func TestWorkspaceFirstLongLineRetainsContentAndContinuationWithinTransportBudget(t *testing.T) {
	raw := []byte(strings.Repeat("\"中🧬", 20000) + "\nNEXT_LINE\n")
	ctx := withAgentWorkspaceReadBudget(context.Background(), 16<<10)
	value, err := readAgentWorkspaceFile(ctx, bytes.NewReader(raw), "source.txt", "text/plain", int64(len(raw)), map[string]any{"offset": 1, "limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	result := mapValue(value)
	if stringValue(result["content"]) == "" || numberValue(result["next_offset"]) != 2 || numberValue(result["truncated_lines"]) != 1 || !agentWorkspaceReadResultFits(ctx, result) {
		t.Fatalf("first long line lost its bounded content or continuation: %#v", result)
	}
	next, err := readAgentWorkspaceFile(ctx, bytes.NewReader(raw), "source.txt", "text/plain", int64(len(raw)), map[string]any{"offset": 2, "limit": 1})
	if err != nil || !strings.Contains(stringValue(mapValue(next)["content"]), "NEXT_LINE") {
		t.Fatalf("next line unreachable: %#v %v", next, err)
	}
}
