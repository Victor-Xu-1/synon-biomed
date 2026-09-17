package server

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

func convertWebDOCXToMarkdown(filePath string) (string, error) {
	archive, err := openWebOOXMLArchive(filePath)
	if err != nil {
		return "", err
	}
	defer archive.Close()
	return convertWebDOCXArchiveToMarkdown(archive)
}

func convertWebDOCXArchiveToMarkdown(archive *webOOXMLArchive) (string, error) {
	raw, err := archive.ReadPart("word/document.xml", maxWebFSReadBytes)
	if err != nil {
		return "", err
	}
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	var output strings.Builder
	var paragraph strings.Builder
	inParagraph := false
	inText := false
	heading := 0
	tokens := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("parse DOCX document XML: %w", err)
		}
		tokens++
		if tokens > 2_000_000 {
			return "", errWebFSTooMany
		}
		switch typed := token.(type) {
		case xml.StartElement:
			switch typed.Name.Local {
			case "p":
				inParagraph = true
				paragraph.Reset()
				heading = 0
			case "pStyle":
				if inParagraph {
					heading = webDOCXHeadingLevel(webXMLAttribute(typed.Attr, "val"))
				}
			case "t":
				inText = inParagraph
			case "tab":
				if inParagraph {
					paragraph.WriteByte('\t')
				}
			case "br", "cr":
				if inParagraph {
					paragraph.WriteByte('\n')
				}
			}
		case xml.CharData:
			if inText {
				paragraph.Write([]byte(typed))
			}
		case xml.EndElement:
			if typed.Name.Local == "t" {
				inText = false
			}
			if typed.Name.Local == "p" {
				appendWebDOCXParagraph(&output, paragraph.String(), heading)
				inParagraph = false
			}
		}
	}
	return strings.TrimSpace(output.String()), nil
}

func webXMLAttribute(attributes []xml.Attr, localName string) string {
	for _, attribute := range attributes {
		if attribute.Name.Local == localName {
			return attribute.Value
		}
	}
	return ""
}

func webDOCXHeadingLevel(style string) int {
	style = strings.ToLower(strings.TrimSpace(style))
	for _, prefix := range []string{"heading", "heading_", "heading-"} {
		if strings.HasPrefix(style, prefix) {
			value := strings.TrimLeft(strings.TrimPrefix(style, prefix), " _-")
			level, err := strconv.Atoi(value)
			if err == nil && level >= 1 && level <= 6 {
				return level
			}
		}
	}
	return 0
}

func appendWebDOCXParagraph(output *strings.Builder, content string, heading int) {
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}
	if output.Len() > 0 {
		output.WriteString("\n\n")
	}
	if heading > 0 {
		output.WriteString(strings.Repeat("#", heading))
		output.WriteByte(' ')
	}
	output.WriteString(content)
}
