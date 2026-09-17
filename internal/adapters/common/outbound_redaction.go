package common

import (
	"regexp"
	"strings"
)

const maxOutboundErrorRunes = 2048

var outboundSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization\s*[:=]\s*bearer\s+)[^\s,;]+`),
	regexp.MustCompile(`(?i)([?&](?:access[_-]?token|token|api[_-]?key|key|secret|password|signature)=)[^&#\s]+`),
	regexp.MustCompile(`(?i)((?:"|')?(?:access[_-]?token|token|api[_-]?key|secret|password|signature)(?:"|')?\s*[:=]\s*(?:"|')?)[^"'\s,;&}]+`),
}

// RedactOutboundText removes common credential forms and any exact secrets
// supplied by the caller before an adapter error is logged or serialized.
func RedactOutboundText(value string, secrets ...string) string {
	value = strings.TrimSpace(value)
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "<redacted>")
		}
	}
	for _, pattern := range outboundSecretPatterns {
		value = pattern.ReplaceAllString(value, `${1}<redacted>`)
	}
	runes := []rune(value)
	if len(runes) > maxOutboundErrorRunes {
		value = string(runes[:maxOutboundErrorRunes]) + "..."
	}
	return value
}
