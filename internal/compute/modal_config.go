package compute

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode"
)

const MaxModalConfigBytes = 1 << 20

type ModalConfigProfile struct {
	Name          string
	Workspace     string
	Active        bool
	TokenID       string
	TokenSecret   string
	TokenIDMasked string
}

type modalProfileBuilder struct {
	ModalConfigProfile
	relevant bool
	seen     map[string]struct{}
}

func ReadModalConfigProfiles(path string) ([]ModalConfigProfile, bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, errors.Is(err, os.ErrNotExist), err
	}
	if !info.Mode().IsRegular() {
		return nil, false, errors.New("Modal config must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, MaxModalConfigBytes+1))
	if err != nil {
		return nil, false, fmt.Errorf("read Modal config: %w", err)
	}
	if len(raw) > MaxModalConfigBytes {
		return nil, false, errors.New("Modal config exceeds one MiB")
	}
	profiles, err := parseModalConfig(string(raw))
	if err != nil {
		return nil, false, err
	}
	return profiles, false, nil
}

func SelectModalConfigCredential(profiles []ModalConfigProfile) (ModalConfigProfile, bool) {
	for _, profile := range profiles {
		if profile.Active && profile.TokenID != "" && profile.TokenSecret != "" {
			return profile, true
		}
	}
	for _, profile := range profiles {
		if profile.TokenID != "" && profile.TokenSecret != "" {
			return profile, true
		}
	}
	return ModalConfigProfile{}, false
}

func MaskModalToken(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= 10 {
		if len(runes) > 3 {
			runes = runes[:3]
		}
		return string(runes) + "&"
	}
	return string(runes[:7]) + "&" + string(runes[len(runes)-4:])
}

func parseModalConfig(content string) ([]ModalConfigProfile, error) {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	builders := make([]*modalProfileBuilder, 0)
	tables := make(map[string]struct{})
	var current *modalProfileBuilder
	for index, rawLine := range lines {
		lineNumber := index + 1
		line, err := stripModalTOMLComment(rawLine)
		if err != nil {
			return nil, modalConfigSyntaxError(lineNumber, err.Error())
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[[") {
			return nil, modalConfigSyntaxError(lineNumber, "array tables are not supported")
		}
		if strings.HasPrefix(line, "[") {
			name, err := parseModalTableHeader(line)
			if err != nil {
				return nil, modalConfigSyntaxError(lineNumber, err.Error())
			}
			if _, exists := tables[name]; exists {
				return nil, modalConfigSyntaxError(lineNumber, "duplicate profile table")
			}
			tables[name] = struct{}{}
			current = &modalProfileBuilder{
				ModalConfigProfile: ModalConfigProfile{Name: name},
				seen:               make(map[string]struct{}),
			}
			builders = append(builders, current)
			continue
		}
		key, value, err := splitModalTOMLAssignment(line)
		if err != nil {
			return nil, modalConfigSyntaxError(lineNumber, err.Error())
		}
		key, err = parseModalTOMLKey(key)
		if err != nil {
			return nil, modalConfigSyntaxError(lineNumber, err.Error())
		}
		if current == nil {
			// Modal profiles are tables. Valid top-level metadata is ignored.
			if strings.TrimSpace(value) == "" {
				return nil, modalConfigSyntaxError(lineNumber, "missing value")
			}
			continue
		}
		switch key {
		case "token_id", "token_secret", "active", "workspace":
			if _, duplicate := current.seen[key]; duplicate {
				return nil, modalConfigSyntaxError(lineNumber, "duplicate profile key")
			}
			current.seen[key] = struct{}{}
			current.relevant = true
		default:
			if err := validateModalTOMLValue(value); err != nil {
				return nil, modalConfigSyntaxError(lineNumber, "invalid value")
			}
			continue
		}
		switch key {
		case "token_id":
			current.TokenID, err = parseModalTOMLString(value)
		case "token_secret":
			current.TokenSecret, err = parseModalTOMLString(value)
		case "workspace":
			current.Workspace, err = parseModalTOMLString(value)
		case "active":
			switch strings.TrimSpace(value) {
			case "true":
				current.Active = true
			case "false":
				current.Active = false
			default:
				err = errors.New("expected TOML boolean")
			}
		}
		if err != nil {
			return nil, modalConfigSyntaxError(lineNumber, "invalid profile value")
		}
	}
	profiles := make([]ModalConfigProfile, 0, len(builders))
	for _, builder := range builders {
		if !builder.relevant {
			continue
		}
		profile := builder.ModalConfigProfile
		profile.TokenIDMasked = MaskModalToken(profile.TokenID)
		profiles = append(profiles, profile)
	}
	return profiles, nil
}

func stripModalTOMLComment(line string) (string, error) {
	var quote rune
	escaped := false
	for index, value := range line {
		if quote == '"' && escaped {
			escaped = false
			continue
		}
		if quote == '"' && value == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if value == quote {
				quote = 0
			}
			continue
		}
		switch value {
		case '\'', '"':
			quote = value
		case '#':
			return line[:index], nil
		}
	}
	if quote != 0 {
		return "", errors.New("unterminated string")
	}
	return line, nil
}

func parseModalTableHeader(line string) (string, error) {
	if !strings.HasSuffix(line, "]") || len(line) < 3 {
		return "", errors.New("invalid table header")
	}
	raw := strings.TrimSpace(line[1 : len(line)-1])
	name, err := parseModalTOMLKey(raw)
	if err != nil || name == "" {
		return "", errors.New("invalid table name")
	}
	return name, nil
}

func splitModalTOMLAssignment(line string) (string, string, error) {
	var quote rune
	escaped := false
	for index, value := range line {
		if quote == '"' && escaped {
			escaped = false
			continue
		}
		if quote == '"' && value == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if value == quote {
				quote = 0
			}
			continue
		}
		if value == '\'' || value == '"' {
			quote = value
			continue
		}
		if value == '=' {
			key := strings.TrimSpace(line[:index])
			value := strings.TrimSpace(line[index+1:])
			if key == "" || value == "" {
				return "", "", errors.New("invalid assignment")
			}
			return key, value, nil
		}
	}
	return "", "", errors.New("expected key/value assignment")
}

func parseModalTOMLKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("empty key")
	}
	if value[0] == '"' {
		parsed, err := strconv.Unquote(value)
		if err != nil {
			return "", errors.New("invalid quoted key")
		}
		return parsed, nil
	}
	if value[0] == '\'' {
		if len(value) < 2 || value[len(value)-1] != '\'' {
			return "", errors.New("invalid literal key")
		}
		return value[1 : len(value)-1], nil
	}
	for _, char := range value {
		if !(unicode.IsLetter(char) || unicode.IsDigit(char) || char == '_' || char == '-') {
			return "", errors.New("invalid bare key")
		}
	}
	return value, nil
}

