import { getToolActivityKind } from '../toolActivityPresentationRegistry';
import { sanitizePublicTaskText } from './toolPublicDetailBlocks';

const URL_PATTERN = /https?:\/\/[^\s<>"'\\]+/giu;
const TRAILING_PUNCTUATION = /[),.;:\]}]+$/u;
const DEFAULT_RESULT_LIMIT = 50;
const MAX_TITLE_CHARACTERS = 180;
const MAX_SNIPPET_CHARACTERS = 320;

export type ResearchSourceResult = {
  key: string;
  title: string;
  url: string | null;
  source: string | null;
  snippet: string | null;
};

export type ResearchSourcePresentation = {
  query: string | null;
  results: ResearchSourceResult[];
};

export function isResearchActivityTool(name: string): boolean {
  // Search results use the dedicated source list. Retrieval is deliberately
  // different: the source identity belongs in the operation detail and the
  // retrieved text, download receipt, or file metadata stays available behind
  // its evidence disclosure instead of being replaced by another link list.
  return getToolActivityKind(name) === 'search';
}

export function extractResearchSourcePresentation(
  input: string | undefined,
  output: string | undefined,
  limit = DEFAULT_RESULT_LIMIT
): ResearchSourcePresentation {
  const parsedInput = parseJson(input);
  const parsedOutput = parseJson(output);
  const query = firstTextAtKeys(
    [parsedInput, parsedOutput],
    ['query', 'search_query', 'term', 'keywords', 'condition', 'intervention']
  );
  const results: ResearchSourceResult[] = [];
  const seen = new Set<string>();

  collectStructuredResults(parsedOutput, results, seen, limit);
  // URL and identifier scraping are fallbacks for plain-text connector output
  // only. Re-scanning serialized structured output can turn nested diagnostics,
  // snippets, or provider metadata into extra source cards that were not part
  // of the returned source collection, making the summary count disagree with
  // the expanded list.
  if (parsedOutput === null && results.length < limit && output) {
    collectTextUrls(output, results, seen, limit);
    if (results.length < limit) collectTextIdentifiers(output, results, seen, limit);
  }

  return { query, results };
}

function collectTextIdentifiers(
  output: string,
  results: ResearchSourceResult[],
  seen: Set<string>,
  limit: number
): void {
  const trimmed = output.trim();
  if (!trimmed || trimmed.length > 4096) return;
  for (const match of trimmed.matchAll(/\b(?:PMC\d+|GSE\d+|[0-9][A-Za-z0-9]{3})\b/giu)) {
    const identifier = match[0];
    const result = /^PMC/iu.test(identifier)
      ? pmcResult(identifier)
      : /^GSE/iu.test(identifier)
        ? accessionResult(identifier)
        : pdbResult(identifier);
    if (result) appendResult(result, results, seen, limit);
    if (results.length >= limit) return;
  }
}

function collectStructuredResults(
  value: unknown,
  results: ResearchSourceResult[],
  seen: Set<string>,
  limit: number,
  depth = 0
): void {
  if (value == null || results.length >= limit || depth > 8) return;
  if (typeof value === 'string') {
    const parsed = parseJson(value);
    if (parsed !== null && parsed !== value) {
      collectStructuredResults(parsed, results, seen, limit, depth + 1);
    }
    return;
  }
  if (Array.isArray(value)) {
    for (const item of value) {
      collectStructuredResults(item, results, seen, limit, depth + 1);
      if (results.length >= limit) return;
    }
    return;
  }
  if (!isRecord(value)) return;

  const result = resultFromRecord(value);
  if (result) appendResult(result, results, seen, limit);

  collectIdentifierResults(value, results, seen, limit);

  // Native and federated search envelopes may expose both raw provider
  // `results` and the canonical ranked/deduplicated `sources` collection.
  // Once canonical sources exist they are the public contract; traversing the
  // raw candidates as well inflates the disclosure and makes it disagree with
  // the authoritative compact count.
  if (Array.isArray(value.sources) && value.sources.length > 0) {
    collectStructuredResults(value.sources, results, seen, limit, depth + 1);
    return;
  }

  for (const key of [
    'result',
    'results',
    'records',
    'articles',
    'works',
    'studies',
    'trials',
    'targets',
    'compounds',
    'entries',
    'structures',
    'datasets',
    'variants',
    'items',
    'content',
    'sources',
    'references',
    'data',
  ]) {
    if (results.length >= limit) return;
    collectStructuredResults(value[key], results, seen, limit, depth + 1);
  }
}

