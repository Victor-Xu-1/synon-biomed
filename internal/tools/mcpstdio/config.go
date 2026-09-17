package mcpstdio

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

func selectServers(configs map[string]ServerConfig, filter string) ([]namedConfig, error) {
	names := serverNames(configs)
	if filter != "" {
		config, ok := findServer(configs, filter)
		if !ok {
			return nil, fmt.Errorf("Server %q not found. Available servers: %s", filter, strings.Join(names, ", "))
		}
		return []namedConfig{{name: canonicalServerName(configs, filter), config: config}}, nil
	}
	selected := make([]namedConfig, 0, len(names))
	for _, name := range names {
		selected = append(selected, namedConfig{name: name, config: configs[name]})
	}
	return selected, nil
}

func findServer(configs map[string]ServerConfig, name string) (ServerConfig, bool) {
	if config, ok := configs[name]; ok {
		return config, true
	}
	normalized := NormalizeName(name)
	for serverName, config := range configs {
		if NormalizeName(serverName) == normalized {
			return config, true
		}
	}
	return ServerConfig{}, false
}

func prepareServerConfig(serverName string, config ServerConfig) ServerConfig {
	serverName = strings.TrimSpace(serverName)
	if strings.TrimSpace(config.Name) == "" {
		config.Name = serverName
	}
	if strings.TrimSpace(config.ConnectorID) == "" {
		config.ConnectorID = serverName
	}
	return config
}

func canonicalServerName(configs map[string]ServerConfig, name string) string {
	if _, ok := configs[name]; ok {
		return name
	}
	normalized := NormalizeName(name)
	for serverName := range configs {
		if NormalizeName(serverName) == normalized {
			return serverName
		}
	}
	return name
}

func serverNames(configs map[string]ServerConfig) []string {
	names := make([]string, 0, len(configs))
	for name := range configs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (c ServerConfig) isStdio() bool {
	kind := strings.TrimSpace(c.Type)
	return (kind == "" || kind == "stdio") && strings.TrimSpace(c.Command) != ""
}

func (c ServerConfig) isRemoteHTTP() bool {
	kind := strings.ToLower(strings.TrimSpace(c.Type))
	return strings.TrimSpace(c.URL) != "" && (kind == "http" || kind == "sse" || kind == "streamable_http" || kind == "streamable-http" || (kind == "" && strings.TrimSpace(c.Command) == ""))
}

func (c ServerConfig) isRemoteWebSocket() bool {
	kind := strings.ToLower(strings.TrimSpace(c.Type))
	return strings.TrimSpace(c.URL) != "" && (kind == "ws" || kind == "websocket" || kind == "ws-ide")
}

func (c ServerConfig) isSDK() bool {
	return strings.EqualFold(strings.TrimSpace(c.Type), "sdk")
}

func (c ServerConfig) isCallableTransport() bool {
	return c.isStdio() || c.isRemoteHTTP() || c.isRemoteWebSocket() || c.isSDKCallable()
}

func (c ServerConfig) IsCallableTransport() bool {
	return c.isCallableTransport()
}

// TransportLabel returns the canonical transport label used in diagnostics.
func (c ServerConfig) TransportLabel() string {
	return c.transportLabel()
}

func (c ServerConfig) transportLabel() string {
	kind := strings.ToLower(strings.TrimSpace(c.Type))
	switch {
	case c.isStdio():
		return "stdio"
	case c.isRemoteHTTP():
		if kind == "" {
			return "http"
		}
		return kind
	case c.isRemoteWebSocket():
		return kind
	case c.isSDK():
		return "sdk"
	case kind != "":
		return kind
	case strings.TrimSpace(c.URL) != "":
		return "http"
	default:
		return "unknown"
	}
}

func (c ServerConfig) isSDKCallable() bool {
	if !c.isSDK() {
		return false
	}
	_, _, ok, err := c.sdkBridgeCommand()
	return ok && err == nil
}

func (c ServerConfig) sdkServerName() string {
	if name := strings.TrimSpace(c.Name); name != "" {
		return name
	}
	return "sdk"
}

func (c ServerConfig) sdkBridgeCommand() (string, []string, bool, error) {
	command := strings.TrimSpace(c.BridgeCommand)
	args := append([]string(nil), c.BridgeArgs...)
	if command == "" {
		command = strings.TrimSpace(os.Getenv(sdkBridgeCommandEnv))
		if rawArgs := strings.TrimSpace(os.Getenv(sdkBridgeArgsEnv)); rawArgs != "" {
			if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
				return "", nil, false, fmt.Errorf("parse %s: %w", sdkBridgeArgsEnv, err)
			}
		}
	}
	if command == "" {
		return "", nil, false, nil
	}
	return command, args, true, nil
}
