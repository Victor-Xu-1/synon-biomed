package server

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const sessionRunnerPublicProgressStyle = "Use one or two short sentences: state a concrete observed finding or remaining uncertainty, then the next action and its purpose. Aim for roughly 40-100 Chinese characters or 25-55 English words when there is enough information; these are style targets, not minimums. Before any evidence exists, explain the immediate scope and purpose without inventing a finding. Add information beyond the operation label, not a transcript of tools. Repeated attempts should mention only what changed; when nothing changed, do not restate the same plan. Never invent success, measurements or causes to make an update more detailed."

const sessionRunnerPublicCommunicationContract = "Assistant communication has two explicit roles. During tool work, use the public_progress field declared by the tool schema for concise user-facing explanation in the user's language. The field's minimum length indicates whether an update is due; otherwise routine operations may use an empty string. " + sessionRunnerPublicProgressStyle + " Do not expose private reasoning, internal identifiers or a final conclusion. The operation description remains the short action label in the expandable audit card. A response with no tool calls is the complete final answer and is accepted only after completion validation. Never put progress control markers or report-sized drafts in tool-round prose."

const sessionRunnerPublicProgressNarrationMaxRunes = 300
const sessionRunnerPublicProgressFingerprintLimit = 64

// sessionRunnerPublicProgressDeduper suppresses an exact public narration
// replay while the engine privately repairs a rejected tool call. It resets
// only after an accepted tool boundary advances the assistant segment, so a
// later genuine stage may reuse the same wording without losing evidence.
// Fingerprints keep the bounded repair guard independent of narration size.
type sessionRunnerPublicProgressDeduper struct {
	fingerprints [][sha256.Size]byte
	blocks       map[string][sha256.Size]byte
}

func (d *sessionRunnerPublicProgressDeduper) shouldPublish(blockID, content string) (bool, error) {
	fingerprint := sha256.Sum256([]byte(content))
	if blockID != "" {
		if previous, found := d.blocks[blockID]; found {
			if previous != fingerprint {
				return false, fmt.Errorf("public progress block %q changed within one assistant segment", blockID)
			}
			return false, nil
		}
		if d.blocks == nil {
			d.blocks = make(map[string][sha256.Size]byte)
		}
		if len(d.blocks) < sessionRunnerPublicProgressFingerprintLimit {
			d.blocks[blockID] = fingerprint
		}
	}
	for _, seen := range d.fingerprints {
		if seen == fingerprint {
			return false, nil
		}
	}
	if len(d.fingerprints) < sessionRunnerPublicProgressFingerprintLimit {
		d.fingerprints = append(d.fingerprints, fingerprint)
	}
	return true, nil
}

func (d *sessionRunnerPublicProgressDeduper) reset() {
	d.fingerprints = d.fingerprints[:0]
	clear(d.blocks)
}

var (
	sessionRunnerPrivateThinkBlockPattern = regexp.MustCompile(`(?is)<\s*think(?:ing|_[a-z0-9][a-z0-9_-]{0,127})?\s*>[\s\S]*?<\s*/\s*think(?:ing|_[a-z0-9][a-z0-9_-]{0,127})?\s*>`)
	sessionRunnerPrivateThinkTagPattern   = regexp.MustCompile(`(?i)<\s*/?\s*(?:think(?:ing|_[a-z0-9][a-z0-9_-]{0,127})?|think_never_used_[a-z0-9][a-z0-9_-]{0,127})\s*>`)
	// A single underscore is common in scientific prose (for example
	// in_vitro or analysis_report) and must remain visible. Internal runtime
	// identifiers either use the MCP double-underscore namespace, a canonical
	// transport name, or a multi-segment implementation name. Keep this filter
	// lexical and domain-neutral; it must not become a task-specific vocabulary.
	sessionRunnerInternalIdentifierPattern = regexp.MustCompile("(?i)(?:\\bmcp__[a-z0-9_-]+__+[a-z0-9_-]+\\b|\\b(?:ask_user|search_skills|manage_(?:environments|packages)|read_file|edit_file|save_artifacts|download_[a-z0-9_-]+|web_(?:fetch|search|research)|host_mcp|runner_[a-z0-9_-]+|runtime_[a-z0-9_-]+|artifact_[a-z0-9_-]+|checkpoint_[a-z0-9_-]+|session_[a-z0-9_-]+|tool_[a-z0-9_-]+|skill_[a-z0-9_-]+|frame_[a-z0-9_-]+|conversation_[a-z0-9_-]+)\\b)")
	sessionRunnerFinalStructurePattern     = regexp.MustCompile(`(?im)(?:^|\n)\s{0,3}#{1,6}\s*(?:核心)?结论\b|(?:^|\n)\s{0,3}#{1,6}\s*(?:最终结果|最终交付|任务完成|分析完成)\b|(?:^|\n)\s{0,3}#{1,6}\s*(?:final answer|final result|final deliverable|task complete|analysis complete)\b`)
)

