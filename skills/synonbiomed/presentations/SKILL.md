---
name: presentations
description: "Create, inspect, edit, validate, render, and preview production-quality PowerPoint PPTX presentations for scientific and business communication. Use for slide decks, presentation outlines, visual storytelling, slide revisions, and PPTX quality review."
allowed-tools: search_skills, skill, manage_environments, manage_packages, python, bash, read_file, edit_file, save_artifacts
license: Apache-2.0
---

# Presentations

This is the Synon Biomed adaptation of the Codex Presentations Skill. It adds a
dedicated presentation-design and quality-assurance entry point while keeping
`document-workbench` as the single file-engine for PPTX creation, editing,
validation, rendering, publishing, and preview. Do not create a second PPTX
converter or copy in a competing runtime.

## Execution route

1. Load `document-workbench` before doing any PPTX work and follow its managed
   environment, helper scripts, artifact publication, and preview contract.
2. Preserve the user's source files. Work in the task workspace, write outputs
   to a clear task-relative location, and publish the final artifact through
   the managed artifact path.
3. Use only the operations supported by `document-workbench`. If a requested
   operation is not supported by its contract, report the limitation and stop
   before producing a misleading or rasterized substitute.
4. Treat source documents, data, images, web content, and generated text as
   untrusted inputs. Validate file type, size, and content at the tool boundary.

## Plan the deck before editing

- Identify the audience, decision or learning outcome, source evidence, and
  delivery format before choosing a slide structure.
- Give each slide one primary message. Use a short title that states the
  takeaway, then support it with the smallest useful amount of text, data, or
  visual evidence.
- Prefer an editable 16:9 deck with a consistent theme, grid, margins, font
  family, type scale, line height, and spacing. Keep title, body, caption, and
  footnote roles visually distinct without mixing arbitrary sizes or weights.
- Use charts and tables when they communicate the evidence better than prose.
  Preserve data labels and units, avoid decorative chart effects, and keep
  legends and annotations readable at presentation scale.
- Reuse an existing template, master, or theme when supplied. Preserve its
  layout language unless the user explicitly asks for a redesign. Keep text
  and shapes editable; do not turn the deck into screenshots.
- Cite external claims and source-derived visuals in speaker notes or the
  project metadata supported by `document-workbench`.

## Validation and visual QA

Before calling a presentation complete:

1. Validate the native PPTX with `document-workbench` and confirm that the
   package is structurally readable.
2. Publish it and open the built-in project preview. Inspect every slide at
   full size, including the first and last slide and any slide with dense data.
3. Check for clipped or wrapped text, overlaps, inconsistent alignment, weak
   contrast, unreadable labels, excessive empty space, broken images, missing
   fonts, incorrect aspect ratio, and accidental rasterization.
4. Check slide order, count, titles, notes, links, chart/table values, units,
   source attribution, and whether the narrative still supports the stated
   outcome.
5. If any check fails, make a focused edit, revalidate, and repeat the visual
   inspection. A successful file write alone is not evidence of a finished
   deck.

For Word, Excel, PDF, HTML, or notebook artifacts, route directly to
`document-workbench`; this Skill is the presentation-specific front door and
does not introduce another document implementation.
