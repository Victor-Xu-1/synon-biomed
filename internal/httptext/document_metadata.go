package httptext

import (
	"context"
	"encoding/json"
	"strings"

	"golang.org/x/net/html"
)

// These are publisher assertions, not verified citations or fetched documents.
// Preserve the vocabulary and document order instead of interpreting scientific
// claims or inventing destinations for JavaScript-only navigation.
type DocumentLink struct {
	Relation string `json:"relation"`
	URL      string `json:"url"`
	Type     string `json:"type,omitempty"`
	Language string `json:"language,omitempty"`
	Title    string `json:"title,omitempty"`
}

type DocumentMetadata struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

func documentAttribute(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func (w *documentWriter) headMetadata(ctx context.Context, head *html.Node) error {
	for n := head.FirstChild; n != nil; n = n.NextSibling {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		if n.Type != html.ElementNode {
			continue
		}
		switch n.Data {
		case "meta":
			name := documentAttribute(n, "name")
			if name == "" {
				name = documentAttribute(n, "property")
			}
			key := strings.ToLower(strings.TrimSpace(name))
			value := strings.TrimSpace(documentAttribute(n, "content"))
			if value == "" || !documentMetadataVocabulary(key) {
				continue
			}
			if strings.HasSuffix(key, "_url") || key == "og:url" {
				w.metadataLink(DocumentLink{Relation: key}, value)
				continue
			}
			w.metadata = append(w.metadata, DocumentMetadata{Name: name, Content: value})
		case "link":
			for _, rel := range strings.Fields(strings.ToLower(documentAttribute(n, "rel"))) {
				// HTML relationship vocabulary, not site/topic matching. Asset
				// preloads and executable resources are not document navigation.
				switch rel {
				case "canonical", "alternate", "author", "license", "next", "prev", "describedby":
					w.metadataLink(DocumentLink{
						Relation: rel, Type: documentAttribute(n, "type"),
						Language: documentAttribute(n, "hreflang"), Title: documentAttribute(n, "title"),
					}, documentAttribute(n, "href"))
				}
			}
		}
	}
	return nil
}

func documentMetadataVocabulary(name string) bool {
	for _, prefix := range []string{"citation_", "bepress_citation_", "dc.", "dcterms.", "prism.", "og:"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func (w *documentWriter) metadataLink(link DocumentLink, href string) {
	u := w.resolveLink(href)
	if u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return
	}
	link.URL = u.String()
	if w.seenLinks == nil {
		w.seenLinks = make(map[DocumentLink]struct{})
	}
	if _, seen := w.seenLinks[link]; seen {
		return
	}
	w.seenLinks[link] = struct{}{}
	w.links = append(w.links, link)
}

// ReadingText appends document navigation and bibliography without moving any
// existing body line. Previously issued continuation offsets must remain valid
// across an upgrade. JSON quoting keeps publisher text as data, not instructions.
func (d Document) ReadingText() string {
	if len(d.Links) == 0 && len(d.Metadata) == 0 {
		return d.Text
	}
	var out strings.Builder
	out.WriteString(d.Text)
	if len(d.Links) > 0 {
		out.WriteString("\nSource-declared document links (not fetched):\n")
		for _, link := range d.Links {
			encoded, _ := json.Marshal(link)
			out.Write(encoded)
			out.WriteByte('\n')
		}
	}
	if len(d.Metadata) > 0 {
		out.WriteString("\nSource-declared document metadata:\n")
		for _, meta := range d.Metadata {
			encoded, _ := json.Marshal(meta)
			out.Write(encoded)
			out.WriteByte('\n')
		}
	}
	return strings.TrimSpace(out.String())
}
