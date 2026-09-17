package feishu

import (
	"context"
	"fmt"
	"io"
	"log"
	"regexp"
	"strings"
)

var feishuSDKSecretPattern = regexp.MustCompile(`(?i)([a-z0-9_]*(?:access_key|ticket|token|secret|password)[a-z0-9_]*)=([^&\s]+)`)

type redactingFeishuSDKLogger struct {
	logger *log.Logger
}

func newRedactingFeishuSDKLogger(writer io.Writer) *redactingFeishuSDKLogger {
	return &redactingFeishuSDKLogger{logger: log.New(writer, "", log.Ldate|log.Lmicroseconds)}
}

func (l *redactingFeishuSDKLogger) Debug(_ context.Context, args ...interface{}) {
	l.write("Debug", args...)
}

func (l *redactingFeishuSDKLogger) Info(_ context.Context, args ...interface{}) {
	l.write("Info", args...)
}

func (l *redactingFeishuSDKLogger) Warn(_ context.Context, args ...interface{}) {
	l.write("Warn", args...)
}

func (l *redactingFeishuSDKLogger) Error(_ context.Context, args ...interface{}) {
	l.write("Error", args...)
}

func (l *redactingFeishuSDKLogger) write(level string, args ...interface{}) {
	if l == nil || l.logger == nil {
		return
	}
	message := strings.TrimSpace(fmt.Sprintln(args...))
	message = feishuSDKSecretPattern.ReplaceAllString(message, `${1}=<redacted>`)
	l.logger.Printf("[%s] %s", level, message)
}