function collectIdentifierResults(
  record: Record<string, unknown>,
  results: ResearchSourceResult[],
  seen: Set<string>,
  limit: number
): void {
  const collections: Array<{
    keys: string[];
    toResult: (identifier: string) => ResearchSourceResult | null;
  }> = [
    { keys: ['pmids', 'pubmed_ids'], toResult: pubmedResult },
    { keys: ['pmcids', 'pmc_ids'], toResult: pmcResult },
    { keys: ['dois'], toResult: doiResult },
    { keys: ['pdb_ids', 'pdbids'], toResult: pdbResult },
    { keys: ['accessions'], toResult: accessionResult },
    { keys: ['urls', 'links'], toResult: urlResult },
  ];

  for (const collection of collections) {
    for (const key of collection.keys) {
      const value = record[key];
      if (!Array.isArray(value)) continue;
      for (const item of value) {
        if (typeof item !== 'string' && typeof item !== 'number') continue;
        const result = collection.toResult(String(item).trim());
        if (result) appendResult(result, results, seen, limit);
        if (results.length >= limit) return;
      }
    }
  }
}

function pubmedResult(identifier: string): ResearchSourceResult | null {
  if (!/^\d{4,12}$/u.test(identifier)) return null;
  const url = `https://pubmed.ncbi.nlm.nih.gov/${identifier}/`;
  return identifierResult(`PubMed PMID ${identifier}`, url);
}

function pmcResult(identifier: string): ResearchSourceResult | null {
  const normalized = identifier.toUpperCase();
  if (!/^PMC\d+$/u.test(normalized)) return null;
  const url = `https://pmc.ncbi.nlm.nih.gov/articles/${normalized}/`;
  return identifierResult(normalized, url);
}

function doiResult(identifier: string): ResearchSourceResult | null {
  const url = normalizeDoi(identifier);
  return url ? identifierResult(identifier.replace(/^doi:\s*/iu, ''), url) : null;
}

function pdbResult(identifier: string): ResearchSourceResult | null {
  const normalized = identifier.toUpperCase();
  if (!/^[0-9][A-Z0-9]{3}$/u.test(normalized)) return null;
  return identifierResult(`RCSB PDB ${normalized}`, `https://www.rcsb.org/structure/${normalized}`);
}

function accessionResult(identifier: string): ResearchSourceResult | null {
  const url = accessionUrl(identifier);
  return url ? identifierResult(identifier.toUpperCase(), url) : null;
}

function urlResult(identifier: string): ResearchSourceResult | null {
  const url = normalizeUrl(identifier);
  return url ? identifierResult(formatUrlLabel(url), url) : null;
}

function identifierResult(title: string, url: string): ResearchSourceResult {
  return {
    key: url,
    title,
    url,
    source: url,
    snippet: null,
  };
}

function resultFromRecord(record: Record<string, unknown>): ResearchSourceResult | null {
  const title = safePublicResearchText(firstString(record, ['title', 'name', 'label', 'pref_name']));
  const pmid = firstString(record, ['pmid']);
  const pmcid = firstString(record, ['pmcid']);
  const accession = firstValidPublicSemanticIdentity([
    firstString(record, ['accession', 'identifier', 'pdb_id']),
    pmid,
    pmcid,
    firstPublicSemanticIdentity(record),
  ]);
  const explicitUrl = firstString(record, ['url', 'link', 'href', 'uri']);
  const doi = firstString(record, ['doi']);
  const url =
    normalizeUrl(explicitUrl) ?? normalizeDoi(doi) ?? publicationIdentifierUrl(pmid, pmcid) ?? accessionUrl(accession);
  if (!title && !url && !accession) return null;

  const publicTitle = truncate(title ?? accession ?? formatUrlLabel(url!), MAX_TITLE_CHARACTERS);
  const snippet = safePublicResearchText(firstString(record, ['snippet', 'summary', 'description', 'abstract']));
  const source = accession ?? url;
  const key = url ?? `${accession ?? 'result'}:${publicTitle}`;
  return {
    key,
    title: publicTitle,
    url,
    source,
    snippet: snippet ? truncate(normalizeWhitespace(snippet), MAX_SNIPPET_CHARACTERS) : null,
  };
}

