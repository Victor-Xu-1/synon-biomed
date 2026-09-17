---
name: literature-review
description: Find, verify, and synthesize scientific literature — from "what's the seminal paper for X" through full multi-source reviews. Covers grounding claims in real retrieved sources, avoiding fabricated citations, handling retractions, and calibrating confidence to evidence strength.
allowed-tools: search_skills, web_search, web_fetch, fetch_article_fulltext, repl, save_artifacts
license: Apache-2.0
metadata:
  # Non-biomodel: sends user's query to Crossref (with contact email when
  # configured) and to OpenAlex (with the user's OpenAlex api_key —
  # required since 2026-02-13; no email is ever sent to OpenAlex).
  third_party:
    # The leaf /rest-api-metadata-license-information/ page now 404s though
    # still in search indexes. Parent docs landing carries the license
    # statement ("Almost all of the metadata we hold is reusable without
    # restriction") and is less likely to rot. Docs page, not a ToU —
    # info_url. verified 2026-06-30
    - kind: service
      name: Crossref
      info_url: https://www.crossref.org/documentation/retrieve-metadata/
      privacy_url: https://www.crossref.org/operations-and-sustainability/privacy/
    - kind: service
      name: OpenAlex
      terms_url: https://openalex.org/OpenAlex_termsofservice.pdf
      privacy_url: https://openalex.org/OpenAlex_privacy_policy.pdf
critical-constraints:
  - For broad evidence work, split the question into independent evidence tracks and use several focused queries; do not join every population, modality, comparator, endpoint, and date constraint into one brittle all-AND query.
  - A zero-result or very small first page from one query is not evidence of absence. Preserve the receipt, remove one restrictive concept or search one evidence track at a time, and try one materially broader query or another appropriate source before concluding that evidence is unavailable.
  - A broad review delivers both the readable report and an editable source-ledger CSV containing stable identifiers, retrieval tracks, evidence roles, and inclusion rationale; raw connector JSON remains working data unless the user asks for it. Saved deliverables contain fully rendered literal content, never unresolved Jinja, Mustache, or other template markers.
  - 'Every included source-ledger row and cited identifier must come from an actual retrieved record whose title plus abstract or structured source record has been screened against the requested population or system, intervention or mechanism, route or setting, outcome, and time range. Discovery hits are candidates, not evidence: exclude near-matches instead of using them to fill a category, and never type remembered, inferred, or placeholder source facts into the ledger.'
  - 'Resolve rolling windows such as recent, latest, or the past N years from the server-supplied current_date and retrieval_as_of before the first query. Preserve the resolved start and end dates in retrieval tracks and screening; never substitute the model knowledge cutoff or a stale compact-summary date.'
  - 'A source class is determined by its authoritative record, not by a search label: a review is not a clinical trial, a journal page is not a patent, and an aggregator is not a primary publication venue. Comparative rankings may be qualitative when evidence is sparse; never invent weights, composite scores, evidence counts, maturity values, or safety grades that are not reproducible from included ledger rows and a declared method.'
  - 'Use phrase or token-bounded screening, deduplicate by stable identifier, and preserve matched terms. Never use a short alias as an unrestricted substring. Before synthesis run audit_evidence_ledger for requested source classes and resolve its issues. Fixed-size samples are never total evidence counts; do not create equal category counts, star ratings, maturity grades, or rankings. Report missing authoritative source classes as gaps, not maturity or activity claims.'
  - 'For retrieval topics that have meaningful Chinese and English terminology, search both languages with source-appropriate synonyms. Search results and snippets are discovery candidates only: use an authoritative structured record, full-text connector, or web_fetch on the exact primary page before a decision-critical source enters the evidence ledger. Return every search and source-read result to the outer agent loop before choosing the next action.'
  - 'When an exact identifier lookup returns not found or unavailable, preserve that result as an exclusion receipt. Never keep the identifier as verified, attach a different URL, or relabel it as another source class; remove it from included evidence and downgrade or remove every dependent report claim before saving.'
  - 'For a long review, create the report and editable evidence ledger as task-relative workspace files before synthesis. Update one coherent section or bounded evidence batch at a time and persist expensive retrieval before starting another track. After all sections are complete, validate and publish with save_artifacts, then return concise links. Continue across execution rounds; this is not a task-duration, attempt, source-count, or quality ceiling.'
