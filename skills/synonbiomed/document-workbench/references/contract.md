# Document Workbench contract

All commands emit one JSON object to stdout and use non-zero exit status on failure. Paths are resolved before execution, outputs are written atomically, and source files are never replaced implicitly.

## Supported formats

| Format | Create | Inspect | Edit | Validate | Convert / preview |
| --- | --- | --- | --- | --- | --- |
| DOCX | yes | paragraphs, tables, metadata | replace text, append blocks | OOXML + limits | built-in project preview; optional safe HTML export |
| XLSX / CSV | yes | sheets, dimensions, formulas | set cells, append rows, add sheet | OOXML + limits | built-in project preview; optional safe HTML export |
| PPTX | yes | slides, titles, notes/text | replace text, append slides | OOXML + limits | built-in project preview; optional safe HTML export |
| PDF | yes | metadata, pages, text sample | merge, rotate, metadata | parser + page limits | page PNGs and text summary |
| IPYNB | yes | cell counts, kernelspec, outputs | replace/append/clear cells | native v4 JSON schema + size limits | safe static HTML; optional execution |
| HTML | yes | visible text, metadata | not edited | UTF-8 + active-content scan | built-in browser preview |

Macro-enabled Office extensions are rejected. The built-in preview supports modern OOXML (`.docx`, `.xlsx`, `.pptx`) and does not install an Office suite. Legacy binary `.doc`, `.xls`, and `.ppt` files require an explicit, separately governed conversion request.

Notebook static preview never executes cells or renders active HTML output. Authorized execution uses a new local kernel and a temporary working directory, but it is not an operating-system sandbox; review notebook source and execute only trusted code.

## Managed runtime admission

Use `manage_environments` for inventory/creation and `manage_packages` for additive installation. The exact base pip pins are in `requirements.lock`: `python-docx`, `openpyxl`, `python-pptx`, `pypdf`, `pypdfium2`, `reportlab`, and `Pillow`. The environment must import `docx`, `openpyxl`, `pptx`, `pypdf`, `pypdfium2`, `reportlab`, and `PIL`. This base environment creates, validates, edits, and previews `.ipynb` through its native JSON schema. Add `nbformat`, `nbclient`, and `ipykernel` only for explicitly authorized notebook execution.

Resolve the implementation only from the runtime-injected absolute path `${SYNON_SKILL_DIR}/scripts/document_workbench.py`; never write user files into the Skill package. Specifications and outputs are task-workspace-relative. Run the helper through Bash with the verified environment. After a failure, inspect the structured receipt and make at most one materially corrected call; do not switch environments or write a competing implementation.

Package installation from notebook code or arbitrary shell commands is forbidden. Microsoft Office, LibreOffice, and OfficeCLI are not project-preview dependencies and must never be downloaded for an ordinary task. An already-installed converter may be used only for an explicitly requested PDF export. Publish user-facing native files through `save_artifacts`; JSON specifications, inspection dumps, and preview-control manifests remain internal unless explicitly requested.

## Create specification

The root is a JSON object. Unknown fields are rejected so misspellings do not silently degrade output.

### DOCX and PDF

```json
{
  "title": "Study report",
  "subtitle": "Validated evidence summary",
  "author": "Synon Biomed",
  "sections": [
    {
      "heading": "Summary",
      "level": 1,
      "blocks": [
        {"type": "paragraph", "text": "Plain paragraph."},
        {"type": "bullets", "items": ["First", "Second"]},
        {"type": "table", "headers": ["Metric", "Value"], "rows": [["Count", 12]]},
        {"type": "image", "path": "figure.png", "caption": "Figure 1", "widthInches": 5.8}
      ]
    }
  ]
}
```

### XLSX

```json
{
  "title": "Screening workbook",
  "sheets": [
    {
      "name": "Results",
      "headers": ["Compound", "IC50 nM", "Qualified"],
      "rows": [["A-01", 42.5, true], ["A-02", 91.2, false]],
      "freeze": "A2",
      "autoFilter": true,
      "widths": {"A": 22, "B": 14, "C": 14},
      "numberFormats": {"B": "0.00"}
    }
  ]
}
```

Cell values beginning with `=` are formulas only when the specification sets `allowFormulas: true`; otherwise they are stored as literal text. Formula cells are never evaluated by this workbench.

### PPTX

```json
{
  "title": "Program review",
  "author": "Synon Biomed",
  "slides": [
    {"layout": "title", "title": "Program review", "subtitle": "Decision meeting"},
    {"layout": "content", "title": "Evidence", "bullets": ["Validated assay", "Reproducible result"]},
    {"layout": "image", "title": "Structure", "image": "figure.png", "caption": "Lead series"}
  ]
}
```

### IPYNB

```json
{
  "title": "Analysis",
  "kernel": {"name": "python3", "displayName": "Python 3", "language": "python"},
  "cells": [
    {"type": "markdown", "source": "# Analysis"},
    {"type": "code", "source": "values = [1, 2, 3]\nsum(values)", "tags": ["analysis"]}
  ]
}
```

## Edit specification

```json
{
  "operations": [
    {"op": "replace_text", "find": "draft", "replace": "validated", "count": 0},
    {"op": "append_section", "heading": "Limitations", "blocks": [{"type": "paragraph", "text": "Pending external validation."}]},
    {"op": "set_cell", "sheet": "Results", "cell": "B2", "value": 42.5},
    {"op": "append_rows", "sheet": "Results", "rows": [["A-03", 38.0, true]]},
    {"op": "append_slide", "slide": {"layout": "content", "title": "Next steps", "bullets": ["Repeat assay"]}},
    {"op": "append_cell", "cell": {"type": "markdown", "source": "## Interpretation"}},
    {"op": "clear_outputs"},
    {"op": "rotate_pages", "pages": [1], "degrees": 90},
    {"op": "merge_pdf", "paths": ["appendix.pdf"]}
  ]
}
```

Operations unsupported by the input format fail with a descriptive error. `count: 0` replaces all matches; a positive count bounds replacements.

## Safety limits

- maximum input file: 250 MiB
- maximum OOXML ZIP entries: 20,000
- maximum uncompressed OOXML size: 1 GiB
- maximum ZIP expansion ratio: 200:1
- maximum PDF pages: 2,000
- maximum workbook sheets: 256
- maximum inspected cells: 2,000,000
- maximum presentation slides: 1,000
- maximum notebook cells: 20,000
- maximum individual text field: 4 MiB
- default external conversion timeout: 180 seconds

Encrypted PDFs and password-protected Office documents are reported as blocked; passwords are never logged or embedded in output metadata.

## Preview manifest

`render` writes this shape:

```json
{
  "schemaVersion": 1,
  "source": "report.docx",
  "sourceFormat": "docx",
  "previewKind": "office",
  "html": "index.html",
  "pdf": "report.pdf",
  "pages": ["page-001.png"],
  "warnings": []
}
```

Paths are relative to the preview directory. HTML is offline and contains no remote script, iframe, object, or tracking request.
