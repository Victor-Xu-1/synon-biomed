---
name: document-workbench
description: "Create, inspect, edit, validate, convert, render, and preview production-quality Word DOCX, Excel XLSX/CSV, PowerPoint PPTX, PDF, HTML, and Jupyter Notebook IPYNB artifacts. Use whenever a task asks for office documents, spreadsheets, slide decks, PDFs, notebooks, cross-format export, or a visual preview. Prefer pdf-explore for exhaustive semantic analysis of an existing long PDF; use this skill for PDF creation, manipulation, conversion, validation, and preview delivery."
allowed-tools: search_skills, skill, manage_environments, manage_packages, python, bash, read_file, edit_file, save_artifacts
license: Apache-2.0
---

# Document Workbench

Use one guarded workflow for Word, Excel, PowerPoint, PDF, HTML, and Jupyter Notebook deliverables. The work is not complete until the native artifact validates, is registered as a project artifact, and opens in the built-in project preview.

## Core rules

1. Work only inside the current project or task workspace. The helper treats its resolved current working directory as the hard workspace root and rejects absolute, relative, or symlink-resolved paths outside it. Never overwrite an input file by default.
2. Treat Office packages, PDFs, notebooks, HTML, formulas, links, macros, and embedded media as untrusted input.
3. Reject macro-enabled Office files (`.docm`, `.xlsm`, `.pptm`) unless the user explicitly requests a separate security review. This workbench never executes macros.
4. Create the requested native file (`.docx`, `.xlsx`, `.pptx`, `.pdf`, `.html`, or `.ipynb`) and publish it directly. Synon Biomed previews these formats inside the project file panel; do not require Microsoft Office, LibreOffice, OfficeCLI, or a download round trip.
5. Notebook execution is opt-in. Never execute an attached notebook merely to preview or inspect it.
6. Preserve source files. Edits must write to a distinct output path unless the user explicitly authorizes replacement.
7. For a long existing PDF that must be searched or synthesized across many pages, load `pdf-explore`; do not duplicate its semantic sweep here.

## Managed execution

Call `manage_environments(mode="list", dependencies=["python-docx", "openpyxl", "python-pptx", "pypdf", "pypdfium2", "reportlab", "pillow"])` first. Reuse a compatible environment or create a dedicated `document-workbench` Python environment, then install the exact pins from `requirements.lock` with `manage_packages(..., use_pip=true)`. Add `nbformat`, `nbclient`, and `ipykernel` only when the user explicitly authorizes notebook execution.

Run `${SYNON_SKILL_DIR}/scripts/document_workbench.py --help` once in that
verified environment before the requested action. The script, not model-written
document code, owns path validation, atomic output, format validation, and
active-content checks. A failed action ends that execution unit; inspect its
structured error before at most one materially corrected call.

The doctor is read-only and must not install system software. The built-in project preview does not require LibreOffice or Microsoft Office. Never download or install an Office suite for an ordinary creation or preview task. Office-to-PDF conversion is optional, may use an already-installed converter only when the user explicitly requests PDF export, and must otherwise report itself unavailable.

## Workflow

### 1. Clarify the deliverable

Confirm only choices that materially change the result: target formats, audience, page/slide/sheet constraints, source-of-truth data, whether notebook code may run, and whether an existing file may be replaced. For ordinary styling choices, use the defaults in `references/contract.md`.

### 2. Build or inspect

Use `edit_file` to create the task-relative JSON specification, then run the
bundled helper in the verified environment. Example specifications are
references; never present a draft spec as an executed document.

```bash
python "${SYNON_SKILL_DIR}/scripts/document_workbench.py" create --format docx --spec report.json --output exports/report.docx
python "${SYNON_SKILL_DIR}/scripts/document_workbench.py" create --format xlsx --spec workbook.json --output exports/data.xlsx
python "${SYNON_SKILL_DIR}/scripts/document_workbench.py" create --format pptx --spec deck.json --output exports/slides.pptx
python "${SYNON_SKILL_DIR}/scripts/document_workbench.py" create --format pdf --spec brief.json --output exports/brief.pdf
python "${SYNON_SKILL_DIR}/scripts/document_workbench.py" create --format ipynb --spec notebook.json --output exports/analysis.ipynb
python "${SYNON_SKILL_DIR}/scripts/document_workbench.py" create --format html --spec report.json --output exports/report.html
```

Inspect and validate existing files before editing:

