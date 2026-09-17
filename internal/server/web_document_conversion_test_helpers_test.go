package server

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func writeWebDocumentZip(t *testing.T, filePath string, parts map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		t.Fatal(err)
	}
	output, err := os.Create(filePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(output)
	for name, content := range parts {
		part, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeWebDocumentFixtures(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{
		"markdown": filepath.Join(root, "note.md"),
		"csv":      filepath.Join(root, "table.csv"),
		"docx":     filepath.Join(root, "report.docx"),
		"xlsx":     filepath.Join(root, "table.xlsx"),
		"pptx":     filepath.Join(root, "slides.pptx"),
	}
	writeWebFSTestFile(t, files["markdown"], "# Native markdown")
	writeWebFSTestFile(t, files["csv"], "name,value\nalpha,7\n")
	writeWebDocumentZip(t, files["docx"], map[string]string{
		"word/document.xml": `<?xml version="1.0"?>
<w:document xmlns:w="urn:word"><w:body>
<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Title</w:t></w:r></w:p>
<w:p><w:r><w:t>Body text</w:t></w:r></w:p>
</w:body></w:document>`,
	})
	writeWebDocumentZip(t, files["xlsx"], map[string]string{
		"xl/workbook.xml": `<?xml version="1.0"?>
<workbook xmlns:r="urn:relationships"><sheets><sheet name="Data" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0"?>
<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/sharedStrings.xml": `<?xml version="1.0"?>
<sst><si><t>name</t></si></sst>`,
		"xl/worksheets/sheet1.xml": `<?xml version="1.0"?>
<worksheet><sheetData>
<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1"><v>7</v></c></row>
<row r="2"><c r="A2" t="inlineStr"><is><t>alpha</t></is></c><c r="B2" t="b"><v>1</v></c></row>
</sheetData><mergeCells><mergeCell ref="A1:B1"/></mergeCells></worksheet>`,
	})
	writeWebDocumentZip(t, files["pptx"], map[string]string{
		"ppt/slides/slide2.xml": `<p:sld xmlns:p="urn:p" xmlns:a="urn:a"><a:p><a:r><a:t>Second</a:t></a:r></a:p></p:sld>`,
		"ppt/slides/slide1.xml": `<p:sld xmlns:p="urn:p" xmlns:a="urn:a"><a:p><a:r><a:t>First</a:t></a:r></a:p></p:sld>`,
	})
	return files
}
