package feishu

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

type StreamingCardPhase string

const (
	StreamingCardIdle       StreamingCardPhase = "idle"
	StreamingCardCreating   StreamingCardPhase = "creating"
	StreamingCardStreaming  StreamingCardPhase = "streaming"
	StreamingCardFinalizing StreamingCardPhase = "finalizing"
	StreamingCardCompleted  StreamingCardPhase = "completed"
	StreamingCardAborted    StreamingCardPhase = "aborted"
)

const ReasoningPreviewChars = 600

type ToolStep struct {
	ID     string
	Name   string
	Status string
}

type StreamingCard struct {
	client           *CardKitClient
	chatID           string
	replyToMessageID string
	mu               sync.Mutex
	ioMu             sync.Mutex

	phase                    StreamingCardPhase
	cardID                   string
	messageID                string
	sequence                 int64
	cardKitStreamActive      bool
	accumulatedText          string
	accumulatedReasoningText string
	toolSteps                []ToolStep
	lastFlushedText          string
	flushController          *FlushController
}

func NewStreamingCard(client *CardKitClient, chatID string, replyToMessageID string) *StreamingCard {
	card := &StreamingCard{
		client:           client,
		chatID:           chatID,
		replyToMessageID: replyToMessageID,
		phase:            StreamingCardIdle,
	}
	card.flushController = NewFlushController(func() error {
		return card.performFlush(context.Background())
	})
	return card
}

func (c *StreamingCard) EnsureCreated(ctx context.Context) error {
	c.ioMu.Lock()
	defer c.ioMu.Unlock()

	c.mu.Lock()
	if c.phase != StreamingCardIdle {
		c.mu.Unlock()
		return nil
	}
	c.phase = StreamingCardCreating
	c.mu.Unlock()

	cardID, err := c.client.CreateCardEntity(ctx, BuildInitialStreamingCard())
	if err == nil {
		messageID, sendErr := c.client.SendCardAsMessage(ctx, c.chatID, cardID, c.replyToMessageID)
		if sendErr == nil {
			c.mu.Lock()
			c.cardID = cardID
			c.messageID = messageID
			c.sequence = 1
			c.cardKitStreamActive = true
			if c.phase == StreamingCardCreating {
				c.phase = StreamingCardStreaming
			}
			hasContent := c.hasAnyContentLocked()
			throttle := c.currentThrottleLocked()
			c.mu.Unlock()
			c.flushController.SetCardMessageReady(true)
			if hasContent {
				_ = c.flushController.ThrottledUpdate(throttle)
			}
			return nil
		}
		err = sendErr
	}

	messageID, fallbackErr := c.client.SendRenderedCardAsMessage(ctx, c.chatID, BuildRenderedCard(" "), c.replyToMessageID)
	if fallbackErr != nil {
		c.mu.Lock()
		if c.phase == StreamingCardCreating {
			c.phase = StreamingCardAborted
		}
		c.mu.Unlock()
		return fallbackErr
	}
	_ = err
	c.mu.Lock()
	c.cardID = ""
	c.messageID = messageID
	c.sequence = 0
	c.cardKitStreamActive = false
	if c.phase == StreamingCardCreating {
		c.phase = StreamingCardStreaming
	}
	hasContent := c.hasAnyContentLocked()
	throttle := c.currentThrottleLocked()
	c.mu.Unlock()
	c.flushController.SetCardMessageReady(true)
	if hasContent {
		_ = c.flushController.ThrottledUpdate(throttle)
	}
	return nil
}

func (c *StreamingCard) AppendText(delta string) {
	c.mu.Lock()
	if delta == "" || c.phase == StreamingCardCompleted || c.phase == StreamingCardAborted {
		c.mu.Unlock()
		return
	}
	merged := mergeStreamingText(c.accumulatedText, delta)
	if merged == c.accumulatedText {
		c.mu.Unlock()
		return
	}
	c.accumulatedText = merged
	throttle := c.currentThrottleLocked()
	c.mu.Unlock()
	_ = c.flushController.ThrottledUpdate(throttle)
}

func (c *StreamingCard) AppendReasoning(delta string) {
	c.mu.Lock()
	if delta == "" || c.phase == StreamingCardCompleted || c.phase == StreamingCardAborted {
		c.mu.Unlock()
		return
	}
	merged := mergeStreamingText(c.accumulatedReasoningText, delta)
	if merged == c.accumulatedReasoningText {
		c.mu.Unlock()
		return
	}
	c.accumulatedReasoningText = merged
	throttle := c.currentThrottleLocked()
	c.mu.Unlock()
	_ = c.flushController.ThrottledUpdate(throttle)
}

