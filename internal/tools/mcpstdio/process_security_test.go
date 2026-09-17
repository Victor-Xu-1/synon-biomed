package mcpstdio

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMCPOutputCancellationUnblocksScanner(t *testing.T) {
	reader, writer := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	stop := stopMCPOutputOnContext(ctx, reader)
	defer stop()

	readDone := make(chan error, 1)
	go func() {
		_, err := reader.Read(make([]byte, 1))
		readDone <- err
	}()
	cancel()
	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("cancelled MCP output reader returned without an error")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled MCP output reader remained blocked")
	}
	_ = writer.Close()
}

func TestStdioMCPRequestHonorsContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	session, err := startSession(ctx, t.TempDir(), ServerConfig{
		Command: "python3",
		Args:    []string{"-c", "import time; time.sleep(60)"},
	})
	if err != nil {
		t.Fatalf("start blocked MCP fixture: %v", err)
	}
	defer session.close()
	started := time.Now()
	_, err = session.request(ctx, 1, "initialize", nil)
	if err == nil {
		t.Fatal("blocked MCP request unexpectedly succeeded")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked MCP request error = %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("cancelled MCP request took %s: %v", elapsed, err)
	}
}

const (
	stdioFixtureEnvironment = "GO_WANT_MCP_STDIO_ENV_FIXTURE"
	hostSecretEnvironment   = "SYNON_MCP_HOST_SECRET_SHOULD_NOT_LEAK"
	explicitEnvironment     = "SYNON_MCP_EXPLICIT_FIXTURE_VALUE"
	tlsPostureEnvironment   = "SYNON_MCP_X509_STRICT"
	tlsBundleEnvironment    = "SSL_CERT_FILE"
)

