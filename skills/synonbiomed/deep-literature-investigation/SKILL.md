---
name: deep-literature-investigation
description: Deep scientific evidence synthesis for multi-part biomedical questions. Extends the canonical literature-review retrieval path with explicit evidence grading, contradiction analysis, saturation checks, and an editable evidence ledger; it does not introduce a second search stack.
allowed-tools: search_skills, skill, repl, web_search, web_fetch, fetch_article_fulltext, save_artifacts
metadata:
  dependencies:
    skills:
      - literature-review
---

# Deep Literature Investigation

Use this Skill only for the additional depth needed by a systematic, multi-claim, or contested evidence question. The Harness composes the required `literature-review` contract before this one. Its source grounding, multilingual discovery, pagination, stable-identifier deduplication, truthful coverage, citation verification, and source-ledger rules remain the single retrieval path. Do not replace them with a separate `web_search` loop, guessed identifiers, or an ad-hoc model extraction pipeline.

## Scope the evidence

1. Split the question into independent evidence tracks that can succeed or fail separately.
2. For each track, record the population or system, intervention or mechanism, comparator, outcome, time range, and appropriate source classes.
3. Use focused Chinese and English query variants plus source-specific controlled vocabulary. Do not join every requested dimension into one brittle conjunction.
4. Resolve relative time windows from the server-supplied current date before retrieval and keep the same explicit bounds in every track, screen, count, artifact, and conclusion.

## Keep long work durable

Create the task-relative report and editable evidence-ledger files before synthesis. Use them as the durable state of the unchanged logical task: after an expensive retrieval or screening pass, preserve the normalized records and decisions; then write one coherent report section or one bounded ledger batch and validate that edit before advancing to the next evidence track. If a provider output boundary interrupts drafting, resume through the files instead of regenerating the full report in chat. After a repair, reread the affected files and the authoritative source receipt, then synchronize exact identifiers, dates, statuses, and claims across companion artifacts while preserving already valid sections and rows. Continue across later execution rounds until every requested section is complete; this workflow sets no task-duration, attempt, source-count, or quality ceiling.

After the report and ledger are complete, validate their cross-artifact identifiers and CSV structure, publish both with `save_artifacts`, and return a concise substantive answer with artifact links. The saved deliverables carry the full analysis; the chat answer should not duplicate an oversized report.

## Retrieve and deepen

For every track:

1. Use the canonical `literature-review` route and the appropriate live MCP connector or `web_search` interface. Return discovery to the outer agent loop, then read selected primary records with the connector detail/full-text method, `web_fetch`, or `fetch_article_fulltext`.
2. Follow advertised pagination with `host.mcp.search` or `host.mcp.collect` for MCP sources. Preserve query and page receipts, provider totals, returned counts, duplicate counts, truncation, and stop reason.
   Treat their `result.evidence.stage` as `discovery`; use stable identifiers with the connector's detail, record, or full-text method before evidence enters a claim map. A direct MCP call also exposes `result.evidence`, so preserve the distinction between structured-record fields and full-text sections without altering the provider payload.
3. Treat a zero or small page as a query diagnostic. Broaden one concept, use a recognized synonym, or change to the appropriate source-specific interface; never convert one empty page into an absence claim.
4. Fetch and inspect the most decision-relevant primary sources. A discovery snippet can prioritize a source but cannot support a detailed methods, result, claim, or legal-scope statement.
5. Build the editable ledger directly from retrieved normalized records. Add analytical fields only after the source identifiers and metadata are fixed.

Use relevance-first deep reading: inspect metadata, abstract or summary, section structure, claims or methods, and result tables to locate the passages that answer the requested question. Read those passages and their immediate context in full, recording a short coverage note (checked, used, unresolved). Expand to the complete record only when a material claim, limitation, comparison, or contradiction cannot be resolved from the focused passages. This is a depth-and-efficiency rule, not a fixed page or source quota.

Before citation expansion or synthesis, screen the candidate pool using the canonical `literature-review` eligibility statement. Keep wrong-route, wrong-indication, wrong-intervention, wrong-endpoint, out-of-window, and secondary-only near-matches as excluded audit rows rather than allowing them into the evidence set. Continue retrieval only for tracks that remain unsupported after screening.

For the two or three strongest anchor papers in each central track, inspect one citation-graph step backward and forward when that interface is available. The backward step should surface the foundational method or result; the forward step should surface replication, extension, qualification, and contradiction. Do not let citation count substitute for directness or study quality.

Stop broadening a track when the source reports exhaustion, successive materially different queries add no eligible evidence, or the retrieved set covers the decision-relevant evidence categories. The stop decision is evidence-based, not a fixed number of pages or records.

## Build the argument from evidence

Maintain a claim-evidence map before drafting. Each decision-relevant claim must have:

- a precise claim statement and scope;
- at least one primary-source location returned by a successful source tool;
- the exact supporting excerpt or structured response field path;
- study design, model or population, sample size, comparator, intervention or exposure, endpoint, and uncertainty when reported;
- evidence that narrows, contradicts, or limits the claim;
- an explicit confidence level and the reason for it.

When evidence is available from only one group, one model, a preprint, a registry without results, or a patent example, say exactly that. Search for negative, null, failed, and non-replicating results as deliberately as positive results when they could change the decision. A strong review explains why the evidence supports the conclusion, where that inference stops, which alternative explanation remains plausible, and what new observation would change the ranking.

## Grade evidence

Grade each claim using the design appropriate to its field rather than one universal hierarchy. Record at least:

- source type and study design;
- directness to the question;
- sample size or experimental coverage when reported;
- comparator and endpoint relevance;
- replication or independent corroboration;
- material limitations and risk of bias;
- whether the source is primary, secondary, preprint, registry-only, or patent disclosure.

Do not label in-vitro, computational, patent, registry, observational, and randomized evidence as interchangeable. A patent disclosure supports what was disclosed, not that an experiment succeeded; a trial registry supports protocol facts unless posted results are present.

## Resolve contradictions

When sources disagree:

1. Keep both source-backed positions in the ledger.
2. Compare study design, population or model, formulation or dose, assay, endpoint, timing, and statistical uncertainty.
3. State whether the conflict is explained, plausibly explained, or unresolved.
4. Calibrate the final confidence to the strongest direct evidence and the unresolved conflict, not to the number of citations.

## Deliverables

Save both:

- a readable report with the search scope, findings by evidence track, contradictions, gaps, confidence, and decision implications;
- an editable CSV source ledger with stable identifier, title, year, source type, query track, evidence role, inclusion rationale, exclusion reason where material, and verification state.

The ledger must also make candidate disposition and directness explicit. The report may cite only included rows, and evidence counts must be reproducible from those rows or preserved source receipts; discovery-result totals, aggregator pages, and excluded near-matches do not count as supporting evidence.

The report should be organized by questions or competing explanations, not paper-by-paper summaries. Each major section should open with the synthesized conclusion, then present the strongest direct evidence, independent corroboration, conflicting or limiting evidence, and the resulting confidence. Recommendations must trace back to this argument rather than to citation volume or a mechanically averaged score.

Every identifier and factual source field must come from a successful retrieval record. Each cited ledger row must retain a `source_url` or equivalent stable source locator, `source_type`, and an exact supporting excerpt or structured field path. Validate the CSV with a real delimited-file parser before `save_artifacts`; keep raw connector payloads as working data unless the user requests them.