func (c *StreamingCard) StartTool(toolUseID string, toolName string) {
	c.mu.Lock()
	if c.phase == StreamingCardCompleted || c.phase == StreamingCardAborted {
		c.mu.Unlock()
		return
	}
	toolUseID = strings.TrimSpace(toolUseID)
	toolName = strings.TrimSpace(toolName)
	if toolUseID == "" && toolName == "" {
		c.mu.Unlock()
		return
	}
	index := c.findToolStepLocked(toolUseID, toolName)
	if index >= 0 {
		if toolName != "" {
			c.toolSteps[index].Name = toolName
		}
		if toolUseID != "" {
			c.toolSteps[index].ID = toolUseID
		}
		if c.toolSteps[index].Status == "" || c.toolSteps[index].Status == "running" {
			c.toolSteps[index].Status = "running"
		}
	} else {
		c.toolSteps = append(c.toolSteps, ToolStep{ID: toolUseID, Name: toolName, Status: "running"})
	}
	throttle := c.currentThrottleLocked()
	c.mu.Unlock()
	_ = c.flushController.ThrottledUpdate(throttle)
}

func (c *StreamingCard) CompleteTool(toolUseID string, toolName string) {
	c.finishTool(toolUseID, toolName, "done")
}

func (c *StreamingCard) FailTool(toolUseID string, toolName string) {
	c.finishTool(toolUseID, toolName, "failed")
}

func (c *StreamingCard) finishTool(toolUseID string, toolName string, status string) {
	c.mu.Lock()
	if c.phase == StreamingCardCompleted || c.phase == StreamingCardAborted {
		c.mu.Unlock()
		return
	}
	toolUseID = strings.TrimSpace(toolUseID)
	toolName = strings.TrimSpace(toolName)
	if toolUseID == "" && toolName == "" {
		c.mu.Unlock()
		return
	}
	index := c.findToolStepLocked(toolUseID, toolName)
	if index >= 0 {
		if toolName != "" {
			c.toolSteps[index].Name = toolName
		}
		if toolUseID != "" {
			c.toolSteps[index].ID = toolUseID
		}
		c.toolSteps[index].Status = status
	} else {
		c.toolSteps = append(c.toolSteps, ToolStep{ID: toolUseID, Name: toolName, Status: status})
	}
	throttle := c.currentThrottleLocked()
	c.mu.Unlock()
	_ = c.flushController.ThrottledUpdate(throttle)
}

func (c *StreamingCard) FlushNow(ctx context.Context) error {
	return c.performFlush(ctx)
}

func (c *StreamingCard) Finalize(ctx context.Context) error {
	c.mu.Lock()
	if c.phase == StreamingCardCompleted || c.phase == StreamingCardAborted {
		c.mu.Unlock()
		return nil
	}
	if c.phase == StreamingCardIdle {
		c.phase = StreamingCardCompleted
		c.mu.Unlock()
		c.flushController.Complete()
		return nil
	}
	c.phase = StreamingCardFinalizing
	c.mu.Unlock()
	c.flushController.CancelPendingFlush()
	c.flushController.WaitForFlush()

	c.ioMu.Lock()
	defer c.ioMu.Unlock()

	c.mu.Lock()
	if c.phase == StreamingCardCompleted || c.phase == StreamingCardAborted {
		c.mu.Unlock()
		return nil
	}
	finalText := c.terminalTextLocked()
	cardID := c.cardID
	messageID := c.messageID
	var closeSequence int64
	var updateSequence int64
	if cardID != "" {
		c.sequence++
		closeSequence = c.sequence
		c.sequence++
		updateSequence = c.sequence
	}
	c.mu.Unlock()

	var closeErr error
	var deliveryErr error
	if cardID != "" {
		if setErr := c.client.SetCardStreamingMode(ctx, cardID, false, closeSequence); setErr != nil {
			closeErr = setErr
		}
		// CardKit merges element trees across updates. Keeping the original
		// markdown element and element_id prevents stale process fragments.
		deliveryErr = c.client.UpdateCardKitCard(ctx, cardID, BuildFinalCardKitCard(finalText), updateSequence)
		if deliveryErr != nil && messageID != "" {
			if patchErr := c.client.PatchMessageCard(ctx, messageID, BuildRenderedCard(finalText)); patchErr == nil {
				deliveryErr = nil
			} else {
				deliveryErr = errors.Join(deliveryErr, patchErr)
			}
		}
	} else if messageID != "" {
		deliveryErr = c.client.PatchMessageCard(ctx, messageID, BuildRenderedCard(finalText))
	} else {
		deliveryErr = errors.New("feishu streaming card has no message to finalize")
	}

	var fallbackMessageID string
	if deliveryErr != nil {
		var fallbackErr error
		fallbackMessageID, fallbackErr = c.client.SendRenderedCardAsMessage(ctx, c.chatID, BuildRenderedCard(finalText), "")
		if fallbackErr == nil {
			deliveryErr = nil
		} else {
			deliveryErr = errors.Join(deliveryErr, fallbackErr)
		}
	}
	if deliveryErr != nil {
		return errors.Join(closeErr, deliveryErr)
	}
	c.mu.Lock()
	if fallbackMessageID != "" {
		c.cardID = ""
		c.messageID = fallbackMessageID
		c.cardKitStreamActive = false
	}
	c.phase = StreamingCardCompleted
	c.lastFlushedText = finalText
	c.mu.Unlock()
	c.flushController.Complete()
	return nil
}

