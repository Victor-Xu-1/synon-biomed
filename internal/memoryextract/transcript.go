package memoryextract

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf16"

	"synon-go/internal/memorypolicy"
	"synon-go/internal/memorytools"
)

var serverToolStubLine = regexp.MustCompile(`(?m)^  - (\S+)(?: — (.*))?$`)

const extractionSessionContinued = "[session continued]"
const extractionDiskImageElided = "[image elided for extraction pass]"

// ProjectCompactMessages reproduces the extraction-only message
// projection after rolling compaction has marked obsolete rows as ignored. It
// never mutates the runner transcript: local image references become an opaque
// text notice, ignored rows are dropped, and an assistant-leading delta is
// anchored with the source sentinel expected by compact extractors.
func ProjectCompactMessages(messages []Message) []Message {
	projected := make([]Message, 0, len(messages)+1)
	for _, message := range messages {
		if message.Ignore {
			continue
		}
		copyMessage := message
		copyMessage.Content = append([]Block(nil), message.Content...)
		for index := range copyMessage.Content {
			block := &copyMessage.Content[index]
			if block.Type == BlockImage && block.DiskReference {
				*block = Block{Type: BlockText, Text: extractionDiskImageElided}
			}
		}
		projected = append(projected, copyMessage)
	}
	if len(projected) > 0 && projected[0].Role == "assistant" {
		projected = append([]Message{{
			Role: "user", Content: []Block{{Type: BlockText, Text: extractionSessionContinued}},
		}}, projected...)
	}
	return projected
}

func HasUserProseSince(messages []Message) bool {
	for _, message := range messages {
		if message.Ignore || message.Role != "user" {
			continue
		}
		for _, block := range message.Content {
			if block.Type != BlockText || strings.HasPrefix(block.Text, "[System]") || strings.HasPrefix(block.Text, "[Memory]") {
				continue
			}
			if containsSubstantiveUserProse(block.Text) {
				return true
			}
		}
	}
	return false
}

func containsSubstantiveUserProse(text string) bool {
	if len(strings.Fields(text)) >= 3 {
		return true
	}
	// CJK prose does not normally contain whitespace between words. Requiring
	// English-style token boundaries silently disables automatic memory for
	// Chinese and Japanese conversations, so accept a short run of Han letters
	// while keeping one- or two-character UI fragments below the gate.
	hanLetters := 0
	for _, value := range text {
		if unicode.In(value, unicode.Han) {
			hanLetters++
		}
	}
	return hanLetters >= 3
}

func HasMemoryWritesSince(messages []Message, extractMaxPerRun int) bool {
	failedWrites := make(map[string]struct{})
	for _, message := range messages {
		if message.Role != "user" {
			continue
		}
		for _, block := range message.Content {
			if block.Type == BlockToolResult && block.ToolError && isWholeBatchMemoryReject(block.Text) {
				failedWrites[block.ToolUseID] = struct{}{}
			}
		}
	}

	durableAppendCalls := 0
	for _, message := range messages {
		if message.Role != "assistant" {
			continue
		}
		for _, block := range message.Content {
			if block.Type != BlockToolUse {
				continue
			}
			switch block.ToolName {
			case "write_memory":
				if _, failed := failedWrites[block.ToolUseID]; failed {
					continue
				}
				if arrayLength(block.ToolInput["replace"]) > 0 || arrayLength(block.ToolInput["remove"]) > 0 {
					return true
				}
				entity := strings.TrimSpace(stringValue(block.ToolInput["entity"]))
				if entity != "frame" && !strings.HasPrefix(entity, "frame:") {
					durableAppendCalls++
					if durableAppendCalls >= extractMaxPerRun {
						return true
					}
				}
			case "compute_details":
				if stringValue(block.ToolInput["mode"]) != "read" {
					return true
				}
			}
		}
	}
	return false
}

func DigestTranscript(messages []Message, maxUTF16Units int) string {
	if maxUTF16Units <= 0 {
		maxUTF16Units = memorypolicy.ExtractionTranscriptMaxUTF16Units
	}
	lines := make([]string, 0)
	for _, message := range messages {
		if message.Ignore {
			continue
		}
		for _, block := range message.Content {
			switch block.Type {
			case BlockToolUse:
				lines = append(lines, message.Role+" [tool_use "+block.ToolName+"]")
			case BlockToolResult:
				continue
			case BlockText:
				if strings.HasPrefix(block.Text, "[Memory]") || block.HarnessNotice {
					continue
				}
				lines = append(lines, message.Role+": "+block.Text)
			}
		}
	}
	digest := strings.Join(lines, "\n---\n")
	if memorypolicy.UTF16Length(digest) <= maxUTF16Units {
		return digest
	}
	return "…" + suffixUTF16(digest, maxUTF16Units)
}

func CollectServerToolStubBodies(messages []Message) []string {
	result := make([]string, 0)
	for _, message := range messages {
		if message.Role != "user" {
			continue
		}
		for _, block := range message.Content {
			if block.Type != BlockText || !isServerToolStub(block.Text) {
				continue
			}
			result = append(result, block.Text)
			for _, match := range serverToolStubLine.FindAllStringSubmatch(block.Text, -1) {
				if match[1] != "" {
					result = append(result, match[1])
				}
				if match[2] != "" {
					result = append(result, match[2])
				}
			}
		}
	}
	return result
}

func isWholeBatchMemoryReject(value string) bool {
	return strings.Contains(value, memorytools.ErrMemoryWriteRejected.Error()) ||
		strings.Contains(value, memorytools.ErrMemoryClassifierUnavailable.Error())
}

func isServerToolStub(value string) bool {
	return strings.HasPrefix(value, "[System] Prior-turn ") && strings.Contains(value, "<persisted-output>\n")
}

func suffixUTF16(value string, maxUnits int) string {
	if maxUnits <= 0 {
		return ""
	}
	runes := []rune(value)
	units := 0
	start := len(runes)
	for start > 0 {
		width := utf16.RuneLen(runes[start-1])
		if width < 0 {
			width = 1
		}
		if units+width > maxUnits {
			break
		}
		units += width
		start--
	}
	return string(runes[start:])
}
