package server

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
)

type webPPTJSON struct {
	Slides []webPPTSlide `json:"slides"`
}

type webPPTSlide struct {
	SlideNumber int `json:"slideNumber"`
	Content     any `json:"content"`
}

func convertWebPPTXToJSON(filePath string) (webPPTJSON, error) {
	archive, err := openWebOOXMLArchive(filePath)
	if err != nil {
		return webPPTJSON{}, err
	}
	defer archive.Close()
	return convertWebPPTXArchiveToJSON(archive)
}

func convertWebPPTXArchiveToJSON(archive *webOOXMLArchive) (webPPTJSON, error) {
	names := archive.Names("ppt/slides/slide", ".xml")
	if len(names) == 0 || len(names) > 10_000 {
		return webPPTJSON{}, errorsForWebPPTSlideCount(len(names))
	}
	slides := make([]webPPTSlide, 0, len(names))
	var total int64
	for _, name := range names {
		number, ok := webPPTSlideNumber(name)
		if !ok {
			continue
		}
		raw, err := archive.ReadPart(name, 16<<20)
		if err != nil {
			return webPPTJSON{}, err
		}
		total += int64(len(raw))
		if total > maxWebDocumentBytes {
			return webPPTJSON{}, errWebFSTooLarge
		}
		paragraphs, err := parseWebPPTSlideText(raw)
		if err != nil {
			return webPPTJSON{}, fmt.Errorf("parse slide %d: %w", number, err)
		}
		slides = append(slides, webPPTSlide{
			SlideNumber: number,
			Content: map[string]any{
				"text": strings.Join(paragraphs, "\n"), "paragraphs": paragraphs,
			},
		})
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].SlideNumber < slides[j].SlideNumber })
	if len(slides) == 0 {
		return webPPTJSON{}, errorsForWebPPTSlideCount(0)
	}
	return webPPTJSON{Slides: slides}, nil
}

func errorsForWebPPTSlideCount(count int) error {
	if count > 10_000 {
		return errWebFSTooMany
	}
	return fmt.Errorf("PPTX contains no readable slides")
}

func webPPTSlideNumber(name string) (int, bool) {
	base := path.Base(name)
	if !strings.HasPrefix(base, "slide") || !strings.HasSuffix(base, ".xml") {
		return 0, false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(base, "slide"), ".xml")
	number, err := strconv.Atoi(raw)
	return number, err == nil && number > 0
}

func parseWebPPTSlideText(raw []byte) ([]string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	paragraphs := make([]string, 0)
	var paragraph strings.Builder
	inParagraph := false
	inText := false
	tokens := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		tokens++
		if tokens > 1_000_000 {
			return nil, errWebFSTooMany
		}
		switch typed := token.(type) {
		case xml.StartElement:
			switch typed.Name.Local {
			case "p":
				inParagraph = true
				paragraph.Reset()
			case "t":
				inText = true
			}
		case xml.CharData:
			if inText {
				paragraph.Write([]byte(typed))
			}
		case xml.EndElement:
			if typed.Name.Local == "t" {
				inText = false
			}
			if typed.Name.Local == "p" && inParagraph {
				if value := strings.TrimSpace(paragraph.String()); value != "" {
					paragraphs = append(paragraphs, value)
				}
				inParagraph = false
			}
		}
	}
	return paragraphs, nil
}
