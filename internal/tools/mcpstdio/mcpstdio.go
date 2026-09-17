package mcpstdio

import "time"

const defaultTimeout = 120 * time.Second
const maxStderrBytes = 64 * 1024
const maxScannerTokenBytes = 32 * 1024 * 1024
const maxRemoteResponseBytes = 10 * 1024 * 1024
const sdkBridgeCommandEnv = "SYNON_MCP_SDK_BRIDGE_COMMAND"
const sdkBridgeArgsEnv = "SYNON_MCP_SDK_BRIDGE_ARGS_JSON"

type mcpHTTPClientContextKey struct{}
