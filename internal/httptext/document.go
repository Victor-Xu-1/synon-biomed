package httptext

import (
	"context"
	"io"
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

// Document is a deterministic reading view, not a summary or a replacement
// for the source. Static body text is retained in document order. HTML
// tags, never domain names, CSS class heuristics or topic words, define layout.
type Document struct {
	Title    string
	Text     string
	Links    []DocumentLink
	Metadata []DocumentMetadata
}

func HTMLDocument(ctx context.Context, source, sourceURL string) (Document, error) {
	root, err := html.Parse(documentReader{ctx, strings.NewReader(source)})
	if err != nil {
		return Document{}, err
	}
	base, _ := url.Parse(sourceURL)
	w := documentWriter{base: base}
	var title strings.Builder
	var visit func(*html.Node, bool, bool) error
	visit = func(n *html.Node, inTitle, pre bool) error {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		if n.Type == html.TextNode {
			if inTitle {
				title.WriteString(n.Data)
			} else if pre {
				w.literal(n.Data)
			} else {
				w.text(n.Data)
			}
			return nil
		}
		if n.Type == html.ElementNode {
			for _, a := range n.Attr {
				if a.Key == "hidden" {
					return nil
				}
			}
			switch n.Data {
			case "script", "style", "template":
				return nil
			case "head":
				baseFound := false
				for child := n.FirstChild; child != nil; child = child.NextSibling {
					if err := context.Cause(ctx); err != nil {
						return err
					}
					if child.Type == html.ElementNode && child.Data == "base" && !baseFound {
						for _, attr := range child.Attr {
							if attr.Key == "href" {
								baseFound = true
								if resolved := w.resolveLink(attr.Val); resolved != nil {
									w.base = resolved
								}
							}
						}
					}
					if child.Type == html.ElementNode && child.Data == "title" {
						if err := visit(child, true, false); err != nil {
							return err
						}
					}
				}
				// HTML base applies to metadata even when it follows the link.
				return w.headMetadata(ctx, n)
			case "br", "hr":
				w.newline()
				return nil
			case "math":
				// Retain MathML structure rather than flattening operators,
				// fractions and scripts into an ambiguous sequence of tokens.
				var formula strings.Builder
				if err := html.Render(&formula, n); err != nil {
					return err
				}
				w.literal(formula.String())
				return nil
			case "img":
				for _, a := range n.Attr {
					if a.Key == "alt" {
						w.text(a.Val)
					}
				}
				for _, a := range n.Attr {
					if a.Key == "src" {
						w.link(a.Val)
					}
				}
				return nil
			}
			if documentBlock(n.Data) {
				w.newline()
			}
			pre = pre || n.Data == "pre"
			if n.Data == "sup" {
				w.literal("^(")
			}
			if n.Data == "sub" {
				w.literal("_(")
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if err := visit(child, inTitle, pre); err != nil {
				return err
			}
		}
		if n.Type == html.ElementNode && !inTitle {
			if n.Data == "sup" || n.Data == "sub" {
				w.literal(")")
			}
			if n.Data == "a" {
				for _, a := range n.Attr {
					if a.Key == "href" {
						w.link(a.Val)
					}
				}
			}
			if n.Data == "td" || n.Data == "th" {
				for _, attr := range n.Attr {
					if attr.Key == "colspan" || attr.Key == "rowspan" {
						w.literal(" [" + attr.Key + "=" + attr.Val + "]")
					}
				}
				w.literal("\t")
			}
			if documentBlock(n.Data) {
				w.newline()
			}
		}
		return nil
	}
	if err := visit(root, false, false); err != nil {
		return Document{}, err
	}
	return Document{Title: strings.Join(strings.Fields(title.String()), " "), Text: strings.TrimSpace(w.out.String()), Links: w.links, Metadata: w.metadata}, nil
}

type documentReader struct {
	ctx context.Context
	io.Reader
}

func (r documentReader) Read(p []byte) (int, error) {
	if err := context.Cause(r.ctx); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

type documentWriter struct {
	base      *url.URL
	out       strings.Builder
	space     bool
	links     []DocumentLink
	metadata  []DocumentMetadata
	seenLinks map[DocumentLink]struct{}
}

func (w *documentWriter) literal(text string) { w.out.WriteString(text); w.space = false }
func (w *documentWriter) newline() {
	if w.out.Len() > 0 && !strings.HasSuffix(w.out.String(), "\n") {
		w.out.WriteByte('\n')
	}
	w.space = false
}
func (w *documentWriter) text(text string) {
	for _, r := range text {
		if unicode.IsSpace(r) {
			w.space = true
			continue
		}
		if w.space && w.out.Len() > 0 && !strings.HasSuffix(w.out.String(), "\n") && !strings.HasSuffix(w.out.String(), "\t") {
			w.out.WriteByte(' ')
		}
		w.out.WriteRune(r)
		w.space = false
	}
}
func (w *documentWriter) link(href string) {
	if u := w.resolveLink(href); u != nil {
		w.literal(" <" + u.String() + ">")
	}
}

func (w *documentWriter) resolveLink(href string) *url.URL {
	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil || href == "" {
		return nil
	}
	if w.base != nil {
		u = w.base.ResolveReference(u)
	}
	if u.Scheme != "" && u.Scheme != "https" && u.Scheme != "http" {
		return nil
	}
	if u.User != nil {
		return nil
	}
	return u
}
func documentBlock(tag string) bool {
	switch tag {
	case "address", "article", "aside", "blockquote", "caption", "dd", "details", "dialog", "div", "dl", "dt", "fieldset", "figcaption", "figure", "footer", "form", "h1", "h2", "h3", "h4", "h5", "h6", "header", "li", "main", "nav", "ol", "p", "pre", "section", "summary", "table", "tbody", "thead", "tfoot", "tr", "ul":
		return true
	default:
		return false
	}
}