func (c *StreamingCard) Abort(ctx context.Context, message string) error {
	c.mu.Lock()
	if c.phase == StreamingCardCompleted || c.phase == StreamingCardAborted {
		c.mu.Unlock()
		return nil
	}
	if c.phase == StreamingCardIdle {
		c.phase = StreamingCardAborted
		c.mu.Unlock()
		c.flushController.Complete()
		return nil
	}

	c.phase = StreamingCardAborted
	c.mu.Unlock()
	c.flushController.CancelPendingFlush()
	c.flushController.WaitForFlush()
	c.ioMu.Lock()
	defer c.ioMu.Unlock()

	c.mu.Lock()

	errorText := strings.TrimSpace(message)
	if errorText == "" {
		errorText = "运行失败"
	}
	partialText := strings.TrimSpace(c.accumulatedText)
	if partialText == "" && c.hasAnyContentLocked() {
		partialText = c.renderedTextLocked()
	}
	if partialText != "" {
		errorText += "\n\n---\n\n" + partialText
	}
	errorText = OptimizeMarkdownForFeishu(SanitizeTextForCard(errorText, FeishuCardTableLimit), 2)
	cardID := c.cardID
	messageID := c.messageID
	var closeSequence int64
	var updateSequence int64
	if cardID != "" {
		c.sequence++
		closeSequence = c.sequence
		c.sequence++
		updateSequence = c.sequence
	}
	c.mu.Unlock()

	var err error
	if cardID != "" {
		if setErr := c.client.SetCardStreamingMode(ctx, cardID, false, closeSequence); setErr != nil {
			err = setErr
		}
		if updateErr := c.client.UpdateCardKitCard(ctx, cardID, BuildErrorCard(errorText), updateSequence); updateErr != nil && err == nil {
			err = updateErr
		}
	} else if messageID != "" {
		err = c.client.PatchMessageCard(ctx, messageID, BuildErrorCard(errorText))
	}

	c.mu.Lock()
	c.lastFlushedText = errorText
	c.mu.Unlock()
	c.flushController.Complete()
	return err
}

func (c *StreamingCard) Phase() StreamingCardPhase {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.phase
}

func (c *StreamingCard) CardID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cardID
}

func (c *StreamingCard) MessageID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.messageID
}

func (c *StreamingCard) Sequence() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sequence
}

func (c *StreamingCard) CardKitStreamActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cardKitStreamActive
}

func (c *StreamingCard) hasAnyContentLocked() bool {
	return c.accumulatedText != "" || c.accumulatedReasoningText != "" || len(c.toolSteps) > 0
}

func (c *StreamingCard) currentThrottleLocked() time.Duration {
	if c.cardKitStreamActive {
		return CardKitThrottle
	}
	return PatchThrottle
}

func (c *StreamingCard) renderedText() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.renderedTextLocked()
}

func (c *StreamingCard) renderedTextLocked() string {
	sections := make([]string, 0, 3)
	if tools := c.renderedToolStepsLocked(); tools != "" {
		sections = append(sections, tools)
	}
	if reasoning := truncateReasoningPreview(c.accumulatedReasoningText, ReasoningPreviewChars); strings.TrimSpace(reasoning) != "" {
		sections = append(sections, "💭 **思考中**\n\n"+reasoning)
	}
	if strings.TrimSpace(c.accumulatedText) != "" {
		sections = append(sections, c.accumulatedText)
	}
	text := OptimizeMarkdownForFeishu(SanitizeTextForCard(strings.Join(sections, "\n\n---\n\n"), FeishuCardTableLimit), 2)
	if strings.TrimSpace(text) == "" {
		return "正在思考中..."
	}
	return text
}