function collectTextUrls(output: string, results: ResearchSourceResult[], seen: Set<string>, limit: number): void {
  for (const match of output.matchAll(URL_PATTERN)) {
    const url = normalizeUrl(match[0].replace(TRAILING_PUNCTUATION, ''));
    if (!url) continue;
    appendResult(
      {
        key: url,
        title: formatUrlLabel(url),
        url,
        source: url,
        snippet: null,
      },
      results,
      seen,
      limit
    );
    if (results.length >= limit) return;
  }
}

function appendResult(
  result: ResearchSourceResult,
  results: ResearchSourceResult[],
  seen: Set<string>,
  limit: number
): void {
  if (results.length >= limit) return;
  const identity = result.url?.toLowerCase() ?? result.key.toLowerCase();
  if (seen.has(identity)) return;
  seen.add(identity);
  results.push(result);
}

function firstTextAtKeys(values: unknown[], keys: string[]): string | null {
  for (const value of values) {
    const match = findTextAtKeys(value, keys);
    const sanitized = safePublicResearchText(match);
    if (sanitized) return truncate(sanitized, MAX_TITLE_CHARACTERS);
  }
  return null;
}

function findTextAtKeys(value: unknown, keys: string[], depth = 0): string | null {
  if (!isRecord(value) || depth > 4) return null;
  const direct = firstString(value, keys);
  if (direct) return direct;
  for (const key of ['result', 'data', 'request', 'input']) {
    const nested = findTextAtKeys(value[key], keys, depth + 1);
    if (nested) return nested;
  }
  return null;
}

function firstString(record: Record<string, unknown>, keys: string[]): string | null {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === 'string' && value.trim()) return value.trim();
  }
  return null;
}

function normalizeUrl(value: string | null): string | null {
  if (!value) return null;
  try {
    const parsed = new URL(value);
    return (parsed.protocol === 'http:' || parsed.protocol === 'https:') &&
      !parsed.username &&
      !parsed.password &&
      !urlContainsCredentialMaterial(parsed) &&
      isPublicResearchHostname(parsed.hostname)
      ? parsed.toString()
      : null;
  } catch {
    return null;
  }
}

function isPublicResearchHostname(value: string): boolean {
  const hostname = value.toLowerCase().replace(/^\[|\]$/gu, '');
  if (!hostname || hostname === 'localhost' || hostname.endsWith('.localhost') || hostname.endsWith('.local')) {
    return false;
  }
  if (hostname.includes(':')) {
    return (
      hostname !== '::' &&
      hostname !== '::1' &&
      !hostname.startsWith('::ffff:') &&
      !hostname.startsWith('fc') &&
      !hostname.startsWith('fd') &&
      !/^fe[89ab]/u.test(hostname) &&
      !hostname.startsWith('ff')
    );
  }
  if (!/^\d+(?:\.\d+){3}$/u.test(hostname)) return hostname.includes('.');
  const octets = hostname.split('.').map(Number);
  if (octets.some((octet) => !Number.isInteger(octet) || octet < 0 || octet > 255)) return false;
  const [first, second] = octets;
  return !(
    first === 0 ||
    first === 10 ||
    first === 127 ||
    (first === 100 && second >= 64 && second <= 127) ||
    (first === 169 && second === 254) ||
    (first === 172 && second >= 16 && second <= 31) ||
    (first === 192 && second === 168) ||
    first >= 224
  );
}

