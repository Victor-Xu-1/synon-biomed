package mcpdirectory

import (
	"testing"
	"time"

	"synon-go/internal/tools/mcpstdio"
)

func TestBundledRuntimeConfigUsesHostTLSPosture(t *testing.T) {
	service := &Service{bundled: defaultBundledConnectors()}
	service.SetTLSPostureProvider(func() mcpstdio.TLSPosture {
		return mcpstdio.TLSPosture{Strict: false, CABundle: "/operator/company.pem", ProxyURL: "http://proxy.example:8080"}
	})
	item, found := service.bundled["bundled:pubmed"]
	if !found {
		t.Fatal("bundled PubMed connector is missing")
	}
	config, err := service.bundledRuntimeConfig(item)
	if err != nil {
		t.Fatal(err)
	}
	if config.TrustedTLS == nil || config.TrustedTLS.Strict || config.TrustedTLS.CABundle != "/operator/company.pem" ||
		config.TrustedTLS.ProxyURL != "http://proxy.example:8080" {
		t.Fatalf("bundled connector TLS posture = %#v", config.TrustedTLS)
	}
}

func TestTLSPostureProviderCanResolveWhileDirectoryReconcileLockIsHeld(t *testing.T) {
	service := &Service{}
	service.SetTLSPostureProvider(func() mcpstdio.TLSPosture {
		return mcpstdio.TLSPosture{Strict: true}
	})
	service.mu.Lock()
	done := make(chan struct{})
	go func() {
		_ = service.applyTLSPosture(mcpstdio.ServerConfig{})
		close(done)
	}()
	select {
	case <-done:
		service.mu.Unlock()
	case <-time.After(time.Second):
		service.mu.Unlock()
		t.Fatal("TLS posture resolution deadlocked behind directory reconciliation")
	}
}
