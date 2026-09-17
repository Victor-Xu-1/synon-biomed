package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	runtimekv "synon-go/internal/persistence/runtimekv"
)

func TestRecordToolGatewayAuditBoundsOversizedDynamicFields(t *testing.T) {
	store := runtimekv.New(t.TempDir() + "/runtime-state.sqlite")
	server := &Server{runtimeStore: store}
	oversized := strings.Repeat("x", toolGatewayAuditFieldMaxBytes+1)
	server.recordToolGatewayAudit(
		"runtime", "Python", "call-a", map[string]any{"code": oversized}, map[string]any{"code": oversized},
		"completed", map[string]any{"output": oversized}, oversized, time.Now(), nil,
	)
	entries, err := store.List(toolGatewayAuditNamespace)
	if err != nil || len(entries) != 1 {
		t.Fatalf("audit entries=%d err=%v", len(entries), err)
	}
	value, ok := entries[0].Value.(map[string]any)
	if !ok {
		t.Fatalf("audit value=%#v", entries[0].Value)
	}
	for _, field := range []string{"input", "result"} {
		raw, err := json.Marshal(value[field])
		if err != nil || len(raw) > toolGatewayAuditFieldMaxBytes {
			t.Fatalf("%s bounded bytes=%d err=%v value=%#v", field, len(raw), err, value[field])
		}
		if strings.Contains(string(raw), oversized) {
			t.Fatalf("%s retained the oversized value", field)
		}
	}
	if errorText, ok := value["error"].(string); !ok || len(errorText) > toolGatewayAuditTextMaxBytes {
		t.Fatalf("bounded error=%#v", value["error"])
	}
}

func TestCompactToolGatewayAuditsRewritesLegacyOversizedValues(t *testing.T) {
	store := runtimekv.New(t.TempDir() + "/runtime-state.sqlite")
	oversized := strings.Repeat("legacy", toolGatewayAuditFieldMaxBytes)
	if _, err := store.Set(toolGatewayAuditNamespace, "legacy", map[string]any{
		"id": "legacy", "result": map[string]any{"output": oversized},
	}); err != nil {
		t.Fatal(err)
	}
	server := &Server{runtimeStore: store}
	if err := server.compactToolGatewayAudits(); err != nil {
		t.Fatal(err)
	}
	entry, found, err := store.Get(toolGatewayAuditNamespace, "legacy")
	if err != nil || !found {
		t.Fatalf("compacted entry found=%t err=%v", found, err)
	}
	value := entry.Value.(map[string]any)
	raw, err := json.Marshal(value["result"])
	if err != nil || len(raw) > toolGatewayAuditFieldMaxBytes {
		t.Fatalf("legacy result bounded bytes=%d err=%v value=%#v", len(raw), err, value["result"])
	}
	if strings.Contains(string(raw), oversized) {
		t.Fatal("legacy result retained the oversized value")
	}
}

func TestRecordToolGatewayAuditRedactsSensitiveValues(t *testing.T) {
	store := runtimekv.New(t.TempDir() + "/runtime-state.sqlite")
	server := &Server{runtimeStore: store}
	server.recordToolGatewayAudit(
		"runtime", "WebFetch", "call-secret",
		map[string]any{"Authorization": "Bearer top-secret", "nested": map[string]any{"api_key": "sk-private"}},
		map[string]any{"url": "https://example.test/?token=private-token"},
		"failed", map[string]any{"cookie": "session=private"},
		"request failed Authorization: Bearer leaked-token", time.Now(), nil,
	)
	entries, err := store.List(toolGatewayAuditNamespace)
	if err != nil || len(entries) != 1 {
		t.Fatalf("audit entries=%d err=%v", len(entries), err)
	}
	raw := fmt.Sprintf("%#v", entries[0].Value)
	for _, secret := range []string{"top-secret", "sk-private", "private-token", "session=private", "leaked-token"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("audit leaked %q: %s", secret, raw)
		}
	}
	if !strings.Contains(raw, "<redacted>") {
		t.Fatalf("audit missing redaction marker: %s", raw)
	}
}

func TestCompactToolGatewayAuditsRedactsLegacyAndPrunesOldest(t *testing.T) {
	store := runtimekv.New(t.TempDir() + "/runtime-state.sqlite")
	base := time.Now().UTC().Add(-time.Hour)
	if err := store.EditNamespace(toolGatewayAuditNamespace, func(entries map[string]runtimekv.Entry) (bool, error) {
		for index := 0; index < toolGatewayAuditMaxEntries+8; index++ {
			key := fmt.Sprintf("audit-%03d", index)
			entries[key] = runtimekv.Entry{
				Namespace: toolGatewayAuditNamespace,
				Key:       key,
				Value: map[string]any{
					"id":    key,
					"input": map[string]any{"password": "legacy-secret", "query": "safe"},
				},
				Version:   1,
				UpdatedAt: base.Add(time.Duration(index) * time.Second),
			}
		}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	server := &Server{runtimeStore: store}
	if err := server.compactToolGatewayAudits(); err != nil {
		t.Fatal(err)
	}
	entries, err := store.List(toolGatewayAuditNamespace)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != toolGatewayAuditMaxEntries {
		t.Fatalf("audit entries=%d want=%d", len(entries), toolGatewayAuditMaxEntries)
	}
	if _, found, err := store.Get(toolGatewayAuditNamespace, "audit-000"); err != nil || found {
		t.Fatalf("oldest audit found=%t err=%v", found, err)
	}
	latest, found, err := store.Get(toolGatewayAuditNamespace, fmt.Sprintf("audit-%03d", toolGatewayAuditMaxEntries+7))
	if err != nil || !found {
		t.Fatalf("latest audit found=%t err=%v", found, err)
	}
	raw := fmt.Sprintf("%#v", latest.Value)
	if strings.Contains(raw, "legacy-secret") || !strings.Contains(raw, "<redacted>") {
		t.Fatalf("legacy audit was not redacted: %s", raw)
	}
}
