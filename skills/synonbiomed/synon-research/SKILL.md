---
name: synon-research
description: Plan and execute evidence-backed life-science research across target biology, genetics, omics, pharmacology, structures, clinical development, literature, and patents. Use for broad biomedical research, deep investigation, target assessment, competitive evidence reviews, or professional reports; do not use for a single simple database lookup. 适用于生命科学深度调研、靶点评估、研发证据综述和专业研究报告。
tags: [biomedical research, target assessment, evidence synthesis, 生命科学调研, 靶点评估]
keywords: [deep research, research report, development value, competitive landscape, evidence ledger, 深度调研, 研发价值, 专业报告, 证据表]
allowed-tools: search_skills, skill, repl, web_search, web_fetch, fetch_article_fulltext, patent_search, save_artifacts
metadata:
  dependencies:
    skills:
      - mcp-synon-research
---

# Synon-research

Use this workflow for broad, comparative, or deliverable-oriented biomedical research. Keep a simple lookup simple.

1. Split the question into a few decision-relevant evidence tracks. Resolve entity identifiers before joining sources.
2. Read [source selection](references/source-selection.md). For substantial work, check `synon-research` for a relevant structured source before broad web discovery; then combine the advertised `web_search`, `web_fetch`, `fetch_article_fulltext`, `patent_search`, native MCPs, and specialized Skills where they add distinct evidence. Return each discovery or read result to the outer agent loop before selecting the next source action.
3. Treat search hits and capability listings as discovery. Read the relevant primary record fields, sections, results, limitations, patent claims/examples, or registry data before using a source in a conclusion.
4. Before writing a substantial report, read [evidence synthesis](references/evidence-synthesis.md). Compare supporting and conflicting evidence, and distinguish observed results, source interpretation, bounded inference, and gaps.

For substantial work, deliver a professional report and an editable evidence ledger. Every decision-relevant claim must resolve to a retrieved source record and an exact excerpt or structured field path. Let evidence needs determine depth; do not impose a fixed source count, report length, or task-duration limit. Write in the user's language while preserving official identifiers and terms where translation would reduce precision.