function normalizeDoi(value: string | null): string | null {
  if (!value) return null;
  const normalized = value
    .replace(/^https?:\/\/doi\.org\//iu, '')
    .replace(/^doi:\s*/iu, '')
    .trim();
  return normalized ? normalizeUrl(`https://doi.org/${normalized}`) : null;
}

function accessionUrl(value: string | null): string | null {
  if (!value || !/^GSE\d+$/iu.test(value)) return null;
  return `https://www.ncbi.nlm.nih.gov/geo/query/acc.cgi?acc=${encodeURIComponent(value.toUpperCase())}`;
}

function publicationIdentifierUrl(pmid: string | null, pmcid: string | null): string | null {
  if (pmcid && /^PMC\d+$/iu.test(pmcid)) {
    return `https://pmc.ncbi.nlm.nih.gov/articles/${pmcid.toUpperCase()}/`;
  }
  if (pmid && /^\d{4,12}$/u.test(pmid)) {
    return `https://pubmed.ncbi.nlm.nih.gov/${pmid}/`;
  }
  return null;
}

function firstPublicSemanticIdentity(record: Record<string, unknown>): string | null {
  for (const [key, candidate] of Object.entries(record)) {
    const normalizedKey = key
      .trim()
      .replace(/([A-Z]+)([A-Z][a-z])/gu, '$1_$2')
      .replace(/([a-z0-9])([A-Z])/gu, '$1_$2')
      .toLowerCase();
    if (!/(?:^|_)(?:accession|identifier|id)$/u.test(normalizedKey)) continue;
    if (
      /(?:^|_)(?:artifact|backend|branch|call|client|exec|frame|job|kernel|message|operation|owner|project|request|run|runner|session|stream|tool|user|version)(?:_|$)/u.test(
        normalizedKey
      )
    ) {
      continue;
    }
    if (typeof candidate !== 'string' && typeof candidate !== 'number') continue;
    const value = String(candidate).trim();
    if (!value || value.length > 80 || !/^[\p{L}\p{N}][\p{L}\p{N}._:+/-]*$/u.test(value)) continue;
    if (/^(?:mem_|swr-|synon[-_])|^[0-9a-f]{8}-[0-9a-f-]{20,}$/iu.test(value)) continue;
    return value;
  }
  return null;
}

function firstValidPublicSemanticIdentity(values: Array<string | null>): string | null {
  for (const value of values) {
    if (!value) continue;
    const normalized = value.trim();
    if (
      normalized.length > 0 &&
      normalized.length <= 80 &&
      /^[\p{L}\p{N}][\p{L}\p{N}._:+/-]*$/u.test(normalized) &&
      !/^(?:mem_|proj(?:ect)?_|artifact_|frame_|swr-|synon[-_])|^[0-9a-f]{8}-[0-9a-f-]{20,}$/iu.test(normalized)
    ) {
      return normalized;
    }
  }
  return null;
}

function safePublicResearchText(value: string | null): string | null {
  if (!value) return null;
  const sanitized = sanitizePublicTaskText(value);
  return sanitized ? normalizeWhitespace(sanitized) : null;
}

function urlContainsCredentialMaterial(value: URL): boolean {
  const sensitiveParameter =
    /(?:^|[_-])(?:api[_-]?key|auth(?:orization)?|credential|password|secret|sig(?:nature)?|token)(?:[_-]|$)/iu;
  const secretLikeValue = /^(?:bearer\s+|gh[pousr]_|sk-)[A-Za-z0-9_-]{8,}/iu;
  for (const [key, candidate] of value.searchParams) {
    if (sensitiveParameter.test(key) || secretLikeValue.test(candidate.trim())) return true;
  }
  return sensitiveParameter.test(value.hash.replace(/^#/u, '')) || secretLikeValue.test(value.hash.slice(1));
}

function formatUrlLabel(value: string): string {
  try {
    const url = new URL(value);
    const path = decodeURIComponent(url.pathname).replace(/\/$/u, '');
    return truncate(`${url.hostname.replace(/^www\./u, '')}${path && path !== '/' ? path : ''}`, MAX_TITLE_CHARACTERS);
  } catch {
    return value;
  }
}

function parseJson(value: string | undefined): unknown {
  if (!value) return null;
  try {
    return JSON.parse(value);
  } catch {
    return null;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value && typeof value === 'object' && !Array.isArray(value));
}

function normalizeWhitespace(value: string): string {
  return value.replace(/\s+/gu, ' ').trim();
}

function truncate(value: string, maxCharacters: number): string {
  const characters = Array.from(value);
  return characters.length <= maxCharacters ? value : `${characters.slice(0, maxCharacters - 1).join('')}…`;
}
