package feishu

import (
	"strings"
	"testing"
)

func TestBuildInitialStreamingCard(t *testing.T) {
	card := BuildInitialStreamingCard()
	if card["schema"] != "2.0" {
		t.Fatalf("schema = %#v", card["schema"])
	}
	config := card["config"].(map[string]any)
	if config["streaming_mode"] != true || config["update_multi"] != true {
		t.Fatalf("config = %#v", config)
	}
	element := firstCardElement(t, card)
	if element["tag"] != "markdown" || element["element_id"] != StreamingElementID || element["text_size"] != "body" {
		t.Fatalf("streaming element = %#v", element)
	}
	if content, _ := element["content"].(string); content == "" || content == " " {
		t.Fatalf("streaming content = %q", content)
	}
}

func TestBuildFinalCardKitCardNormalizesMarkdownHeadings(t *testing.T) {
	card := BuildFinalCardKitCard("## 标题\n\n**你好**\n\n```md\n# 保留\n```")
	element := firstCardElement(t, card)
	if element["tag"] != "markdown" || element["element_id"] != StreamingElementID {
		t.Fatalf("final card element = %#v", element)
	}
	content := element["content"].(string)
	if content == "## 标题" || content == "**你好**" {
		t.Fatalf("content was not normalized = %q", content)
	}
	if !containsAll(content, []string{"标题", "你好", "# 保留"}) {
		t.Fatalf("content = %q", content)
	}
}

func TestBuildRenderedCardUsesCompactLarkMD(t *testing.T) {
	card := BuildRenderedCard("# 标题\n\n![alt](https://example.com/a.png)\n\n[link](https://example.com)")
	element := firstCardElement(t, card)
	if element["tag"] != "div" {
		t.Fatalf("rendered element = %#v", element)
	}
	text := element["text"].(map[string]any)
	if text["tag"] != "lark_md" {
		t.Fatalf("rendered text = %#v", text)
	}
	content := text["content"].(string)
	if !containsAll(content, []string{"标题", "alt", "[link](https://example.com)"}) {
		t.Fatalf("rendered content = %q", content)
	}
}

func TestBuildErrorCard(t *testing.T) {
	card := BuildErrorCard("oops")
	header := card["header"].(map[string]any)
	if header["template"] != "red" {
		t.Fatalf("header = %#v", header)
	}
	element := firstCardElement(t, card)
	if element["content"] != "oops" {
		t.Fatalf("error body = %#v", element)
	}
}

func firstCardElement(t *testing.T, card map[string]any) map[string]any {
	t.Helper()
	body := card["body"].(map[string]any)
	elements := body["elements"].([]any)
	if len(elements) == 0 {
		t.Fatal("missing elements")
	}
	element, ok := elements[0].(map[string]any)
	if !ok {
		t.Fatalf("element is not map: %#v", elements[0])
	}
	return element
}

func containsAll(text string, needles []string) bool {
	for _, needle := range needles {
		if !strings.Contains(text, needle) {
			return false
		}
	}
	return true
}