func TestStdioMCPSubprocessUsesMinimalExplicitEnvironment(t *testing.T) {
	if os.Getenv(stdioFixtureEnvironment) == "1" {
		runStdioEnvironmentFixture()
		return
	}
	t.Setenv(hostSecretEnvironment, "host-only-secret")
	session, err := startSession(context.Background(), t.TempDir(), ServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestStdioMCPSubprocessUsesMinimalExplicitEnvironment", "--"},
		Env: map[string]string{
			stdioFixtureEnvironment: "1",
			explicitEnvironment:     "configured-value",
			tlsPostureEnvironment:   "0",
			tlsBundleEnvironment:    "/untrusted/connector.pem",
			"HTTPS_PROXY":           "http://untrusted.example:9999",
		},
		TrustedTLS: &TLSPosture{Strict: true, CABundle: "/trusted/operator.pem", ProxyURL: "http://proxy.example:8080"},
	})
	if err != nil {
		t.Fatalf("start stdio environment fixture: %v", err)
	}
	defer session.close()
	result, err := session.request(context.Background(), 1, "environment/probe", nil)
	if err != nil {
		t.Fatalf("probe stdio environment: %v", err)
	}
	var decoded struct {
		Explicit string `json:"explicit"`
		HostLeak string `json:"hostLeak"`
		TLSMode  string `json:"tlsMode"`
		Bundle   string `json:"bundle"`
		Proxy    string `json:"proxy"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatalf("decode stdio environment probe: %v", err)
	}
	if decoded.Explicit != "configured-value" || decoded.HostLeak != "" || decoded.TLSMode != "1" ||
		decoded.Bundle != "/trusted/operator.pem" || decoded.Proxy != "http://proxy.example:8080" {
		t.Fatalf("stdio environment probe = %#v", decoded)
	}
}

func TestMCPTrustedTLSRelaxationOverridesConnectorEnvironment(t *testing.T) {
	environment, err := buildMCPEnvironment(
		map[string]string{
			"SYNON_MCP_X509_STRICT": "1",
			"SSL_CERT_FILE":         "/untrusted/connector.pem",
		},
		trustedMCPEnvironment(ServerConfig{TrustedTLS: &TLSPosture{
			Strict: false, CABundle: "/trusted/operator.pem",
		}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(environment, "\n")
	if !strings.Contains(joined, "SYNON_MCP_X509_STRICT=0") ||
		!strings.Contains(joined, "SSL_CERT_FILE=/trusted/operator.pem") ||
		strings.Contains(joined, "/untrusted/connector.pem") {
		t.Fatalf("trusted TLS environment = %q", joined)
	}
}

func TestMCPTrustedNetworkInheritsCredentialedOperatorProxyWithoutHostSecretSweep(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://operator:synthetic-secret@proxy.example:8080")
	layer := trustedMCPEnvironment(ServerConfig{TrustedTLS: &TLSPosture{Strict: true}})
	if layer["HTTPS_PROXY"] != "http://operator:synthetic-secret@proxy.example:8080" ||
		layer["HTTP_PROXY"] != layer["HTTPS_PROXY"] {
		t.Fatalf("trusted inherited proxy was not propagated: %#v", layer)
	}
}

func TestMCPSubprocessEnvironmentValidationAndOrdering(t *testing.T) {
	t.Setenv("PATH", "/safe/bin")
	t.Setenv(hostSecretEnvironment, "must-not-appear")
	environment, err := buildMCPEnvironment(
		map[string]string{"Z_LAST": "z", "A_FIRST": "a"},
		map[string]string{"Z_LAST": "override"},
	)
	if err != nil {
		t.Fatalf("build MCP environment: %v", err)
	}
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, hostSecretEnvironment) || strings.Contains(joined, "must-not-appear") {
		t.Fatalf("host secret leaked into MCP environment: %q", joined)
	}
	if !strings.Contains(joined, "PATH=/safe/bin") || !strings.Contains(joined, "Z_LAST=override") {
		t.Fatalf("required environment missing: %q", joined)
	}
	for index := 1; index < len(environment); index++ {
		if environment[index-1] > environment[index] {
			t.Fatalf("environment is not deterministic: %#v", environment)
		}
	}

	if _, err := buildMCPEnvironment(map[string]string{"BAD=KEY": "value"}); err == nil {
		t.Fatal("invalid environment key was accepted")
	}
	if _, err := buildMCPEnvironment(map[string]string{"VALID": "bad\x00value"}); err == nil {
		t.Fatal("NUL environment value was accepted")
	}
	tooMany := make(map[string]string, maxMCPConfiguredEnv+1)
	for index := 0; index <= maxMCPConfiguredEnv; index++ {
		tooMany["KEY_"+strconv.Itoa(index)] = "value"
	}
	if _, err := buildMCPEnvironment(tooMany); err == nil {
		t.Fatal("oversized configured environment was accepted")
	}
}

func TestMCPSubprocessArgumentValidation(t *testing.T) {
	if err := validateMCPProcessSpec("", nil); err == nil {
		t.Fatal("empty command was accepted")
	}
	if err := validateMCPProcessSpec("command", []string{"bad\x00argument"}); err == nil {
		t.Fatal("NUL argument was accepted")
	}
	if err := validateMCPProcessSpec("command", make([]string, maxMCPArgumentCount+1)); err == nil {
		t.Fatal("excess argument count was accepted")
	}
}

func runStdioEnvironmentFixture() {
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request rpcMessage
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		_ = encoder.Encode(rpcMessage{
			JSONRPC: "2.0",
			ID:      request.ID,
			Result: mustRawJSON(map[string]any{
				"explicit": os.Getenv(explicitEnvironment),
				"hostLeak": os.Getenv(hostSecretEnvironment),
				"tlsMode":  os.Getenv(tlsPostureEnvironment),
				"bundle":   os.Getenv(tlsBundleEnvironment),
				"proxy":    os.Getenv("HTTPS_PROXY"),
			}),
		})
	}
	os.Exit(0)
}
