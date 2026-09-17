package agentruntime

import (
	"bytes"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type ContentPartType string

const (
	ContentPartText     ContentPartType = "text"
	ContentPartImage    ContentPartType = "image"
	ContentPartDocument ContentPartType = "document"
	ContentPartAudio    ContentPartType = "audio"
)

type MediaSourceType string

const (
	MediaSourceData MediaSourceType = "data"
	MediaSourceFile MediaSourceType = "file"
)

const (
	DefaultMaxMediaPartBytes  int64 = 25 << 20
	DefaultMaxMediaTotalBytes int64 = 32 << 20
)

type ContentPart struct {
	Type  ContentPartType `json:"type"`
	Text  string          `json:"text,omitempty"`
	Media *MediaContent   `json:"media,omitempty"`
}

type MediaContent struct {
	MIMEType string      `json:"mime_type"`
	Filename string      `json:"filename,omitempty"`
	Source   MediaSource `json:"source"`
}

type MediaSource struct {
	Type MediaSourceType `json:"type"`
	Data []byte          `json:"data,omitempty"`
	Path string          `json:"path,omitempty"`
}

type MediaPolicy struct {
	AllowedFileRoots []string `json:"-"`
	MaxPartBytes     int64    `json:"-"`
	MaxTotalBytes    int64    `json:"-"`
}

func NormalizeModelRequestMedia(request ModelRequest) (ModelRequest, error) {
	policy := normalizeMediaPolicy(request.MediaPolicy)
	output := request
	output.Messages = make([]Message, len(request.Messages))
	var total int64
	for messageIndex, message := range request.Messages {
		output.Messages[messageIndex] = message
		if len(message.Parts) == 0 {
			continue
		}
		output.Messages[messageIndex].Parts = make([]ContentPart, len(message.Parts))
		for partIndex, part := range message.Parts {
			normalized, size, err := normalizeContentPart(part, policy)
			if err != nil {
				return ModelRequest{}, fmt.Errorf("message %d content part %d: %w", messageIndex, partIndex, err)
			}
			total += size
			if total > policy.MaxTotalBytes {
				return ModelRequest{}, fmt.Errorf("media content exceeds total limit of %d bytes", policy.MaxTotalBytes)
			}
			output.Messages[messageIndex].Parts[partIndex] = normalized
		}
	}
	output.MediaPolicy = policy
	return output, nil
}

func normalizeMediaPolicy(policy MediaPolicy) MediaPolicy {
	if policy.MaxPartBytes <= 0 {
		policy.MaxPartBytes = DefaultMaxMediaPartBytes
	}
	if policy.MaxTotalBytes <= 0 {
		policy.MaxTotalBytes = DefaultMaxMediaTotalBytes
	}
	return policy
}

func normalizeContentPart(part ContentPart, policy MediaPolicy) (ContentPart, int64, error) {
	switch part.Type {
	case ContentPartText:
		if part.Media != nil {
			return ContentPart{}, 0, errors.New("text part must not contain media")
		}
		if part.Text == "" {
			return ContentPart{}, 0, errors.New("text part is empty")
		}
		return ContentPart{Type: ContentPartText, Text: part.Text}, 0, nil
	case ContentPartImage, ContentPartDocument, ContentPartAudio:
	default:
		return ContentPart{}, 0, fmt.Errorf("unsupported content part type %q", part.Type)
	}
	if part.Media == nil {
		return ContentPart{}, 0, fmt.Errorf("%s part requires media", part.Type)
	}
	media, err := normalizeMediaContent(part.Type, *part.Media, policy)
	if err != nil {
		return ContentPart{}, 0, err
	}
	return ContentPart{Type: part.Type, Media: &media}, int64(len(media.Source.Data)), nil
}

func normalizeMediaContent(kind ContentPartType, media MediaContent, policy MediaPolicy) (MediaContent, error) {
	media.MIMEType = strings.ToLower(strings.TrimSpace(media.MIMEType))
	parsedMIME, _, err := mime.ParseMediaType(media.MIMEType)
	if err != nil || parsedMIME != media.MIMEType || !mediaMIMEAllowed(kind, media.MIMEType) {
		return MediaContent{}, fmt.Errorf("MIME type %q is not allowed for %s", media.MIMEType, kind)
	}
	media.Filename = strings.TrimSpace(media.Filename)
	if media.Filename != "" && (filepath.Base(media.Filename) != media.Filename ||
		strings.ContainsAny(media.Filename, "/\\\x00\r\n") || media.Filename == "." || media.Filename == "..") {
		return MediaContent{}, errors.New("media filename is invalid")
	}
	var data []byte
	switch media.Source.Type {
	case MediaSourceData:
		if len(media.Source.Data) == 0 || strings.TrimSpace(media.Source.Path) != "" {
			return MediaContent{}, errors.New("data source requires bytes and cannot contain a path")
		}
		if int64(len(media.Source.Data)) > policy.MaxPartBytes {
			return MediaContent{}, fmt.Errorf("media part exceeds limit of %d bytes", policy.MaxPartBytes)
		}
		data = append([]byte(nil), media.Source.Data...)
	case MediaSourceFile:
		if len(media.Source.Data) != 0 {
			return MediaContent{}, errors.New("file source cannot contain inline bytes")
		}
		data, err = readAllowedMediaFile(media.Source.Path, policy)
		if err != nil {
			return MediaContent{}, err
		}
	default:
		return MediaContent{}, fmt.Errorf("unsupported media source type %q", media.Source.Type)
	}
	if err := validateMediaBytes(kind, media.MIMEType, data); err != nil {
		return MediaContent{}, err
	}
	media.Source = MediaSource{Type: MediaSourceData, Data: data}
	return media, nil
}

func readAllowedMediaFile(path string, policy MediaPolicy) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("media file path must be absolute")
	}
	return readAllowedMediaFileHandle(filepath.Clean(path), policy.AllowedFileRoots, policy.MaxPartBytes)
}

func mediaMIMEAllowed(kind ContentPartType, value string) bool {
	switch kind {
	case ContentPartImage:
		return value == "image/jpeg" || value == "image/png" || value == "image/gif" || value == "image/webp"
	case ContentPartDocument:
		return value == "application/pdf" || value == "text/plain" || value == "text/markdown"
	case ContentPartAudio:
		return value == "audio/wav" || value == "audio/mpeg"
	default:
		return false
	}
}

func validateMediaBytes(kind ContentPartType, mimeType string, data []byte) error {
	if len(data) == 0 {
		return errors.New("media content is empty")
	}
	detected := strings.ToLower(strings.TrimSpace(strings.Split(http.DetectContentType(data), ";")[0]))
	switch kind {
	case ContentPartImage:
		if detected != mimeType {
			return fmt.Errorf("media content type mismatch: declared %s detected %s", mimeType, detected)
		}
	case ContentPartDocument:
		if mimeType == "application/pdf" && (!bytes.HasPrefix(data, []byte("%PDF-")) || detected != "application/pdf") {
			return errors.New("document is not a valid PDF payload")
		}
		if strings.HasPrefix(mimeType, "text/") && (!utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0) {
			return errors.New("document text must be valid UTF-8 without NUL bytes")
		}
	case ContentPartAudio:
		validWAV := len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WAVE"
		validMP3 := bytes.HasPrefix(data, []byte("ID3")) || len(data) >= 2 && data[0] == 0xff && data[1]&0xe0 == 0xe0
		if mimeType == "audio/wav" && !validWAV || mimeType == "audio/mpeg" && !validMP3 {
			return fmt.Errorf("media content type mismatch for %s", mimeType)
		}
	}
	return nil
}
