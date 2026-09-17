package logoassets

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
)

func TestReadReturnsPinnedSVGAndPNGAssets(t *testing.T) {
	svg, err := Read("ai-major/openai.svg")
	if err != nil {
		t.Fatal(err)
	}
	if len(svg) == 0 || !strings.Contains(string(svg), "<svg") {
		t.Fatalf("OpenAI asset is not SVG: %q", svg)
	}
	png, err := Read("ai-china/minimax.png")
	if err != nil {
		t.Fatal(err)
	}
	if len(png) < 8 || string(png[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("MiniMax asset is not PNG: %x", png[:min(8, len(png))])
	}
}

func TestPinnedAssetInventoryIsComplete(t *testing.T) {
	count := 0
	totalBytes := int64(0)
	err := fs.WalkDir(bundled, "assets", func(_ string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		count++
		totalBytes += info.Size()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 44 || totalBytes != 148609 {
		t.Fatalf("embedded logo inventory files=%d bytes=%d", count, totalBytes)
	}
}

func TestReadRejectsUnsafeAndUnknownAssetNames(t *testing.T) {
	for _, name := range []string{
		"", "../openai.svg", "ai-major/../openai.svg", "/ai-major/openai.svg",
		`ai-major\openai.svg`, "ai-major/openai.html", "ai-major//openai.svg",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Read(name); !errors.Is(err, ErrInvalidName) {
				t.Fatalf("Read(%q) err=%v", name, err)
			}
		})
	}
	if _, err := Read("ai-major/missing.svg"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing asset err=%v", err)
	}
}
