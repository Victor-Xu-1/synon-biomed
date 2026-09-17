package qrimage

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

const (
	defaultImageSize = 256
	minImageSize     = 192
	maxImageSize     = 320
	maxPayloadBytes  = 4096
)

// EncodeURL produces an in-memory PNG QR image without sending the payload to
// a third-party QR service. Callers should still treat the decoded URL as a
// provider authorization secret and must not return it separately.
func EncodeURL(value string, size int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxPayloadBytes {
		return "", errors.New("QR URL payload is empty or too large")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", errors.New("QR URL payload is invalid")
	}
	if size <= 0 {
		size = defaultImageSize
	}
	if size < minImageSize || size > maxImageSize {
		return "", fmt.Errorf("QR image size must be between %d and %d", minImageSize, maxImageSize)
	}
	png, err := qrcode.Encode(value, qrcode.Medium, size)
	if err != nil {
		return "", fmt.Errorf("encode QR image: %w", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}