---

# Literature review

A literature question has two halves: finding the papers a domain expert would point to, and turning them into something more useful than a reading list — a synthesis that says what's established, what's contested, what's new, and where the holes are. Both halves can fail quietly and look like competent output until someone checks.

## Read the request for what it's actually asking

"What's the paper for X" wants one or two specific citations; "what's the evidence on X" wants a synthesis; "compare A and B" wants a comparison, not two adjacent summaries; "where are the gaps" wants the gaps, with the survey as supporting material. A two-word lay query wants you to choose the scope a domain expert would default to and say so up front — "I'll take this as asking about human RCT evidence; the animal literature is separate." Ask a clarifier only when the answer would genuinely change what you do.

## Grounding: retrieve first, then write

For broad-survey, where-are-the-gaps, and compare-methods requests, the first move is a literature sweep — `search_openalex` / `crossref_lookup` from `kernel.py`, a PubMed query, `web_search`, or whichever domain connector is wired in (run `search_skills({prefix:"mcp-"})` once to see which literature/data MCP servers are available, e.g. PubMed, Semantic Scholar, bioRxiv, ClinicalTrials.gov, and use the one that fits the field) — and the answer is built from what comes back. Your recall picks the framing; the retrieval picks the citations. A real survey usually carries on the order of fifteen or more distinct primary-paper DOIs, because each claim is anchored to the paper that established it; a handful of review citations is a reading list, not a synthesis. When the question is after a *specific* paper — "the original," "the seminal," a named trial or method — find the highly-cited primary publication that the follow-ups all cite, not a review or news piece about it.

That applies even when you know the answer cold. Resolving the DOI for a paper you're certain of — the Transformer paper, a textbook constant, a landmark trial — is a one-second tool call, and it's the difference between a citation and a claim about a citation. Verification is something that happens in your tool trace, not a sentence in your reply. **A DOI you emit either resolves to a real paper that says what you claim, or it's a fabrication, and the difference is checkable in five seconds.** When you have author/year/journal but not the DOI, look it up via CrossRef or OpenAlex rather than pattern-completing one; when even those details are hazy, that's a search query, not a citation. For recent developments, contested findings, or anything you "remember" from near or after your knowledge cutoff, retrieval isn't optional.

After the first sweep, take the two or three most relevant hits and walk one step in each direction on the citation graph: pull their reference lists (backward) and their cited-by lists (forward), then fold anything new and on-topic into the set before you start writing. The seminal paper a field builds on surfaces in the backward step; the recent work that extends or contests your top hits surfaces in the forward step, and neither reliably appears in a keyword sweep alone. `expand_citations(doi)` in `kernel.py` returns both directions from OpenAlex.

### Discovery, reading, and synthesis are different stages

Use `web_search` for broad discovery, not as a proxy for reading. For every source that can change the recommendation, open its exact publisher, registry, patent-office, or authoritative repository record with `web_fetch`, `fetch_article_fulltext`, or the corresponding MCP method. Preserve the stable identifier and read enough of the abstract or structured record to screen scope; for central claims, read methods, results, limitations, and any relevant tables or claims. A title, snippet, hit count, landing page, or AI summary does not qualify a record for inclusion.

Apply a relevance-first reading pass to each selected record. Inspect its metadata, abstract or summary, table of contents or section headings, claims or methods, and result structure to locate the passages that answer the requested entities, comparators, mechanisms, endpoints, or decisions. Read those passages deeply and capture the exact supporting field or excerpt. Expand to adjacent sections or the complete record only when a key claim, limitation, comparison, or contradiction remains unresolved. Keep a compact coverage note for each included source stating what was checked, what was used, and what remains unverified; this preserves depth without mechanically reading every page of every near-match.

For cross-source or fast-moving questions, run one bounded `web_search` discovery action, inspect the returned candidates, and let the outer agent loop select the next `web_fetch`, `fetch_article_fulltext`, or authoritative connector record. Repeat only for a named evidence gap or contradiction, preserving each result before the next choice. Keep the primary records in the ledger; a generic fetched page is not automatically a paper full-text receipt. A source tool returning a complete structured record can serve as the deep-read receipt, so do not fetch the same page merely to increase tool counts.

