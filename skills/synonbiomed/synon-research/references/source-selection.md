# Source selection and focused reading

## Choose sources by evidence role

Start from the question, not from the database catalog. Match each track to the source class that can actually support it:

- target identity, function, expression, pathways and structure: authoritative biological and structural databases;
- human causal support and patient stratification: genetics, variant, QTL, cohort and disease-ontology records;
- compound mechanism and pharmacology: curated chemistry, binding, mechanism, assay and regulatory records;
- clinical development: registry protocol fields, posted results, regulator documents and primary publications;
- competitive and legal scope: authoritative patent records, families, claims, examples and dated legal status;
- novelty, replication and controversy: primary literature, citation links, retraction/correction status and relevant negative evidence.

Use the source's native vocabulary and controlled identifiers. For questions with meaningful Chinese and English terminology, search both languages where the source supports them and merge by stable identifiers. Do not translate an official gene symbol, accession, trial ID, patent number, Latin name, or chemical identifier merely to satisfy bilingual coverage.

## Use one MCP entry without flattening source semantics

The independent `synon-research` MCP is a compact bridge to the operator-verified upstream research capability pack. It exposes three operations:

1. `life_science_sources`: search available source capabilities;
2. `life_science_source_contract`: inspect one source's whitelisted operations and provenance;
3. `life_science_source`: execute one selected JSON helper with bounded input, time, output and a source hash receipt.

The bridge does not make all sources interchangeable. Prefer a native typed MCP method when it provides a more complete record or honest pagination. Use `web_search` for broad and multilingual discovery, return its candidates to the outer agent loop, and use `web_fetch` or a full-text tool for the selected primary page. Use the patent route for authoritative patent records. Load another specialized Skill only when its domain method materially changes execution; do not load a stack of generic research Skills that repeat the same instructions.

Use the external bridge for a real capability gap, not to repeat a source already read through a stronger route. Merge records by stable identifier and retain the source-specific receipt so evidence gathered by different tools remains auditable in one ledger.

An unavailable external package, an unavailable source, an empty result, an unknown total and a truncated page are different states. Preserve the real state. Broaden a query only when the track remains unsupported, and change one important concept at a time so the effect is auditable.

Treat a source that needs an optional credential, contact field, subscription or account setting as unavailable for the current pass. Continue through an equivalent public authority first; ask the user only when that exact source is uniquely necessary and the remaining choice is genuinely user-owned.

## Read for relevance

Inspect metadata and record structure first. Read the passages or fields that answer the research track plus their immediate context. Expand to the complete record when a material limitation, conflict, comparison or legal interpretation cannot be resolved from the focused sections.

Prioritize depth using directness, study design, sample or experimental coverage, endpoint relevance, recency where material, independent corroboration and likely decision impact. Do not read every document end to end, and do not shallow-read every search result.

Stop broad discovery when source pagination is exhausted, materially different searches yield no new eligible evidence, or the decision-relevant evidence classes are adequately represented. Continue only for a named gap or contradiction. A fixed page size is a transport boundary, not an evidence quota.
