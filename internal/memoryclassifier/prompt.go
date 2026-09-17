package memoryclassifier

import (
	_ "embed"
	"encoding/json"
	"errors"
	"strings"
)

//go:embed assets/prompt_injection_classifier.txt
var promptInjectionTemplateAsset string

type PromptInjectionPrompt struct {
	System string
	User   string
}

// BuildPromptInjectionPrompt keeps policy and untrusted memory in separate
// message roles. JSON encoding preserves the complete input as a single value;
// markup, quotes, and apparent role boundaries cannot change that structure.
func BuildPromptInjectionPrompt(body string) (PromptInjectionPrompt, error) {
	system := strings.TrimSpace(promptInjectionTemplateAsset)
	if system == "" {
		return PromptInjectionPrompt{}, errors.New("memory classifier policy is empty")
	}
	payload, err := json.Marshal(struct {
		MemoryText string `json:"memory_text"`
	}{MemoryText: body})
	if err != nil {
		return PromptInjectionPrompt{}, err
	}
	return PromptInjectionPrompt{System: system, User: string(payload)}, nil
}