For MCP retrieval through `repl`, inspect both `result.retrieval` and `result.evidence`. The retrieval receipt reports coverage and pagination; the evidence receipt reports whether the call produced discovery candidates, a structured record, a full-text read, or a derived summary. `host.mcp.search(...)` and `host.mcp.collect(...)` intentionally remain discovery-only even when they return many records. Select by stable identifier, then call the connector's detail, record, or full-text method for every decision-relevant source. Do not copy the private receipt labels into the user-facing report.

For topics used in both Chinese and English, run focused searches in both languages and merge by stable identifier or family. Use the source's native vocabulary and controlled terms rather than literal word-for-word translations. Recent or “latest” work gets a retrieval-as-of date and explicit date range; include older landmark evidence only when it materially explains current practice.

### Query coverage without brittle conjunctions

For a request with several technologies, populations, outcomes, or development stages, build two to four evidence tracks and search them separately. A focused discovery query normally carries two to four discriminative concepts plus a date or source filter; it should not require every requested dimension to occur in one title or abstract. Search each comparator independently, add a cross-comparison query, then merge and deduplicate by DOI, PMID, trial identifier, accession, or canonical URL.

Treat a zero-result response as a query diagnostic. Preserve its real count, then remove the least essential restriction, use a recognized synonym, or search one evidence track at a time. For source-specific databases, use the language and controlled vocabulary that source supports; cover the other user language through general discovery. Stop broadening when the source reports exhaustion or the retrieved set answers the requested evidence tracks—never pad the result with unrelated records merely to reach a number.

For a broad review, keep an editable source ledger alongside the report. Each included row should preserve the stable identifier, title, year, source type, retrieval query or track, evidence role, and inclusion rationale; excluded near-matches should retain a short exclusion reason when that decision matters. Publish the source ledger as a user-readable CSV when it materially supports audit and reuse, while raw connector JSON remains working data unless the user explicitly requests it.

### Build long reviews as durable artifacts

For a multi-track review or any request that asks for a report plus an evidence table, establish the task-relative report and ledger files before synthesis begins. Treat those files as the durable source of truth across model generations: after an expensive retrieval pass, preserve the normalized records and screening decisions; after synthesis, write one coherent report section or one bounded ledger batch, check that edit, and then move to the next track. After a correction, reread the affected files and the authoritative source receipt, synchronize exact identifiers, dates, statuses, and claims across companion artifacts, and preserve all already valid sections and rows. Do not hold the entire report or table in an unsaved assistant draft, and do not regenerate completed sections after an output boundary.

This is an execution pattern, not a stopping rule. Continue through as many later tool rounds as the unchanged task needs. When all requested sections and rows are present, validate cross-artifact identifiers and parse the ledger with a real CSV reader, publish the completed files with `save_artifacts`, and make the chat response a concise substantive summary with links rather than a duplicate full report.

Build that ledger directly from the normalized records returned by the source tools, then add only analytical columns such as evidence role, inclusion rationale, and exclusion reason. Do not manually append a trial, patent, publication, sponsor, author, date, or result from recall. If a requested evidence track has no verified record after appropriate broadening and source-specific retrieval, leave the track explicitly unsupported rather than filling it with a plausible-looking identifier. Write delimited files with a real CSV/TSV writer and validate the final header-aligned row count before saving; free-text abstracts, titles, and rationales must be quoted by the writer rather than assembled as comma-joined strings.

### Screen candidates before synthesis

Search and connector results are a candidate pool, not the evidence set. After the first sweep, pause retrieval and screen each decision-relevant candidate against one explicit eligibility statement derived from the request: population or experimental system, intervention or mechanism, route or setting, relevant endpoint, source class, and time window. Inspect at least the abstract or the source's structured record before inclusion; a title fragment, search snippet, repository landing page, or identifier match is not enough. Resume searching only for evidence tracks that still lack eligible support.

Keep the screening decision auditable. The source ledger should distinguish `candidate`, `included`, and `excluded`, and preserve `retrieval_track`, `eligibility_reason`, `exclusion_reason`, `verification_state`, `directness`, and the stable primary-source locator. Exclude a near-match when any defining scope element is wrong—for example the wrong route, indication, molecule class, intervention, endpoint, or date—even if its title shares several keywords. Do not relabel an excluded record to make it fit another track.

