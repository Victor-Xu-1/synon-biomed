package logoassets

import (
	"embed"
	"errors"
	"io/fs"
	"path"
	"strings"
)

const (
	SourceRepository = "https://github.com/iOfficeAI/AionCore"
	SourceCommit     = "020a27a77aeb2b5be7a2b4380aab8ae2686311c4"
)

var ErrInvalidName = errors.New("invalid logo asset name")

//go:embed assets
var bundled embed.FS

func Read(name string) ([]byte, error) {
	normalized, err := normalize(name)
	if err != nil {
		return nil, err
	}
	content, err := bundled.ReadFile("assets/" + normalized)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fs.ErrNotExist
	}
	return content, err
}

func normalize(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 256 || strings.ContainsAny(name, "\\\x00") ||
		strings.HasPrefix(name, "/") {
		return "", ErrInvalidName
	}
	cleaned := path.Clean(name)
	if cleaned != name || cleaned == "." || strings.HasPrefix(cleaned, "../") {
		return "", ErrInvalidName
	}
	switch strings.ToLower(path.Ext(cleaned)) {
	case ".svg", ".png":
		return cleaned, nil
	default:
		return "", ErrInvalidName
	}
}
