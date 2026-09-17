package qrimage

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestEncodeURLReturnsLocalPNGDataURL(t *testing.T) {
	dataURL, err := EncodeURL("https://liteapp.weixin.qq.com/q/provider-ticket", 256)
	if err != nil {
		t.Fatalf("EncodeURL() error = %v", err)
	}
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(dataURL, prefix) {
		t.Fatalf("data URL prefix = %q", dataURL[:min(len(dataURL), len(prefix))])
	}
	png, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(dataURL, prefix))
	if err != nil {
		t.Fatalf("decode PNG data URL: %v", err)
	}
	if len(png) < 8 || string(png[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("QR is not a PNG: %x", png[:min(len(png), 8)])
	}
}

func TestEncodeURLRejectsUnsafeOrUnboundedInput(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		value string
		size  int
	}{
		{name: "empty", value: "", size: 256},
		{name: "javascript", value: "javascript:alert(1)", size: 256},
		{name: "hostless", value: "https:///missing-host", size: 256},
		{name: "payload too large", value: "https://example.test/" + strings.Repeat("a", 4096), size: 256},
		{name: "image too small", value: "https://example.test/qr", size: 64},
		{name: "image too large", value: "https://example.test/qr", size: 512},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := EncodeURL(testCase.value, testCase.size); err == nil {
				t.Fatal("EncodeURL() unexpectedly succeeded")
			}
		})
	}
}