For deterministic first-pass screening, use `screen_records(records, required_concepts, year_from, year_to)` from `kernel.py`, or preserve equivalent phrase and token-boundary semantics. Each concept group is required, synonyms are alternatives within that group, and short aliases are matched only as complete tokens. After the abstract or structured-record review, deduplicate the included set by DOI, PMID, trial identifier, patent number, accession, or canonical URL across all retrieval tracks. A duplicate may retain multiple evidence roles in one row, but it must not be counted twice.

Prefer the authoritative record for each source class: publisher or bibliographic database for publications, the trial registry for trials, and the patent office or authoritative patent database for patents. Aggregator and social-repository pages can help discovery but are not the publication venue and should not replace a primary locator when one is available. Registry entries support protocol and posted-result facts only; patents support disclosed scope and examples, not clinical efficacy; reviews support framing and citation chasing, not primary-result counts.

Before drafting, build the report's claim set only from `included` rows. Every source identifier cited in the report must resolve to an included ledger row, and every numerical evidence count must be recomputable from the ledger or a preserved source-query receipt. Treat maturity, safety, translation timing, comparative superiority, and absence-of-evidence statements as conclusions requiring direct support; otherwise label them as bounded expert inference or omit them. Never turn search-result volume into evidence strength.

Run `audit_evidence_ledger(included_rows, required_source_types=[...])` once before synthesis, using the source classes actually requested by the user. Resolve every duplicate, mislabeled trial or patent, missing rationale, and missing source-class issue before saving. When an authoritative source class remains empty after appropriate retrieval and screening, state that no eligible record was found; do not let a review article, search snippet, or adjacent technology stand in for it.

### Patent evidence is a separate source class

When patents are requested, build a patent track rather than relabeling papers or search snippets. Combine English and Chinese keyword/synonym queries with relevant IPC/CPC classes, assignee or inventor searches, backward/forward patent citations, and family expansion. Start broad enough to discover vocabulary, then read the authoritative patent record for each decision-relevant family.

Normalize publication, application, and grant numbers; earliest priority date; family identity; jurisdictions; applicant/assignee; inventors; legal status with an as-of date; and cited non-patent literature. Count families separately from publications so one invention filed in several countries is not treated as several independent inventions.

Read at least the abstract, independent claims, and the examples or data supporting the relevant use. Distinguish what is merely described from what is claimed, and what is experimentally exemplified from what is prophetic. A patent supports disclosure, claim scope, examples, and ownership—not clinical efficacy. Compare claim overlap, design-around space, enablement evidence, and status uncertainty in professional terms; do not issue a freedom-to-operate or validity opinion unless the user explicitly asks and the jurisdictional evidence is adequate.

Patent-ledger rows preserve family and publication identifiers, priority date, jurisdiction, assignee, legal status and status date, relevant independent claims, supporting examples, technical role, inclusion rationale, and the exact authoritative locator. Report inaccessible claims, uncertain family links, or stale status as evidence gaps rather than filling them from memory.

## Retractions and the null result

Sensational papers are findable because they were sensational, and some were later retracted or failed to replicate. CrossRef's `update-to` field flags retractions; for any high-profile or surprising finding, a check takes seconds. The related trap is the question whose honest answer is "no such paper exists": when someone asks for "the paper showing X" and X fell apart or was never established, the right answer names the claim, says what happened to it, and points to what the actual evidence shows — not the closest-matching citation.

## Synthesis is comparison, not summary

A list of papers with one-sentence summaries is a bibliography. The useful layer is on top: this finding replicated, that one didn't; these three agree on the effect but disagree on mechanism; this approach wins in setting A and that one in B; this 2015 result was superseded by this 2022 one. Organize by theme or question, not by paper. For compare-methods requests the deliverable is the trade-off and a recommendation, not two summaries.

## Making the prose carry its weight

A review paragraph earns its place by opening on *your* synthetic claim and then spending citations to back it, not by opening on a citation and reporting what it found. "Chen 2019 reported a 40% reduction; Park 2020 reported 35%" is two index cards. "The effect is real but modest, with pooled estimates clustering at 35-40% (Chen 2019; Park 2020)" is a review. The diagnostic: read only the first sentence of each paragraph in sequence; if they form your argument, you've written a synthesis; if they form a list of author names, you've written an annotated bibliography in paragraph costume.

