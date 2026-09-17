package memoryprompt

import (
	_ "embed"
	"strings"
)

//go:embed assets/memory_rules.md
var memoryRules string

//go:embed assets/memory_privacy.md
var whatNotToSave string

//go:embed assets/memory_write_guidance.md
var memoryWriteNudge string

func MemoryRules() string {
	// The serialized context splitter recognizes this protocol header. Keep
	// it in code so editing policy prose cannot change the message trust role.
	return SystemHeader + "\n\n" + strings.TrimSpace(memoryRules)
}

func WhatNotToSave() string {
	return strings.TrimSpace(whatNotToSave)
}

func MemoryWriteNudge() string {
	return strings.TrimSpace(memoryWriteNudge)
}

func SystemRules() string {
	return strings.Join([]string{
		MemoryRules(),
		WhatNotToSave(),
		MemoryWriteNudge(),
	}, "\n\n")
}
