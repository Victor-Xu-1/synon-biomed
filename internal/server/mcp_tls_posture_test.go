package server

import (
	"testing"

	"synon-go/internal/tools/mcpstdio"
)

func TestServerAppliesHostTLSPostureToCustomStdioConnector(t *testing.T) {
	server := &Server{mcpX509Posture: func() mcpstdio.TLSPosture {
		return mcpstdio.TLSPosture{Strict: false, CABundle: "/operator/company.pem", ProxyURL: "http://proxy.example:8080"}
	}}
	config := server.applyMCPX509Posture(mcpstdio.ServerConfig{
		Env: map[string]string{"SYNON_MCP_X509_STRICT": "1"},
	})
	if config.TrustedTLS == nil || config.TrustedTLS.Strict || config.TrustedTLS.CABundle != "/operator/company.pem" ||
		config.TrustedTLS.ProxyURL != "http://proxy.example:8080" {
		t.Fatalf("custom connector TLS posture = %#v", config.TrustedTLS)
	}
}

func TestServerDefaultsCustomConnectorToStrictTLSPosture(t *testing.T) {
	config := (&Server{}).applyMCPX509Posture(mcpstdio.ServerConfig{})
	if config.TrustedTLS == nil || !config.TrustedTLS.Strict || config.TrustedTLS.CABundle != "" {
		t.Fatalf("default custom connector TLS posture = %#v", config.TrustedTLS)
	}
}
