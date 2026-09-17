package feishu

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestEncodeDeviceVerificationURLReturnsLocalPNGDataURL(t *testing.T) {
	dataURL, err := EncodeDeviceVerificationURL("https://accounts.feishu.cn/device/complete?code=device-1", 256)
	if err != nil {
		t.Fatalf("encode QR: %v", err)
	}
	prefix := "data:image/png;base64,"
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

func TestEncodeDeviceVerificationURLRejectsUnsafeOrUnboundedInput(t *testing.T) {
	for _, testCase := range []struct {
		name string
		url  string
		size int
	}{
		{name: "javascript", url: "javascript:alert(1)", size: 256},
		{name: "small", url: "https://accounts.feishu.cn/device/1", size: 64},
		{name: "large", url: "https://accounts.feishu.cn/device/1", size: 512},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := EncodeDeviceVerificationURL(testCase.url, testCase.size); err == nil {
				t.Fatalf("unsafe input accepted")
			}
		})
	}
}