// sessionRunnerStripPrivateThinkMarkers is the backend publication boundary
// for provider-specific hidden-thinking wrappers and orphan sentinels. The
// frontend applies the same defensive filter for historical messages, but
// new durable/public text must already be clean when it leaves the runner.
func sessionRunnerStripPrivateThinkMarkers(content string) string {
	content = sessionRunnerPrivateThinkBlockPattern.ReplaceAllString(content, "")
	return sessionRunnerPrivateThinkTagPattern.ReplaceAllString(content, "")
}

// sessionRunnerPublicProgressNarration is the single publication boundary for
// assistant text that is followed by tool work. Providers occasionally combine
// a formal-looking final answer with a save or verification call. That response
// is not terminal by protocol, so publishing it would create a false conclusion
// that later validation has to retract. Keep ordinary concise scientific
// narration, but withhold final-shaped or report-sized drafts until the runner
// produces a no-tool candidate and every completion gate accepts it.
func sessionRunnerPublicProgressNarration(content string) string {
	content = strings.TrimSpace(sessionRunnerStripPrivateThinkMarkers(content))
	if content == "" {
		return ""
	}
	normalizedControl := strings.ToLower(content)
	if strings.Contains(normalizedControl, "<|functioncallbegin|>") ||
		strings.Contains(normalizedControl, "<|functioncallend|>") ||
		strings.Contains(normalizedControl, `\u003c|functioncallbegin|`) ||
		strings.Contains(normalizedControl, `\u003c|functioncallend|`) {
		return ""
	}
	if utf8.RuneCountInString(content) > sessionRunnerPublicProgressNarrationMaxRunes ||
		strings.Contains(content, "{{artifact:") ||
		sessionRunnerProgressNarrationHasFinalStructure(content) {
		return ""
	}
	content = sessionRunnerSanitizeProgressNarrationFragments(content)
	// Orphan transport delimiters are not narration. Keep other symbol-only
	// scientific answers intact rather than requiring natural-language text.
	if strings.ContainsAny(content, "<>") && strings.Trim(content, " \t\r\n<>/|'\"\\") == "" {
		return ""
	}
	return content
}

func sessionRunnerSanitizeProgressNarrationFragments(content string) string {
	paragraphs := strings.Split(content, "\n")
	public := make([]string, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" {
			if len(public) > 0 && public[len(public)-1] != "" {
				public = append(public, "")
			}
			continue
		}
		fragments := sessionRunnerProgressNarrationFragments(paragraph)
		kept := make([]string, 0, len(fragments))
		for _, fragment := range fragments {
			if strings.TrimSpace(fragment) != "" &&
				!sessionRunnerProgressNarrationExposesProductInternals(fragment) {
				kept = append(kept, fragment)
			}
		}
		if len(kept) > 0 {
			public = append(public, strings.TrimSpace(strings.Join(kept, "")))
		}
	}
	return strings.TrimSpace(strings.Join(public, "\n"))
}

func sessionRunnerProgressNarrationFragments(paragraph string) []string {
	fragments := make([]string, 0, 4)
	start := 0
	for index, character := range paragraph {
		if character != '。' && character != '！' && character != '？' &&
			character != '.' && character != '!' && character != '?' {
			continue
		}
		end := index + utf8.RuneLen(character)
		if character == '.' || character == '!' || character == '?' {
			if end < len(paragraph) {
				next, _ := utf8.DecodeRuneInString(paragraph[end:])
				if !unicode.IsSpace(next) {
					continue
				}
			}
		}
		fragments = append(fragments, paragraph[start:end])
		start = end
	}
	if start < len(paragraph) {
		fragments = append(fragments, paragraph[start:])
	}
	if len(fragments) == 0 {
		return []string{paragraph}
	}
	return fragments
}

func sessionRunnerProgressNarrationExposesProductInternals(content string) bool {
	normalized := strings.ToLower(strings.Join(strings.Fields(content), " "))
	for _, marker := range []string{
		"工具协议", "工具调用协议", "有界执行单元", "内部约束", "系统提示词", "提示词约束",
		"tool protocol", "bounded execution unit", "runtime correction", "system prompt", "prompt contract",
		"internal constraint", "harness",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return sessionRunnerInternalIdentifierPattern.MatchString(content)
}

func sessionRunnerProgressNarrationHasFinalStructure(content string) bool {
	normalized := strings.ToLower(strings.Join(strings.Fields(content), " "))
	if sessionRunnerFinalStructurePattern.MatchString(content) {
		return true
	}
	for _, marker := range []string{
		"核心结论:", "核心结论：", "结论如下", "最终结果:", "最终结果：", "最终交付",
		"任务已完成", "分析已完成", "计算已完成", "报告已生成",
		"conclusion:", "final answer", "final result", "final deliverable", "task complete", "analysis is complete", "report generated",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func appendSessionRunnerPublicCommunicationContract(messages []chatCompletionMessage) []chatCompletionMessage {
	return appendRuntimeTerminalPolicyContextMessage(messages, sessionRunnerPublicCommunicationContract)
}