func parseModalTOMLString(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) < 2 {
		return "", errors.New("expected string")
	}
	if value[0] == '"' {
		if err := validateModalBasicString(value); err != nil {
			return "", err
		}
		parsed, err := strconv.Unquote(value)
		if err != nil {
			return "", errors.New("invalid basic string")
		}
		return parsed, nil
	}
	if value[0] == '\'' && value[len(value)-1] == '\'' {
		inner := value[1 : len(value)-1]
		if strings.ContainsRune(inner, '\'') {
			return "", errors.New("invalid literal string")
		}
		for _, char := range inner {
			if char < 0x20 && char != '\t' {
				return "", errors.New("control character in literal string")
			}
		}
		return inner, nil
	}
	return "", errors.New("expected string")
}

func validateModalBasicString(value string) error {
	if len(value) < 2 || value[len(value)-1] != '"' {
		return errors.New("unterminated basic string")
	}
	for index := 1; index < len(value)-1; index++ {
		char := value[index]
		if char < 0x20 && char != '\t' {
			return errors.New("control character in basic string")
		}
		if char != '\\' {
			continue
		}
		index++
		if index >= len(value)-1 {
			return errors.New("truncated string escape")
		}
		escape := value[index]
		switch escape {
		case 'b', 't', 'n', 'f', 'r', '"', '\\':
			continue
		case 'u', 'U':
			digits := 4
			if escape == 'U' {
				digits = 8
			}
			if index+digits >= len(value) {
				return errors.New("truncated Unicode escape")
			}
			for offset := 1; offset <= digits; offset++ {
				if !isModalHex(value[index+offset]) {
					return errors.New("invalid Unicode escape")
				}
			}
			index += digits
		default:
			return errors.New("invalid TOML string escape")
		}
	}
	return nil
}

func validateModalTOMLValue(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("missing value")
	}
	if value[0] == '"' || value[0] == '\'' {
		_, err := parseModalTOMLString(value)
		return err
	}
	if value == "true" || value == "false" {
		return nil
	}
	if value[0] == '[' || value[0] == '{' {
		return validateBalancedModalTOMLValue(value)
	}
	if strings.ContainsAny(value, "{}[]") {
		return errors.New("unbalanced value")
	}
	return nil
}

func validateBalancedModalTOMLValue(value string) error {
	stack := make([]byte, 0, 4)
	var quote byte
	escaped := false
	for index := 0; index < len(value); index++ {
		char := value[index]
		if quote == '"' && escaped {
			escaped = false
			continue
		}
		if quote == '"' && char == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if char == quote {
				quote = 0
			}
			continue
		}
		if char == '"' || char == '\'' {
			quote = char
			continue
		}
		switch char {
		case '[', '{':
			stack = append(stack, char)
		case ']', '}':
			if len(stack) == 0 || (char == ']' && stack[len(stack)-1] != '[') || (char == '}' && stack[len(stack)-1] != '{') {
				return errors.New("unbalanced value")
			}
			stack = stack[:len(stack)-1]
		}
	}
	if quote != 0 || len(stack) != 0 {
		return errors.New("unbalanced value")
	}
	return nil
}

func isModalHex(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' || value >= 'A' && value <= 'F'
}

func modalConfigSyntaxError(line int, reason string) error {
	return fmt.Errorf("invalid Modal config at line %d: %s", line, reason)
}
