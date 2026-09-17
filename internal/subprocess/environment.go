package subprocess

import (
	"fmt"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

const (
	MaxCommandBytes      = 16 * 1024
	MaxArgumentCount     = 256
	MaxArgumentBytes     = 16 * 1024
	MaxArgumentsBytes    = 32 * 1024
	MaxConfiguredEnv     = 256
	MaxEnvironmentBytes  = 512 * 1024
	MaxEnvironmentValue  = 128 * 1024
	MaxEnvironmentKeyLen = 256
)

var environmentKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var inheritedEnvironmentKeys = []string{
	"APPDATA",
	"ComSpec",
	"COLORTERM",
	"CURL_CA_BUNDLE",
	"HOME",
	"LANG",
	"LC_ALL",
	"LC_CTYPE",
	"LOCALAPPDATA",
	"LOGNAME",
	"NODE_EXTRA_CA_CERTS",
	"PATH",
	"PATHEXT",
	"PROGRAMDATA",
	"ProgramFiles",
	"ProgramW6432",
	"REQUESTS_CA_BUNDLE",
	"SHELL",
	"SSL_CERT_DIR",
	"SSL_CERT_FILE",
	"SystemRoot",
	"TERM",
	"TERM_PROGRAM",
	"TEMP",
	"TMP",
	"TMPDIR",
	"TZ",
	"USER",
	"USERPROFILE",
	"WINDIR",
	"DISPLAY",
	"WAYLAND_DISPLAY",
	"WSL_DISTRO_NAME",
	"WSL_INTEROP",
	"XDG_CACHE_HOME",
	"XDG_CONFIG_HOME",
	"XDG_DATA_HOME",
	"XDG_STATE_HOME",
	"XDG_RUNTIME_DIR",
}

type environmentEntry struct {
	key   string
	value string
}

func ValidateSpec(kind string, command string, args []string) error {
	kind = processKind(kind)
	if command == "" {
		return fmt.Errorf("%s command is required", kind)
	}
	if strings.IndexByte(command, 0) >= 0 || len(command) > MaxCommandBytes {
		return fmt.Errorf("%s command is invalid or exceeds %d bytes", kind, MaxCommandBytes)
	}
	if len(args) > MaxArgumentCount {
		return fmt.Errorf("%s has %d arguments; limit is %d", kind, len(args), MaxArgumentCount)
	}
	total := 0
	for index, arg := range args {
		if strings.IndexByte(arg, 0) >= 0 {
			return fmt.Errorf("%s argument %d contains a NUL byte", kind, index)
		}
		if len(arg) > MaxArgumentBytes {
			return fmt.Errorf("%s argument %d exceeds %d bytes", kind, index, MaxArgumentBytes)
		}
		total += len(arg)
		if total > MaxArgumentsBytes {
			return fmt.Errorf("%s arguments exceed %d bytes", kind, MaxArgumentsBytes)
		}
	}
	return nil
}

func BuildEnvironment(kind string, layers ...map[string]string) ([]string, error) {
	kind = processKind(kind)
	entries := make(map[string]environmentEntry, len(inheritedEnvironmentKeys)+16)
	for _, key := range inheritedEnvironmentKeys {
		if value, ok := os.LookupEnv(key); ok {
			entries[canonicalEnvironmentKey(key)] = environmentEntry{key: key, value: value}
		}
	}

	configured := 0
	for _, layer := range layers {
		configured += len(layer)
		if configured > MaxConfiguredEnv {
			return nil, fmt.Errorf("%s environment has %d configured entries; limit is %d", kind, configured, MaxConfiguredEnv)
		}
		keys := make([]string, 0, len(layer))
		for key := range layer {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value := layer[key]
			if err := validateEnvironmentEntry(kind, key, value); err != nil {
				return nil, err
			}
			entries[canonicalEnvironmentKey(key)] = environmentEntry{key: key, value: value}
		}
	}

	ordered := make([]environmentEntry, 0, len(entries))
	total := 0
	for _, entry := range entries {
		if err := validateEnvironmentEntry(kind, entry.key, entry.value); err != nil {
			return nil, err
		}
		total += len(entry.key) + 1 + len(entry.value)
		if total > MaxEnvironmentBytes {
			return nil, fmt.Errorf("%s environment exceeds %d bytes", kind, MaxEnvironmentBytes)
		}
		ordered = append(ordered, entry)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].key < ordered[j].key
	})
	serialized := make([]string, 0, len(ordered))
	for _, entry := range ordered {
		serialized = append(serialized, entry.key+"="+entry.value)
	}
	return serialized, nil
}

func validateEnvironmentEntry(kind string, key string, value string) error {
	if len(key) == 0 || len(key) > MaxEnvironmentKeyLen || !environmentKeyPattern.MatchString(key) {
		return fmt.Errorf("%s environment key %q is invalid", kind, key)
	}
	if strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("%s environment %q contains a NUL byte", kind, key)
	}
	if len(value) > MaxEnvironmentValue {
		return fmt.Errorf("%s environment %q exceeds %d bytes", kind, key, MaxEnvironmentValue)
	}
	return nil
}

func canonicalEnvironmentKey(key string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(key)
	}
	return key
}

func processKind(kind string) string {
	if kind = strings.TrimSpace(kind); kind != "" {
		return kind
	}
	return "subprocess"
}