## Write prose, not a bulleted bibliography

The artifact should read like a section of a referee-grade review: paragraphs of connected argument, each making one claim and anchoring it with an inline citation, transitioning to the next. A page that is 80% bullet points is a reading list dressed up as a review — it tells the reader *that* papers exist, not what they collectively show. Reserve bullets for places a list is genuinely the right structure (a reference appendix, a head-to-head comparison table, an enumerated set of named methods); the synthesis itself is prose. If you find yourself starting consecutive lines with `- Author Year showed…`, that's a paragraph that hasn't been written yet.

## Calibrating to evidence

Say which findings are landmark and which are recent; flag preprints as preprints; note when older results were refined or overturned. Match confidence to evidence: a single-cohort finding is "one group reported X," a phase-3 RCT is stated plainly, a contested area gets both sides and an honest "unresolved." When the question contains a contested premise, engage the premise rather than building on it. When the request is about gaps, name specific ones and anchor each to what establishes it as a gap — "more research is needed" means you haven't found the actual hole.

## Put the answer in the answer — and open on the substance

The review — prose, citations, bottom line — belongs in your response text, where the reader sees it. For anything beyond a one-paper lookup, also save the full review as a markdown artifact (`save_artifacts`) so the reader has a clean, linkable document; the chat reply *is* the answer, and the artifact link goes at the end of it, never as a "Report saved:" opener. A reply that is *only* "I've saved a 14-paper review, all DOIs verified" is not an answer — write the substance in the chat, then link the artifact.

The first sentence should be content the reader came for: the finding, the paper, the comparison. "Here's the synthesis," "All DOIs verified against CrossRef; no retraction flags," "I've verified every citation," "the report is current as of today" — these are process narration, and they don't belong in the chat reply *or* the saved artifact. Verification happens in your tool trace; the reader infers it from citations that resolve and claims that hold up. Do not write a "DOIs verified / no retractions" line anywhere in the output — not as an opener, not as a footer, not as an italic subtitle under the artifact title. The artifact body follows exactly the same rule as the chat reply: open on substance, close on substance. The register to aim for is a tight methods paragraph or a referee-grade mini-review: lead with the key result, lay out the supporting evidence with inline DOIs, address the obvious counterpoint or limitation, and close on what's still open. A reader who only gets your first paragraph should already have the answer.

Cite inline as a markdown link — `[Author Year](https://doi.org/10.xxxx/xxxxxx)` — so the rendered prose reads `(Author Year)` and the DOI rides in the href where a reader can click it and a regex can still extract it. If the DOI itself contains parentheses (some publishers use PII-style suffixes, e.g. `Sxxxx-xxxx(NN)nnnnn-n`), URL-encode them as `%28` and `%29` in the href so the markdown link does not break in simpler renderers. Do not use numbered `[1][2][3]` references (they desync the moment a paragraph is reordered), and reserve the raw `(DOI: 10.xxxx/...)` form for plain-text-only output; a sentence whose visible text is half identifier is not referee-grade prose. The author names in the link text come from the retrieved record, never from recall — keep `authors` riding alongside year and DOI in whatever working notes you draft from (the lookup helpers return it for exactly this reason; only degraded fallback paths go without). A note that carries the DOI but not the names leaves `(Author Year)` to be filled from memory at prose time, and memory supplies plausible names, not the paper's. `kernel.py` provides `verify_dois`, `crossref_lookup`, `search_openalex`, `expand_citations`, and `style_pass`. Section headings are short noun phrases (six words or fewer); when you have five or more topics, group them under two or three parent `##` headings and demote the rest to `###`. The goal is that a domain expert reading your review nods along, finds the papers they'd have named themselves, and doesn't catch you in a single claim you can't back.

The auto-loaded helpers also include `screen_records` for phrase/token-boundary candidate screening and `audit_evidence_ledger` for duplicate, source-class, and missing-track checks.

## Style pass before saving

Before saving the artifact, run `style_pass(draft)` once on the full markdown. Fix the issues it lists in a single editing pass, then save; do not call it a second time and do not loop until it returns ok. It is a lint, not a gate, and a clean draft on the first pass is normal. It is shipped in this skill's `kernel.py` and auto-loaded; if `style_pass` is not in `dir()`, read `kernel.py` from this skill's directory and exec it.
