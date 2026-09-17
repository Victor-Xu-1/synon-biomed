package websearch

import (
	"strings"

	"golang.org/x/net/html"
)

// Search-result markup owns the title/snippet association. Pairing independent
// regex match lists by index shifts evidence whenever one result lacks a
// snippet; extracting titles alone discards evidence already in the response.
func parseDuckDuckGoHTMLResults(body string) []scriptResult {
	document, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return nil
	}
	var results []scriptResult
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "a" && searchHTMLHasClass(node, "result__a") {
			link := normalizeSearchResultURL(searchHTMLAttribute(node, "href"))
			title := searchHTMLText(node)
			if link != "" && title != "" {
				results = append(results, scriptResult{Title: title, URL: link, Snippet: searchHTMLText(searchHTMLSnippet(node))})
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(document)
	return results
}

func searchHTMLSnippet(anchor *html.Node) *html.Node {
	for parent := anchor.Parent; parent != nil; parent = parent.Parent {
		if searchHTMLHasClass(parent, "result") {
			return searchHTMLFirstClass(parent, "result__snippet")
		}
	}
	// Minimal HTML results may be siblings without a result wrapper. Stay in
	// that parent and never borrow a snippet from the following result.
	for sibling := anchor.NextSibling; sibling != nil; sibling = sibling.NextSibling {
		if searchHTMLFirstClass(sibling, "result__a") != nil {
			break
		}
		if snippet := searchHTMLFirstClass(sibling, "result__snippet"); snippet != nil {
			return snippet
		}
	}
	return nil
}

func searchHTMLFirstClass(node *html.Node, class string) *html.Node {
	if searchHTMLHasClass(node, class) {
		return node
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if found := searchHTMLFirstClass(child, class); found != nil {
			return found
		}
	}
	return nil
}

func searchHTMLHasClass(node *html.Node, class string) bool {
	for _, candidate := range strings.Fields(searchHTMLAttribute(node, "class")) {
		if candidate == class {
			return true
		}
	}
	return false
}

func searchHTMLAttribute(node *html.Node, key string) string {
	for _, attribute := range node.Attr {
		if attribute.Key == key {
			return attribute.Val
		}
	}
	return ""
}

func searchHTMLText(node *html.Node) string {
	if node == nil {
		return ""
	}
	var text strings.Builder
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		if node.Type == html.TextNode {
			text.WriteString(node.Data)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(node)
	return normalizeSpace(text.String())
}
