package feishu

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestFeishuSDKLoggerRedactsConnectionCredentials(t *testing.T) {
	var output bytes.Buffer
	logger := newRedactingFeishuSDKLogger(&output)
	logger.Info(context.Background(), "connected to wss://msg-frontier.feishu.cn/ws/v2?access_key=access-secret&ticket=ticket-secret&token=token-secret&app_secret=app-secret&password=password-secret&device_id=visible")

	got := output.String()
	for _, secret := range []string{"access-secret", "ticket-secret", "token-secret", "app-secret", "password-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("SDK log leaked %q: %s", secret, got)
		}
	}
	for _, field := range []string{"access_key=<redacted>", "ticket=<redacted>", "token=<redacted>", "app_secret=<redacted>", "password=<redacted>"} {
		if !strings.Contains(got, field) {
			t.Fatalf("SDK log missing redaction marker %q: %s", field, got)
		}
	}
	if !strings.Contains(got, "device_id=visible") {
		t.Fatalf("SDK log removed non-sensitive context: %s", got)
	}
}