```bash
python "${SYNON_SKILL_DIR}/scripts/document_workbench.py" validate --input exports/report.docx
python "${SYNON_SKILL_DIR}/scripts/document_workbench.py" inspect --input exports/report.docx --output exports/report.inspect.json
```

The schemas and supported edit operations are defined in `references/contract.md`. Example specifications are under `examples/`.

### 3. Edit without destructive replacement

```bash
python "${SYNON_SKILL_DIR}/scripts/document_workbench.py" edit --input exports/report.docx --spec changes.json --output exports/report-revised.docx
```

The edit contract supports text replacement and append operations for DOCX/PPTX, cell/sheet operations for XLSX, page operations for PDF, and cell/output/metadata operations for IPYNB. Unsupported edits must fail explicitly instead of silently dropping content.

### 4. Convert and render only when the user requests an additional export

```bash
python "${SYNON_SKILL_DIR}/scripts/document_workbench.py" convert --input exports/report.docx --output exports/report.pdf
python "${SYNON_SKILL_DIR}/scripts/document_workbench.py" convert --input exports/analysis.ipynb --output exports/analysis.html
python "${SYNON_SKILL_DIR}/scripts/document_workbench.py" render --input exports/report.docx --output-dir exports/report-preview
```

The project file panel is the primary preview and requires no conversion. `render` is an optional visual-QA/export helper: it always produces `preview-manifest.json`; Office and Notebook render produce passive HTML, while PDF render produces page PNGs. Never install LibreOffice to satisfy this step. Active scripts and untrusted HTML outputs are not executed.

### 5. Execute notebooks only with explicit authorization

```bash
python "${SYNON_SKILL_DIR}/scripts/document_workbench.py" execute-notebook --input exports/analysis.ipynb --output exports/analysis-executed.ipynb --allow-execution --timeout 120
```

Execution uses a fresh local kernel, a temporary working directory, a bounded timeout, a separate output file, and no shell interpolation. This is process isolation, not an operating-system sandbox: execute only code the user trusts and that has passed source review. If execution is not authorized, keep code cells unexecuted and provide a static preview.

### 6. Publish native and preview artifacts

After validation, call `save_artifacts` once with the actual `.docx`, `.xlsx`, `.pptx`, `.pdf`, `.html`, and `.ipynb` deliverables. This makes them project files and enables the same in-app preview used by other generated files. Do not publish duplicate HTML/PDF preview files, JSON specifications, inspection dumps, or preview-control manifests unless the user explicitly requests them.

```json
{
  "files": [
    "exports/report.docx",
    "exports/data.xlsx",
    "exports/slides.pptx"
  ],
  "language": "python",
  "environment": "document-workbench",
  "human_description": "Publish the validated native files for built-in project preview."
}
```

The paths are relative to the current scientific workspace. `save_artifacts` is the only publication authority for the conversation file panel; a successful local write is not a published deliverable.

### 7. Visual QA

Open every saved Word, Excel, PowerPoint, PDF, HTML, and Notebook artifact in the project file panel. Inspect every Word/PDF page, every PowerPoint slide, every spreadsheet sheet, and the complete Notebook/HTML preview. Check clipped or overflowing text, table widths, image scaling, formulas, page/slide/sheet counts, execution output, and non-ASCII text. Iterate until every preview is readable at the target viewport. A successful file write or converter response alone is not acceptance evidence.

## Output standard

- Word: real heading styles, usable tables, restrained typography, page margins, metadata, and no fake layout made from repeated spaces.
- Excel: typed values, frozen headers where appropriate, filters, readable widths, explicit formulas, number formats, and no unexplained magic numbers.
- PowerPoint: 16:9 layout, one narrative point per slide, editable text/shapes, consistent margins, and no rasterized text.
- PDF: embedded or portable fonts where possible, selectable text, stable pagination, and a page-render check.
- Notebook: ordered markdown/code cells, deterministic setup notes, bounded execution, visible outputs, and cleared stale errors when requested.
- HTML preview: self-contained, offline, escaped, keyboard-readable, and free of remote scripts, tracking, or patterned image backgrounds.

## Verification

The repository regression suite under `scripts/test_document_workbench.py` is a maintainer gate for changes to this skill; do not run it as part of an ordinary user task. For each user deliverable, run doctor once, validate every native output, publish through `save_artifacts`, and open every resulting project preview. Report the exact operations, validation result, and previews inspected. Never describe an unopened, unvalidated, partially inspected, or unpublished document as complete.
