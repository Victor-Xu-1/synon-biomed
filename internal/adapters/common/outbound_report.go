package common

import "strings"

type OutboundReport struct {
	Platform        string               `json:"platform"`
	MessageKey      string               `json:"messageKey"`
	MessageType     string               `json:"messageType,omitempty"`
	RenderMode      string               `json:"renderMode,omitempty"`
	MessageLimit    int                  `json:"messageLimit,omitempty"`
	Policy          OutboundRenderPolicy `json:"policy,omitempty"`
	Chunks          int                  `json:"chunks"`
	TextParts       int                  `json:"textParts"`
	PlatformCalls   int                  `json:"platformCalls"`
	MediaUploads    int                  `json:"mediaUploads"`
	CardUpdates     int                  `json:"cardUpdates"`
	Completed       bool                 `json:"completed"`
	Skipped         bool                 `json:"skipped"`
	Error           string               `json:"error,omitempty"`
	DeliverySummary string               `json:"deliverySummary"`
}

type OutboundRenderPolicy struct {
	Platform           string   `json:"platform"`
	RenderMode         string   `json:"renderMode"`
	MessageLimit       int      `json:"messageLimit,omitempty"`
	SupportsMarkdown   bool     `json:"supportsMarkdown"`
	SupportsMedia      bool     `json:"supportsMedia"`
	SupportsCards      bool     `json:"supportsCards"`
	MediaStrategy      string   `json:"mediaStrategy,omitempty"`
	CompletionStrategy string   `json:"completionStrategy,omitempty"`
	Safety             []string `json:"safety,omitempty"`
}

func NewOutboundReport(platform string, message ServerMessage, renderMode string, messageLimit int) OutboundReport {
	return NewOutboundReportWithPolicy(platform, message, OutboundRenderPolicy{
		RenderMode:   renderMode,
		MessageLimit: messageLimit,
	})
}

func NewOutboundReportWithPolicy(platform string, message ServerMessage, policy OutboundRenderPolicy) OutboundReport {
	platform = strings.TrimSpace(platform)
	policy = policy.normalized(platform)
	return OutboundReport{
		Platform:     platform,
		MessageKey:   MessageDeliveryKey(message),
		MessageType:  strings.TrimSpace(stringValue(message["type"])),
		RenderMode:   policy.RenderMode,
		MessageLimit: policy.MessageLimit,
		Policy:       policy,
	}
}

func (p OutboundRenderPolicy) normalized(platform string) OutboundRenderPolicy {
	p.Platform = strings.TrimSpace(defaultString(p.Platform, platform))
	p.RenderMode = strings.TrimSpace(p.RenderMode)
	p.MediaStrategy = strings.TrimSpace(p.MediaStrategy)
	p.CompletionStrategy = strings.TrimSpace(p.CompletionStrategy)
	if len(p.Safety) > 0 {
		safety := make([]string, 0, len(p.Safety))
		for _, item := range p.Safety {
			item = strings.TrimSpace(item)
			if item != "" {
				safety = append(safety, item)
			}
		}
		p.Safety = safety
	}
	return p
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func (r *OutboundReport) ObserveChunk(chunk OutboundChunk) {
	if r == nil {
		return
	}
	r.Chunks++
	if chunk.Complete {
		r.Completed = true
	}
}

func (r *OutboundReport) AddTextPart() {
	if r == nil {
		return
	}
	r.TextParts++
	r.PlatformCalls++
}

func (r *OutboundReport) AddTextPartOnly() {
	if r == nil {
		return
	}
	r.TextParts++
}

func (r *OutboundReport) AddMediaUpload() {
	if r == nil {
		return
	}
	r.MediaUploads++
	r.PlatformCalls++
}

func (r *OutboundReport) AddCardUpdate() {
	if r == nil {
		return
	}
	r.CardUpdates++
	r.PlatformCalls++
}

func (r *OutboundReport) Finish(err error) (OutboundReport, error) {
	if r == nil {
		if err != nil {
			return OutboundReport{Error: RedactOutboundText(err.Error()), DeliverySummary: "failed"}, err
		}
		return OutboundReport{Skipped: true, DeliverySummary: "skipped"}, nil
	}
	if err != nil {
		r.Error = RedactOutboundText(err.Error())
		r.DeliverySummary = "failed"
		return *r, err
	}
	if r.Chunks == 0 || r.PlatformCalls == 0 && !r.Completed {
		r.Skipped = true
		r.DeliverySummary = "skipped"
		return *r, nil
	}
	if r.Completed {
		r.DeliverySummary = "completed"
		return *r, nil
	}
	r.DeliverySummary = "delivered"
	return *r, nil
}

func (r *OutboundReport) RedactError(secrets ...string) {
	if r == nil || r.Error == "" {
		return
	}
	r.Error = RedactOutboundText(r.Error, secrets...)
}
