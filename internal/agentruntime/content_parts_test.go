package agentruntime

import (
	"os"
	"path/filepath"
	"testing"
)

var tinyPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

func TestNormalizeModelRequestMediaAcceptsValidatedInlineImage(t *testing.T) {
	request, err := NormalizeModelRequestMedia(ModelRequest{Messages: []Message{{
		Role: "user", Parts: []ContentPart{
			{Type: ContentPartText, Text: "inspect"},
			{Type: ContentPartImage, Media: &MediaContent{MIMEType: "image/png", Source: MediaSource{Type: MediaSourceData, Data: tinyPNG}}},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	part := request.Messages[0].Parts[1]
	if part.Media == nil || part.Media.Source.Type != MediaSourceData || string(part.Media.Source.Data) != string(tinyPNG) {
		t.Fatalf("normalized media = %#v", part)
	}
}

func TestNormalizeModelRequestMediaRejectsUnsafeOrMismatchedMedia(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, tinyPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		part ContentPart
	}{
		{name: "mismatched image bytes", part: ContentPart{Type: ContentPartImage, Media: &MediaContent{MIMEType: "image/png", Source: MediaSource{Type: MediaSourceData, Data: []byte("not an image")}}}},
		{name: "outside file root", part: ContentPart{Type: ContentPartImage, Media: &MediaContent{MIMEType: "image/png", Source: MediaSource{Type: MediaSourceFile, Path: outside}}}},
		{name: "remote source", part: ContentPart{Type: ContentPartImage, Media: &MediaContent{MIMEType: "image/png", Source: MediaSource{Type: "url", Path: "https://example.invalid/image.png"}}}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := NormalizeModelRequestMedia(ModelRequest{
				Messages:    []Message{{Role: "user", Parts: []ContentPart{testCase.part}}},
				MediaPolicy: MediaPolicy{AllowedFileRoots: []string{root}},
			})
			if err == nil {
				t.Fatal("NormalizeModelRequestMedia() unexpectedly succeeded")
			}
		})
	}
}

func TestNormalizeModelRequestMediaUsesOneHandleAnchoredRegularFile(t *testing.T) {
	root := t.TempDir()
	regular := filepath.Join(root, "regular.png")
	if err := os.WriteFile(regular, tinyPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	normalize := func(path string) error {
		_, err := NormalizeModelRequestMedia(ModelRequest{
			Messages: []Message{{Role: "user", Parts: []ContentPart{{
				Type:  ContentPartImage,
				Media: &MediaContent{MIMEType: "image/png", Source: MediaSource{Type: MediaSourceFile, Path: path}},
			}}}},
			MediaPolicy: MediaPolicy{AllowedFileRoots: []string{root}},
		})
		return err
	}
	if err := normalize(regular); err != nil {
		t.Fatalf("regular file: %v", err)
	}

	finalSymlink := filepath.Join(root, "final-link.png")
	if err := os.Symlink(regular, finalSymlink); err == nil {
		if err := normalize(finalSymlink); err == nil {
			t.Fatal("final symbolic link unexpectedly succeeded")
		}
	}

	realDirectory := filepath.Join(root, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(realDirectory, "nested.png")
	if err := os.WriteFile(nested, tinyPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	directorySymlink := filepath.Join(root, "directory-link")
	if err := os.Symlink(realDirectory, directorySymlink); err == nil {
		if err := normalize(filepath.Join(directorySymlink, "nested.png")); err == nil {
			t.Fatal("intermediate symbolic link unexpectedly succeeded")
		}
	}

	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, tinyPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	hardlink := filepath.Join(root, "hardlink.png")
	if err := os.Link(outside, hardlink); err == nil {
		if err := normalize(hardlink); err == nil {
			t.Fatal("hard-linked media file unexpectedly succeeded")
		}
	}
}
