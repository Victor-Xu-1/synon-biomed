package server

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"synon-go/internal/tools/fileops"
	"time"
)

func (s *Server) executeBriefTool(toolName string, input map[string]any) (any, error) {
	if err := s.validateRegisteredTool(toolName, input); err != nil {
		return nil, err
	}
	message := strings.TrimSpace(stringValue(input["message"]))
	if message == "" {
		return nil, errors.New("SendUserMessage.message is required")
	}
	status := strings.TrimSpace(stringValue(input["status"]))
	if status == "" {
		status = "normal"
	}
	if status != "normal" && status != "proactive" {
		return nil, fmt.Errorf("SendUserMessage.status must be normal or proactive, got %q", status)
	}
	result := map[string]any{
		"message": message,
		"sentAt":  time.Now().UTC().Format(time.RFC3339Nano),
	}
	attachmentPaths := stringArrayValue(input["attachments"])
	if len(attachmentPaths) > 0 {
		attachments := make([]map[string]any, 0, len(attachmentPaths))
		for _, attachmentPath := range attachmentPaths {
			info, err := fileops.Info(s.fileRoot, attachmentPath)
			if err != nil {
				return nil, fmt.Errorf("attachment %q: %w", attachmentPath, err)
			}
			if info.Type != "file" {
				return nil, fmt.Errorf("attachment %q must be a file", attachmentPath)
			}
			attachments = append(attachments, map[string]any{
				"path":    info.Path,
				"size":    info.Size,
				"isImage": isImageAttachment(info.Path, info.MIME),
			})
		}
		result["attachments"] = attachments
	}
	return result, nil
}

func isImageAttachment(path string, mime string) bool {
	if strings.HasPrefix(strings.ToLower(mime), "image/") {
		return true
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".apng", ".avif", ".bmp", ".gif", ".jpeg", ".jpg", ".png", ".svg", ".webp":
		return true
	default:
		return false
	}
}
