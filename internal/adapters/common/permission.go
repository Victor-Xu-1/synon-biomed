package common

import (
	"strings"
)

type PermissionDecision struct {
	RequestID string
	Allowed   bool
	Rule      string
}

func ParsePermitCallbackData(data string) (PermissionDecision, bool) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 || parts[0] != "permit" || parts[1] == "" {
		return PermissionDecision{}, false
	}

	switch parts[2] {
	case "yes":
		return PermissionDecision{RequestID: parts[1], Allowed: true}, true
	case "always":
		return PermissionDecision{RequestID: parts[1], Allowed: true, Rule: "always"}, true
	case "no":
		return PermissionDecision{RequestID: parts[1], Allowed: false}, true
	default:
		return PermissionDecision{}, false
	}
}

func ParsePermissionCommand(text string, pendingRequestIDs []string) (PermissionDecision, bool) {
	trimmed := strings.TrimSpace(text)
	fields := strings.Fields(trimmed)
	if len(fields) >= 2 && strings.HasPrefix(fields[0], "/") {
		action := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
		requestID := fields[1]
		switch action {
		case "deny":
			return PermissionDecision{RequestID: requestID, Allowed: false}, true
		case "always", "allow-always":
			return PermissionDecision{RequestID: requestID, Allowed: true, Rule: "always"}, true
		case "allow":
			return PermissionDecision{RequestID: requestID, Allowed: true}, true
		}
	}

	if len(pendingRequestIDs) != 1 {
		return PermissionDecision{}, false
	}
	requestID := pendingRequestIDs[0]
	shortcut := strings.ToLower(trimmed)

	if containsShortcut(shortcut, []string{"1", "/1", "allow", "/allow", "y", "yes", "允许", "允许一次", "同意", "批准"}) {
		return PermissionDecision{RequestID: requestID, Allowed: true}, true
	}
	if containsShortcut(shortcut, []string{"2", "/2", "always", "/always", "allow-always", "/allow-always", "永久允许", "一直允许"}) {
		return PermissionDecision{RequestID: requestID, Allowed: true, Rule: "always"}, true
	}
	if containsShortcut(shortcut, []string{"3", "/3", "deny", "/deny", "n", "no", "拒绝", "不允许", "否"}) {
		return PermissionDecision{RequestID: requestID, Allowed: false}, true
	}
	return PermissionDecision{}, false
}

func containsShortcut(value string, candidates []string) bool {
	for _, candidate := range candidates {
		if value == candidate {
			return true
		}
	}
	return false
}
