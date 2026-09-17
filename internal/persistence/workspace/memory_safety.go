package workspace

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"synon-go/internal/memorypolicy"
)

var ErrMemoryPromptInjection = errors.New("Memory write rejected: content flagged as potential prompt injection")

type MemoryPromptInjectionError struct {
	Pattern string
}

func (e *MemoryPromptInjectionError) Error() string {
	return fmt.Sprintf("%s (exfil: %s)", ErrMemoryPromptInjection, e.Pattern)
}

func (e *MemoryPromptInjectionError) Unwrap() error { return ErrMemoryPromptInjection }

const (
	memoryMarkdownLabelPattern = `(?:\\.|\[[^\]]*\]|[^\[\]\\])`
	memoryAbsoluteURLPattern   = `[\s\x00-\x1f]*(?:h[\t\n\r]*t[\t\n\r]*t[\t\n\r]*p[\t\n\r]*s?[\t\n\r]*:[\t\n\r]*)?[\\/][\t\n\r]*[\\/]`
)

var (
	memoryTagPattern        = regexp.MustCompile(`(?i)</?memory[_a-z]*\b[^>]*>`)
	memoryRolePrefixPattern = regexp.MustCompile(`(?im)^\s*\[(system|memory)\]\s*`)
	memoryHeadingPattern    = regexp.MustCompile(`(?m)^#+\s+`)
	memoryBlankLinesPattern = regexp.MustCompile(`\n{3,}`)
	memoryLineBreaksPattern = regexp.MustCompile(`\s*[\n\v\f\r\x{0085}\x{2028}\x{2029}]+\s*`)

	memoryMarkdownImage      = regexp.MustCompile(`!\[` + memoryMarkdownLabelPattern + `*\]\([^)]+\)`)
	memoryMarkdownImageRef   = regexp.MustCompile(`!\[` + memoryMarkdownLabelPattern + `*\]\[[^\]]*\]`)
	memoryMarkdownLongLink   = regexp.MustCompile(`\[` + memoryMarkdownLabelPattern + `+\]\((\S{200,})\)`)
	memoryURLPayload         = regexp.MustCompile(`(?i)https?://\S*?[?/=#][A-Za-z0-9+_-]{80,}={0,2}`)
	memoryBareLongURL        = regexp.MustCompile(`(?i)https?://\S{150,}`)
	memoryCSSURL             = regexp.MustCompile(`(?i)\burl\(\s*['"]?` + memoryAbsoluteURLPattern)
	memoryCSSImport          = regexp.MustCompile(`(?i)@import\s*(?:/\*[^*]*\*+(?:[^/*][^*]*\*+)*/\s*)*['"]` + memoryAbsoluteURLPattern)
	memoryMetaRefresh        = regexp.MustCompile(`(?i)\bhttp-equiv\s*=\s*['"]?refresh\b`)
	memoryHTMLFetchTag       = regexp.MustCompile(`(?i)<(?:img|image|feimage|iframe|video|audio|source|embed|object|input|link|track|script|meta|body|frame|use|td|th|table)\b`)
	memoryHTMLFetchAttribute = regexp.MustCompile(`(?i)\b(?:src|srcset|imagesrcset|href|data|poster|background)\s*=\s*['"]?` + memoryAbsoluteURLPattern + `|\b(?:srcset|imagesrcset)\s*=\s*(?:"[^"]*?|'[^']*?'|[^\s>]*?),` + memoryAbsoluteURLPattern)
	memoryMarkdownRefURL     = regexp.MustCompile(`(?i)\]:\s*<?(?:https?:)?//`)
	memoryMarkdownInlineURL  = regexp.MustCompile(`(?i)\]\(\s*<?(?:https?:)?//`)
	memoryCSSStringAbsolute  = regexp.MustCompile(`(?i)^['"]` + memoryAbsoluteURLPattern)
	memoryCSSArgumentEnd     = regexp.MustCompile(`(?i)^(?:,|\)|(?:type|var|env|attr|calc|min|max|clamp|round|mod|rem|sin|cos|tan|asin|acos|atan|atan2|pow|sqrt|hypot|log|exp|abs|sign)\(|[+-]?(?:\d|\.\d))`)
	memoryBackslashPunct     = regexp.MustCompile(`\\([#$%&*+,./:;=?@\\^_` + "`" + `{|}~-])`)
	memoryHTMLEntity         = regexp.MustCompile(`(?i)&#x0*[0-9a-f]{1,6};?|&#0*[0-9]{1,7};?|&[a-zA-Z]{2,10};?`)
	memoryCSSEscape          = regexp.MustCompile(`\\(?:[0-9a-fA-F]{1,6}(?:\r\n|[ \t\n\r\f])?|[g-zG-Z \t\x00-\x08\x0b\x0e-\x1f])`)

	memorySecretPatterns = []struct {
		name    string
		pattern *regexp.Regexp
	}{
		{name: "ANTHROPIC_API_KEY", pattern: regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`)},
		{name: "GITHUB_TOKEN", pattern: regexp.MustCompile(`gh[ps]_[A-Za-z0-9]{36}`)},
		{name: "AWS_ACCESS_KEY_ID", pattern: regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
		{name: "NVIDIA_API_KEY", pattern: regexp.MustCompile(`nvapi-[A-Za-z0-9_-]{20,}`)},
	}
)

// PrepareUserMemoryBody mirrors the reference sanitizeBodyForPersist contract:
// redact credentials, cap the persisted value to 1000 UTF-16 units, then run
// the static exfil gate. The public API validates the raw Zod string first.
func PrepareUserMemoryBody(value string) (string, error) {
	if value == "" {
		return "", errors.New("memory text must be non-empty")
	}
	value = redactMemoryCredentials(value)
	value = memorypolicy.TruncateWithEllipsis(value, memorypolicy.TextMaxUTF16Units)
	if pattern := FindMemoryExfilPattern(value); pattern != "" {
		return "", &MemoryPromptInjectionError{Pattern: pattern}
	}
	return value, nil
}

// PrepareAgentMemoryBody mirrors sanitizeBodyForPersist(text, 1000): trim is
// performed by the caller, known credentials are redacted, and overlong tool
// input is truncated with an ellipsis instead of rejecting the whole call.
func PrepareAgentMemoryBody(value string) (string, bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false, errors.New("memory text must be non-empty")
	}
	value = redactMemoryCredentials(value)
	truncated := memorypolicy.UTF16Length(value) > memorypolicy.TextMaxUTF16Units
	return memorypolicy.TruncateWithEllipsis(value, memorypolicy.TextMaxUTF16Units), truncated, nil
}

func redactMemoryCredentials(value string) string {
	for _, secret := range memorySecretPatterns {
		value = secret.pattern.ReplaceAllString(value, "[REDACTED:"+secret.name+"]")
	}
	return value
}

func RedactMemoryCredentials(value string) string {
	return redactMemoryCredentials(value)
}

func FindMemoryExfilPattern(value string) string {
	current := value
	transforms := []func(string) string{
		sanitizeMemoryBodyForClassifier,
		decodeMemoryHTMLEntities,
		func(input string) string { return memoryBackslashPunct.ReplaceAllString(input, "$1") },
		decodeMemoryCSSEscapes,
	}
	for iteration := 0; iteration < memorypolicy.StaticDecodeMaxPasses; iteration++ {
		if pattern := directMemoryExfilPattern(current); pattern != "" {
			return pattern
		}
		changed := false
		for _, transform := range transforms {
			next := transform(current)
			if next == current {
				continue
			}
			current = next
			changed = true
			if pattern := directMemoryExfilPattern(current); pattern != "" {
				return pattern
			}
		}
		if !changed {
			break
		}
	}
	return directMemoryExfilPattern(current)
}

func directMemoryExfilPattern(value string) string {
	checks := []struct {
		name    string
		pattern *regexp.Regexp
	}{
		{name: "markdown_image", pattern: memoryMarkdownImage},
		{name: "markdown_image_ref", pattern: memoryMarkdownImageRef},
		{name: "markdown_link_long_url", pattern: memoryMarkdownLongLink},
		{name: "url_base64_payload", pattern: memoryURLPayload},
		{name: "bare_long_url", pattern: memoryBareLongURL},
		{name: "css_url", pattern: memoryCSSURL},
		{name: "css_import", pattern: memoryCSSImport},
		{name: "meta_refresh", pattern: memoryMetaRefresh},
	}
	for _, check := range checks {
		if check.pattern.MatchString(value) {
			return check.name
		}
	}
	if memoryHTMLFetchTag.MatchString(value) && memoryHTMLFetchAttribute.MatchString(value) {
		return "html_fetch"
	}
	if strings.Contains(value, "![") {
		if memoryMarkdownInlineURL.MatchString(value) {
			return "markdown_image"
		}
		if memoryMarkdownRefURL.MatchString(value) {
			return "markdown_image_shortcut"
		}
	}
	if scanMemoryImageSetAbsolute(value) {
		return "css_image_set"
	}
	return ""
}

func sanitizeMemoryBodyForClassifier(value string) string {
	value = memoryTagPattern.ReplaceAllString(value, "")
	value = memoryRolePrefixPattern.ReplaceAllString(value, "")
	value = memoryHeadingPattern.ReplaceAllString(value, "")
	return memoryBlankLinesPattern.ReplaceAllString(value, "\n\n")
}

// SanitizeMemoryDisplayText removes memory control markup before stored facts
// are rendered into prompts or compatibility summaries.
func SanitizeMemoryDisplayText(value string) string {
	value = sanitizeMemoryBodyForClassifier(value)
	value = memoryLineBreaksPattern.ReplaceAllString(value, " ")
	return strings.TrimSpace(value)
}

func decodeMemoryHTMLEntities(value string) string {
	entities := map[string]string{
		"colon": ":", "sol": "/", "bsol": "\\", "comma": ",", "num": "#", "quest": "?",
		"lpar": "(", "rpar": ")", "lbrack": "[", "rbrack": "]", "excl": "!", "quot": `"`,
		"apos": "'", "grave": "`", "NewLine": "\n", "Tab": "\t", "lt": "<", "gt": ">",
		"equals": "=", "amp": "&", "LT": "<", "GT": ">", "AMP": "&", "QUOT": `"`,
	}
	return memoryHTMLEntity.ReplaceAllStringFunc(value, func(match string) string {
		if strings.HasPrefix(strings.ToLower(match), "&#x") {
			digits := strings.TrimSuffix(match[3:], ";")
			codepoint, err := strconv.ParseInt(digits, 16, 32)
			if err != nil {
				return match
			}
			return validMemoryCodepoint(codepoint)
		}
		if strings.HasPrefix(match, "&#") {
			digits := strings.TrimSuffix(match[2:], ";")
			codepoint, err := strconv.ParseInt(digits, 10, 32)
			if err != nil {
				return match
			}
			return validMemoryCodepoint(codepoint)
		}
		semicolon := strings.HasSuffix(match, ";")
		name := strings.TrimSuffix(match[1:], ";")
		if !semicolon {
			lower := strings.ToLower(name)
			if (name == lower || name == strings.ToUpper(name)) && (lower == "lt" || lower == "gt" || lower == "amp" || lower == "quot") {
				return entities[lower]
			}
			return match
		}
		if decoded, ok := entities[name]; ok {
			return decoded
		}
		return match
	})
}

func validMemoryCodepoint(codepoint int64) string {
	if codepoint < 0 || codepoint > unicode.MaxRune || codepoint >= 0xD800 && codepoint <= 0xDFFF {
		return "�"
	}
	return string(rune(codepoint))
}

func decodeMemoryCSSEscapes(value string) string {
	return memoryCSSEscape.ReplaceAllStringFunc(value, func(match string) string {
		body := match[1:]
		hexEnd := 0
		for hexEnd < len(body) && hexEnd < 6 && isMemoryHex(body[hexEnd]) {
			hexEnd++
		}
		if hexEnd == 0 {
			return body
		}
		codepoint, err := strconv.ParseInt(body[:hexEnd], 16, 32)
		if err != nil || codepoint == 0 {
			return "�"
		}
		return validMemoryCodepoint(codepoint)
	})
}

func isMemoryHex(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' || value >= 'A' && value <= 'F'
}

func scanMemoryImageSetAbsolute(value string) bool {
	lower := strings.ToLower(value)
	for offset := 0; offset < len(lower); {
		relative := strings.Index(lower[offset:], "image-set(")
		if relative < 0 {
			return false
		}
		start := offset + relative
		if start > 0 && isMemoryWordByte(lower[start-1]) {
			offset = start + 1
			continue
		}
		if scanOneMemoryImageSet(value, start+len("image-set(")) {
			return true
		}
		offset = start + len("image-set(")
	}
	return false
}

func scanOneMemoryImageSet(value string, cursor int) bool {
	depth := 1
	var quote byte
	argumentStart := true
	absoluteString := false
	for cursor < len(value) && depth > 0 {
		character := value[cursor]
		if quote != 0 {
			switch character {
			case '\\':
				cursor++
			case quote:
				quote = 0
				if absoluteString {
					next := skipMemoryCSSSpaceAndComments(value, cursor+1)
					if next >= len(value) || memoryCSSArgumentEnd.MatchString(value[next:]) {
						return true
					}
					absoluteString = false
				}
			}
		} else {
			switch {
			case character == '/' && cursor+1 < len(value) && value[cursor+1] == '*':
				end := strings.Index(value[cursor+2:], "*/")
				if end < 0 {
					cursor = len(value) - 1
				} else {
					cursor += end + 3
				}
			case character == '"' || character == '\'':
				if argumentStart && depth == 1 && memoryCSSStringAbsolute.MatchString(value[cursor:]) {
					absoluteString = true
				}
				quote = character
				argumentStart = false
			case character == '(':
				depth++
				argumentStart = false
			case character == ')':
				depth--
			case character == ',' && depth == 1:
				argumentStart = true
			case character > 32:
				argumentStart = false
			}
		}
		cursor++
	}
	return absoluteString
}

func skipMemoryCSSSpaceAndComments(value string, cursor int) int {
	for {
		for cursor < len(value) && value[cursor] <= 32 {
			cursor++
		}
		if cursor+1 >= len(value) || value[cursor] != '/' || value[cursor+1] != '*' {
			return cursor
		}
		end := strings.Index(value[cursor+2:], "*/")
		if end < 0 {
			return len(value)
		}
		cursor += end + 4
	}
}

func isMemoryWordByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '_'
}
