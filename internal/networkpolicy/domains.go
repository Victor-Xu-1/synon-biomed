package networkpolicy

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// PublicWildcard grants any syntactically valid public hostname. Private,
// reserved and link-local destinations are still rejected. An explicit public
// grant replaces restrictive domain defaults, never operator-supplied denies.
const PublicWildcard = "*"

var reservedDomainSuffixes = []string{
	".arpa", ".example", ".home", ".internal", ".invalid", ".lan", ".local", ".localhost", ".onion", ".test",
}

var builtInDeniedPatterns = []string{
	"s3.amazonaws.com", "s3.*.amazonaws.com", "storage.googleapis.com", "commondatastorage.googleapis.com",
	"r2.cloudflarestorage.com", "transfer.sh", "*.transfer.sh", "pastebin.com", "*.pastebin.com",
	"paste.ee", "*.paste.ee", "hastebin.com", "*.hastebin.com", "dpaste.org", "*.dpaste.org",
	"dpaste.com", "*.dpaste.com", "file.io", "*.file.io", "0x0.st", "*.0x0.st", "temp.sh", "*.temp.sh",
	"bashupload.com", "*.bashupload.com", "termbin.com", "*.termbin.com", "sprunge.us", "*.sprunge.us",
	"gofile.io", "*.gofile.io", "catbox.moe", "*.catbox.moe", "hooks.slack.com",
	"hooks.zapier.com", "maker.ifttt.com", "discord.com", "*.discord.com", "discordapp.com", "*.discordapp.com",
	"webhook.site", "*.webhook.site", "ntfy.sh", "*.ntfy.sh", "requestbin.com", "*.requestbin.com",
	"pipedream.net", "*.pipedream.net", "ngrok.app", "*.ngrok.app", "ngrok-free.app", "*.ngrok-free.app",
	"ngrok.io", "*.ngrok.io", "ngrok.dev", "*.ngrok.dev", "ngrok-free.dev", "*.ngrok-free.dev",
	"trycloudflare.com", "*.trycloudflare.com", "loca.lt", "*.loca.lt", "serveo.net", "*.serveo.net",
	"telebit.cloud", "*.telebit.cloud",
}

var reservedIPv4Prefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
}

func BuiltInDeniedPatterns() []string {
	return append([]string{}, builtInDeniedPatterns...)
}

// EffectiveDeniedPatterns keeps default restrictions separate from explicit
// operator policy. Persist this resolved policy with each execution authority.
func EffectiveDeniedPatterns(allowed, denied []string) []string {
	for _, pattern := range allowed {
		if pattern == PublicWildcard {
			return append([]string{}, denied...)
		}
	}
	return append(BuiltInDeniedPatterns(), denied...)
}

func NormalizePattern(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(value, ".")))
	if value == "" {
		return "", errors.New("domain is required")
	}
	if value == PublicWildcard {
		return PublicWildcard, nil
	}
	if strings.Contains(value, "://") || strings.ContainsAny(value, "/?#@: ") {
		return "", fmt.Errorf("invalid domain %q", value)
	}
	host := strings.TrimPrefix(value, "*.")
	if host == value && strings.HasPrefix(value, "*") {
		return "", fmt.Errorf("invalid wildcard domain %q", value)
	}
	if host != value && net.ParseIP(host) != nil {
		return "", fmt.Errorf("invalid wildcard domain %q", value)
	}
	if len(host) > 253 {
		return "", errors.New("domain is too long")
	}
	if ip := net.ParseIP(host); ip != nil {
		return "", fmt.Errorf("invalid domain %q", value)
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return "", fmt.Errorf("domain %q must contain a public suffix", value)
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("invalid domain label in %q", value)
		}
		for _, character := range label {
			if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
				continue
			}
			return "", fmt.Errorf("invalid domain label in %q", value)
		}
	}
	return value, nil
}

func NormalizePatterns(values []string, max int) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	domains := make([]string, 0, len(values))
	for _, value := range values {
		domain, err := NormalizePattern(value)
		if err != nil {
			return nil, err
		}
		if _, found := seen[domain]; found {
			continue
		}
		seen[domain] = struct{}{}
		domains = append(domains, domain)
	}
	if max > 0 && len(domains) > max {
		return nil, fmt.Errorf("too many domains (max %d)", max)
	}
	return domains, nil
}

func PrivateOrReserved(pattern string) bool {
	if pattern == PublicWildcard {
		return false
	}
	host := strings.TrimPrefix(pattern, "*.")
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsMulticast() {
			return true
		}
		address, err := netip.ParseAddr(host)
		if err != nil {
			return true
		}
		for _, prefix := range reservedIPv4Prefixes {
			if prefix.Contains(address) {
				return true
			}
		}
		return false
	}
	if host == "localhost" {
		return true
	}
	for _, suffix := range reservedDomainSuffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

func ConflictingPattern(pattern string, denied []string) string {
	for _, candidate := range denied {
		if patternsOverlap(pattern, candidate) {
			return candidate
		}
	}
	return ""
}

// AllowsHost applies normalized allow and deny patterns to one concrete host.
// Deny always wins. Callers remain responsible for resolving and rejecting
// private/reserved IP addresses before dialing.
func AllowsHost(host string, allowed, denied []string) bool {
	host, err := NormalizePattern(host)
	if err != nil || PrivateOrReserved(host) {
		return false
	}
	for _, pattern := range EffectiveDeniedPatterns(allowed, denied) {
		if patternCovers(pattern, host) {
			return false
		}
	}
	for _, pattern := range allowed {
		if patternCovers(pattern, host) {
			return true
		}
	}
	return false
}

func patternsOverlap(first, second string) bool {
	return patternCovers(first, second) || patternCovers(second, first)
}

func patternCovers(pattern, candidate string) bool {
	if pattern == PublicWildcard {
		return true
	}
	if pattern == candidate {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		suffix := strings.TrimPrefix(pattern, "*.")
		candidate = strings.TrimPrefix(candidate, "*.")
		return candidate == suffix || strings.HasSuffix(candidate, "."+suffix)
	}
	if !strings.Contains(pattern, "*") {
		return false
	}
	patternLabels := strings.Split(pattern, ".")
	candidateLabels := strings.Split(strings.TrimPrefix(candidate, "*."), ".")
	if len(patternLabels) != len(candidateLabels) {
		return false
	}
	for index := range patternLabels {
		if patternLabels[index] != "*" && patternLabels[index] != candidateLabels[index] {
			return false
		}
	}
	return true
}
