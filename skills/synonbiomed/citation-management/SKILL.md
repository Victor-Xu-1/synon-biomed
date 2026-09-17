---
name: citation-management
description: Resolve DOI metadata, validate DOI records, generate BibTeX, and search citation records through the local citation_lookup system tool.
license: Apache-2.0
---

# Citation Management

Use this skill when the user asks for DOI lookup, BibTeX generation, citation
cleanup, citation validation, or bibliographic search. Prefer the
`citation_lookup` system tool before writing a citation from memory.

For a known DOI, call `citation_lookup` with `operation: "lookup"` to retrieve
the title, authors, journal, year, publisher, and source evidence. Use
`operation: "doi_to_bibtex"` when the requested deliverable is a BibTeX entry.
Use `operation: "search"` for partial paper titles or bibliographic fragments.

When writing final citations, keep identifiers exact and clickable. If a DOI
does not resolve through the tool, say so plainly and avoid guessing the suffix.
For literature reviews, combine this skill with source-backed literature search
so the bibliography supports the claims rather than becoming a detached list.
