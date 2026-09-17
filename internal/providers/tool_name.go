package providers

import (
	"errors"

	"synon-go/internal/toolcontract"
)

func normalizeModelToolName(name string) (string, error) {
	normalized, ok := toolcontract.NormalizeRuntimeName(name)
	if !ok {
		return "", errors.New("model returned an invalid tool name")
	}
	return normalized, nil
}
