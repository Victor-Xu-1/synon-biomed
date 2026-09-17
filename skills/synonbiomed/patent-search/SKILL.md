---
name: patent-search
description: Search, compare, locate, and download public patent publications from WIPO PATENTSCOPE, Google Patents, the European Patent Office, and China's CNIPA. Use for prior-art discovery, patent-family investigation, assignee or inventor searches, claim landscapes, chemistry and Markush disclosures, freedom-to-operate clues, publication-number lookup, or saving an identified patent PDF into the current project.
allowed-tools: patent_search, manage_environments, python, save_artifacts
license: Apache-2.0
---

# Patent Search

Use `patent_search` as the governed entry point. It separates discovery results from official records and never bypasses credentials, login pages, rate limits, or CAPTCHA.

## Workflow

1. Build a compact query from the invention, synonyms, target or mechanism, scaffold or compound class, assignee, inventor, and relevant date range. For Chinese concepts, search both Chinese and English terms when useful.
2. Call `patent_search` with `operation: "search"`. Omit `sources` to search WIPO, Google Patents, EPO, and CNIPA; or pass any of `wipo`, `google`, `epo`, and `cnipa`. Size discovery to the question rather than a fixed display count. Search independent Chinese, English, synonym, assignee, classification, and scaffold tracks when relevant; preserve every source/query receipt, merge by publication or family identity, and continue while the result reports truncation or an uncovered evidence track remains. Exact-number lookup may return one record; a broad landscape normally needs multiple source-balanced batches before claim-level selection.
3. Treat every search hit as discovery evidence. Extract or confirm publication numbers, then call `operation: "lookup"` for each decision-relevant family. Supply `focus_terms` for the requested mechanism, formulation, scaffold, route, or use so the lookup returns focused description and example passages alongside the record metadata, abstract, and claims. Do not confuse an application, publication, grant, or family member.
4. A title, snippet, hit count, or locator-only record is not patent evidence. Before making a claim-level statement, require a successful lookup with `recordDepth: full_record`, then read the abstract, relevant independent claims, description passages, and examples. Distinguish claimed scope, described embodiments, experimentally exemplified results, cited prior art, legal status, and machine-translated or OCR text. When only a locator is available, keep the row as a candidate or evidence gap.
5. To save a patent, call `patent_search` with `operation: "resolve_download"` and the exact publication number. When Google Patents returns a trusted `downloadUrl`, reuse one managed Python environment containing `requests`, download that exact URL once with `requests.get(url, timeout=(10, 120))`, require a successful response and `response.content.startswith(b"%PDF-")`, then write a path-free filename made from the normalized publication number plus `.pdf`. Call `save_artifacts` for that validated file so the transcript preserves the source result and the project provides built-in preview. Do not refetch, rewrite, or rename the same PDF through another tool.
6. When the result says `user_action_required`, open the official page and let the user complete authentication or CAPTCHA. Continue only after access is available; never automate around the gate.

## Source policy

- Prefer WIPO or the relevant national or regional office as the legal publication source. WIPO PDF is authoritative over OCR text.
- EPO Espacenet is public for interactive review. Machine access through OPS requires separately configured registered credentials; never ask the user to paste those credentials into the model context.
- CNIPA search and downloads can require a free account and interactive verification. Report this boundary plainly.
- Google Patents is useful for discovery and may expose a publication PDF, but it is not an official patent-office API. Verify important facts against the official record.
- A keyword match is not a freedom-to-operate opinion. State that legal-status and claim-scope conclusions require qualified patent counsel.

Read [source-access.md](references/source-access.md) when choosing a source, resolving a download, or explaining an access limitation.

## Output

Return a concise, source-backed table with publication number, title, applicant or assignee, priority or publication date when verified, jurisdiction, family relationship when known, relevance, source URL, and evidence status. Group results by patent family or claim theme, flag unverified matches, and link every downloaded artifact.
