package memoryextract

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"synon-go/internal/memorypolicy"
	"synon-go/internal/memorytools"
	workspace "synon-go/internal/persistence/workspace"
)

var ErrLiteralRepairFailed = errors.New("memory replacement literal repair failed")

type RepairRequest struct {
	Prompt      string
	Model       string
	MaxTokens   int
	Temperature float64
}

type RepairModel interface {
	RepairMemoryText(context.Context, RepairRequest) (string, error)
}

type ModelLiteralRepairer struct {
	model RepairModel
}

func NewModelLiteralRepairer(model RepairModel) *ModelLiteralRepairer {
	return &ModelLiteralRepairer{model: model}
}

func (r *ModelLiteralRepairer) RepairMemoryReplacement(ctx context.Context, oldText, updatedText string) (string, error) {
	oldText = workspace.RedactMemoryCredentials(oldText)
	updatedText = workspace.RedactMemoryCredentials(updatedText)
	missing := memorytools.MissingDurableLiterals(oldText, updatedText)
	if len(missing) == 0 {
		return updatedText, nil
	}
	if r == nil || r.model == nil {
		return "", fmt.Errorf("%w: repair model is unavailable", ErrLiteralRepairFailed)
	}
	maxChars := memorypolicy.TextMaxUTF16Units
	basePrompt := fmt.Sprintf(`An existing memory row is being replaced with updated text. The updated text was written from a truncated preview of the old row, so it may accidentally drop details it did not mean to change.

OLD row (full text):
%s

UPDATED text:
%s

Write the final row: the UPDATED text wins for anything it explicitly updates or corrects — state the new value as the current value, keeping the superseded old value only inside a brief parenthetical like "(was X)" — and carry over any specific value, number, path, id, or name from the OLD row that the updated text does not address at all, written exactly as it appears in the OLD row and as normal text, NOT inside the "(was …)" parenthetical (that parenthetical is only for values the update replaced). Never present an old value as current, and never introduce values that appear in neither text. 1-3 sentences, max %d characters. Reply with the final row text only.`,
		oldText, updatedText, maxChars-memorypolicy.LiteralRepairPromptHeadroom)
	feedback := ""
	var lastProblem string
	for attempt := 0; attempt < memorypolicy.LiteralRepairAttempts; attempt++ {
		prompt := basePrompt
		if feedback != "" {
			prompt += "\n\n" + feedback
		}
		candidate, err := r.model.RepairMemoryText(ctx, RepairRequest{
			Prompt: prompt, MaxTokens: memorypolicy.LiteralRepairMaxTokens, Temperature: 0,
		})
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrLiteralRepairFailed, err)
		}
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || memorypolicy.UTF16Length(candidate) > maxChars {
			quality := "empty"
			if candidate != "" {
				quality = "too long"
			}
			feedback = fmt.Sprintf("Your previous attempt was %s — reply with only the final row text, at most %d characters.", quality, maxChars-memorypolicy.LiteralRepairPromptHeadroom)
			lastProblem = quality
			continue
		}
		if lost := missingLiteralRepairValues(oldText, updatedText, candidate); len(lost) > 0 {
			feedback = "Your previous attempt dropped these values — the final row MUST contain each of them exactly as written: " + strings.Join(lost, ", ")
			lastProblem = fmt.Sprintf("dropped %d value(s)", len(lost))
			continue
		}
		if introduced := memorytools.UnexpectedDurableLiterals(candidate, oldText, updatedText); len(introduced) > 0 {
			feedback = "Your previous attempt introduced values that appear in neither the OLD row nor the UPDATED text — remove them: " + strings.Join(introduced, ", ")
			lastProblem = fmt.Sprintf("introduced %d value(s)", len(introduced))
			continue
		}
		return candidate, nil
	}
	return "", fmt.Errorf("%w after %d attempts: %s", ErrLiteralRepairFailed, memorypolicy.LiteralRepairAttempts, lastProblem)
}

func missingLiteralRepairValues(oldText, updatedText, candidate string) []string {
	result := make([]string, 0)
	seen := make(map[string]struct{})
	for _, values := range [][]string{
		memorytools.MissingDurableLiterals(oldText, candidate),
		memorytools.MissingUpdatedDurableLiterals(updatedText, candidate),
	} {
		for _, value := range values {
			key := strings.ToLower(value)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, value)
		}
	}
	return result
}
