package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type sourcePreviewTestAuthority struct {
	recordingLargeToolResultAuthority
	calls int
	edit  func(*LargeToolResultDescriptor)
}

func (a *sourcePreviewTestAuthority) Externalize(ctx context.Context, input LargeToolResultInput) (LargeToolResultDescriptor, error) {
	d, err := a.recordingLargeToolResultAuthority.Externalize(ctx, input)
	d.Preview = "Source view: " + string(input.RawJSON[len(input.RawJSON)-64:])
	if a.edit != nil {
		a.edit(&d)
	}
	return d, err
}

func (a *sourcePreviewTestAuthority) ValidatePreview(ctx context.Context, input LargeToolResultInput, d LargeToolResultDescriptor) error {
	a.calls++
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.Preview != "Source view: "+string(input.RawJSON[len(input.RawJSON)-64:]) {
		return errors.New("derived view does not match original")
	}
	return nil
}

func TestEngineDerivedPreviewRequiresTrustedValidator(t *testing.T) {
	raw := json.RawMessage(`{"ok":true,"content":"` + strings.Repeat("Original 科学 ", 500) + `"}`)
	for _, trusted := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_validator", true: "trusted_validator"}[trusted], func(t *testing.T) {
			authority := &sourcePreviewTestAuthority{}
			var registered LargeToolResultAuthority = authority
			if !trusted {
				registered = FuncLargeToolResultAuthority(authority.Externalize)
			}
			engine := Engine{MaxToolResultBytes: 1024, LargeToolResults: registered}
			_, err := engine.MaterializeRawToolResult(context.Background(), ToolCall{ID: "view", Name: "fetch"}, raw, ToolResultSucceeded)
			if trusted && (err != nil || authority.calls != 1) {
				t.Fatalf("trusted view failed: calls=%d err=%v", authority.calls, err)
			}
			if !trusted && (err == nil || authority.calls != 0) {
				t.Fatal("non-prefix preview accepted without trusted runtime validator")
			}
		})
	}
}

func TestEngineDerivedPreviewCannotBypassSourceOrTransportValidation(t *testing.T) {
	raw := json.RawMessage(`{"ok":true,"content":"` + strings.Repeat("Original data ", 500) + `"}`)
	cases := []struct {
		name               string
		edit               func(*LargeToolResultDescriptor)
		wantValidatorCalls int
	}{
		{"hash", func(d *LargeToolResultDescriptor) { d.SHA256 = strings.Repeat("0", 64) }, 0},
		{"size", func(d *LargeToolResultDescriptor) { d.SizeBytes++ }, 0},
		{"outcome", func(d *LargeToolResultDescriptor) { d.Outcome = ToolResultUnavailable }, 0},
		{"source_url", func(d *LargeToolResultDescriptor) { d.ContentURL += "/other" }, 0},
		{"utf8", func(d *LargeToolResultDescriptor) { d.Preview = string([]byte{255}) }, 0},
		{"budget", func(d *LargeToolResultDescriptor) { d.Preview = strings.Repeat("view", 1000) }, 0},
		{"content", func(d *LargeToolResultDescriptor) { d.Preview = "invented source content" }, 1},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			authority := &sourcePreviewTestAuthority{edit: test.edit}
			engine := Engine{MaxToolResultBytes: 1024, LargeToolResults: authority}
			_, err := engine.MaterializeRawToolResult(context.Background(), ToolCall{ID: "tamper", Name: "fetch"}, raw, ToolResultSucceeded)
			if err == nil || authority.calls != test.wantValidatorCalls {
				t.Fatalf("invalid %s accepted or bypassed engine: calls=%d err=%v", test.name, authority.calls, err)
			}
		})
	}
}