func (c *StreamingCard) terminalTextLocked() string {
	text := OptimizeMarkdownForFeishu(SanitizeTextForCard(c.accumulatedText, FeishuCardTableLimit), 2)
	if strings.TrimSpace(text) == "" {
		return c.renderedTextLocked()
	}
	return text
}

func (c *StreamingCard) findToolStepLocked(toolUseID string, toolName string) int {
	if toolUseID != "" {
		for index, step := range c.toolSteps {
			if step.ID == toolUseID {
				return index
			}
		}
	}
	if toolName != "" {
		for index, step := range c.toolSteps {
			if step.Name == toolName {
				return index
			}
		}
	}
	return -1
}

func (c *StreamingCard) renderedToolStepsLocked() string {
	if len(c.toolSteps) == 0 {
		return ""
	}
	parts := make([]string, 0, len(c.toolSteps))
	for _, step := range c.toolSteps {
		name := strings.TrimSpace(step.Name)
		if name == "" {
			name = strings.TrimSpace(step.ID)
		}
		if name == "" {
			continue
		}
		marker := "⚙️"
		switch step.Status {
		case "done":
			marker = "✅"
		case "failed":
			marker = "❌"
		}
		parts = append(parts, marker+" "+name)
	}
	if len(parts) == 0 {
		return ""
	}
	return "🛠️ " + strings.Join(parts, " · ")
}

func truncateReasoningPreview(text string, limit int) string {
	text = strings.TrimSpace(text)
	if text == "" || limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit <= 3 {
		return string(runes[len(runes)-limit:])
	}
	return "..." + string(runes[len(runes)-limit+3:])
}

func mergeStreamingText(accumulated string, delta string) string {
	if delta == "" {
		return accumulated
	}
	if accumulated == "" {
		return delta
	}
	if delta == accumulated || strings.HasSuffix(accumulated, delta) {
		return accumulated
	}
	if strings.HasPrefix(delta, accumulated) {
		return delta
	}

	accumulatedRunes := []rune(accumulated)
	deltaRunes := []rune(delta)
	maxOverlap := min(len(accumulatedRunes), len(deltaRunes))
	for overlap := maxOverlap; overlap >= 8; overlap-- {
		if string(accumulatedRunes[len(accumulatedRunes)-overlap:]) == string(deltaRunes[:overlap]) {
			return accumulated + string(deltaRunes[overlap:])
		}
	}
	return accumulated + delta
}

func (c *StreamingCard) performFlush(ctx context.Context) error {
	c.ioMu.Lock()
	defer c.ioMu.Unlock()

	c.mu.Lock()
	if c.phase != StreamingCardStreaming || c.messageID == "" {
		c.mu.Unlock()
		return nil
	}
	finalText := c.renderedTextLocked()
	if finalText == c.lastFlushedText {
		c.mu.Unlock()
		return nil
	}
	cardID := c.cardID
	messageID := c.messageID
	cardKitStreamActive := c.cardKitStreamActive
	var sequence int64
	if cardID != "" && cardKitStreamActive {
		c.sequence++
		sequence = c.sequence
	}
	c.mu.Unlock()

	if cardID != "" && cardKitStreamActive {
		err := c.client.StreamCardContent(ctx, cardID, StreamingElementID, finalText, sequence)
		if err != nil {
			if IsCardRateLimitError(err) {
				return nil
			}
			if IsCardTableLimitError(err) {
				c.mu.Lock()
				if c.cardID == cardID {
					c.cardKitStreamActive = false
				}
				c.mu.Unlock()
				return nil
			}
			return err
		}
		c.mu.Lock()
		c.lastFlushedText = finalText
		c.mu.Unlock()
		return nil
	}
	if cardID != "" && !cardKitStreamActive {
		return nil
	}
	if err := c.client.PatchMessageCard(ctx, messageID, BuildRenderedCard(finalText)); err != nil {
		if IsCardRateLimitError(err) {
			return nil
		}
		return err
	}
	c.mu.Lock()
	c.lastFlushedText = finalText
	c.mu.Unlock()
	return nil
}
