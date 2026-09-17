package server

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenWebOOXMLArchiveAllowsCanonicalDirectoryEntries(t *testing.T) {
	archivePath := writeWebOOXMLTestArchive(t, []webOOXMLTestEntry{
		{name: "word/"},
		{name: "word/_rels/"},
		{name: "word/document.xml", content: "<document/>"},
	})

	archive, err := openWebOOXMLArchive(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = archive.Close() })

	content, err := archive.ReadPart("word/document.xml", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "<document/>" {
		t.Fatalf("document part = %q", content)
	}
	if names := archive.Names("word/", ""); len(names) != 1 || names[0] != "word/document.xml" {
		t.Fatalf("indexed parts = %v", names)
	}
}

func TestOpenWebOOXMLArchiveRejectsNonCanonicalDirectoryEntries(t *testing.T) {
	for _, name := range []string{"../word/", "/word/", "word/../", "word//"} {
		t.Run(strings.ReplaceAll(name, "/", "_"), func(t *testing.T) {
			archivePath := writeWebOOXMLTestArchive(t, []webOOXMLTestEntry{{name: name}})
			archive, err := openWebOOXMLArchive(archivePath)
			if archive != nil {
				_ = archive.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "unsafe part name") {
				t.Fatalf("open archive with %q: archive=%v err=%v", name, archive, err)
			}
		})
	}
}

type webOOXMLTestEntry struct {
	name    string
	content string
}

func writeWebOOXMLTestArchive(t *testing.T, entries []webOOXMLTestEntry) string {
	t.Helper()
	archivePath := filepath.Join(t.TempDir(), "document.docx")
	output, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(output)
	for _, entry := range entries {
		part, err := writer.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(entry.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	return archivePath
}
