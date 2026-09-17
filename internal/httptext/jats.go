package httptext

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

const (
	maxJATSXMLDepth  = 256
	maxJATSXMLTokens = 1_000_000
)

// JATSDocument creates a deterministic reading projection from journal XML.
// It keeps the abstract, body, tables, figure captions, links and references
// in document order while excluding author/affiliation front matter. The
// source XML remains authoritative and is never rewritten by this view.
func JATSDocument(ctx context.Context, source string) (Document, error) {
	decoder := xml.NewDecoder(documentReader{ctx, strings.NewReader(source)})
	decoder.Strict = true
	writer := documentWriter{}
	var title strings.Builder
	stack := []jatsFrame{}
	readableDepth := 0
	sectionDepth := 0
	tokens := 0

	for {
		if err := context.Cause(ctx); err != nil {
			return Document{}, err
		}
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Document{}, err
		}
		tokens++
		if tokens > maxJATSXMLTokens {
			return Document{}, errors.New("JATS XML token limit exceeded")
		}
		switch value := token.(type) {
		case xml.StartElement:
			if len(stack) >= maxJATSXMLDepth {
				return Document{}, errors.New("JATS XML depth limit exceeded")
			}
			name := strings.ToLower(value.Name.Local)
			frame := jatsFrame{name: name, href: jatsLink(value.Attr)}
			if name == "article-title" && readableDepth == 0 {
				frame.articleTitle = true
			}
			if name == "abstract" || name == "body" || name == "ref-list" {
				frame.readableRegion = true
				if readableDepth == 0 {
					writer.newline()
					switch name {
					case "abstract":
						writer.literal("Abstract\n")
					case "body":
						writer.literal("Body\n")
					case "ref-list":
						writer.literal("References\n")
					}
				}
				readableDepth++
			}
			if readableDepth > 0 {
				if name == "sec" {
					sectionDepth++
					frame.section = true
					writer.newline()
				}
				switch name {
				case "title":
					if jatsStackContains(stack, "sec") {
						writer.newline()
						writer.literal(strings.Repeat("#", min(sectionDepth+1, 6)) + " ")
					}
				case "p", "caption", "fig", "table-wrap", "table", "tr", "list-item", "ref", "mixed-citation", "element-citation":
					writer.newline()
				case "sup":
					writer.literal("^(")
				case "sub":
					writer.literal("_(")
				}
			}
			stack = append(stack, frame)
		case xml.CharData:
			if jatsStackHasArticleTitle(stack) {
				title.Write(value)
			}
			if readableDepth > 0 {
				writer.text(string(value))
			}
		case xml.EndElement:
			if len(stack) == 0 {
				continue
			}
			frame := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if readableDepth > 0 {
				switch frame.name {
				case "ext-link", "uri":
					writer.link(frame.href)
				case "sup", "sub":
					writer.literal(")")
				case "td", "th":
					writer.literal("\t")
				case "title", "p", "caption", "fig", "table-wrap", "table", "tr", "list-item", "ref", "mixed-citation", "element-citation", "sec":
					writer.newline()
				}
			}
			if frame.section && sectionDepth > 0 {
				sectionDepth--
			}
			if frame.readableRegion && readableDepth > 0 {
				readableDepth--
			}
		}
	}

	return Document{
		Title: strings.Join(strings.Fields(title.String()), " "),
		Text:  strings.TrimSpace(writer.out.String()),
		Links: writer.links,
	}, nil
}

type jatsFrame struct {
	name           string
	href           string
	articleTitle   bool
	readableRegion bool
	section        bool
}

func jatsStackHasArticleTitle(stack []jatsFrame) bool {
	for index := len(stack) - 1; index >= 0; index-- {
		if stack[index].articleTitle {
			return true
		}
	}
	return false
}

func jatsStackContains(stack []jatsFrame, name string) bool {
	for index := len(stack) - 1; index >= 0; index-- {
		if stack[index].name == name {
			return true
		}
	}
	return false
}

func jatsLink(attributes []xml.Attr) string {
	for _, attribute := range attributes {
		if strings.EqualFold(attribute.Name.Local, "href") {
			return strings.TrimSpace(attribute.Value)
		}
	}
	return ""
}
