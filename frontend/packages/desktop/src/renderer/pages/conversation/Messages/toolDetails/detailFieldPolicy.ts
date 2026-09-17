import { toolPublicDetailText, type ToolPublicDetailTextKey } from '@/renderer/services/i18n/toolPublicDetailLocale';
// Shared public-field policy for summary rows and recursively expanded data.
const PRIVATE_DETAIL_KEY =
  /(?:^|_)(?:api[_-]?key|authorization|artifact[_-]?id|backend|branch[_-]?id|call[_-]?id|cell[_-]?index|claim[_-]?token|client[_-]?message[_-]?id|content[_-]?url|cookie|credentials?|debug|details[_-]?rev|env|environment[_-]?variables|(?:environment|kernel|runtime)[_-]?generation|exec(?:ution)?[_-]?id|exit[_-]?status|frame[_-]?id|headers|http[_-]?(?:backends?|error)|human[_-]?description|input[_-]?path|job[_-]?id|journal[_-]?event[_-]?id|kernel[_-]?(?:id|kind)|mem[_-]?id|message[_-]?id|ok|omitted|operation[_-]?id|owner[_-]?id|password|private[_-]?key|project[_-]?id|publication[_-]?(?:sequence|boundary[_-]?id)|raw|remote[_-]?path|request[_-]?id|reused|root[_-]?frame[_-]?id|run[_-]?id|runner(?:[_-]?(?:attempt|id))?|script[_-]?exists|secret|session[_-]?id|source[_-]?(?:event[_-]?id|publication[_-]?sequence)|stack|status[_-]?code|storage[_-]?path|stream[_-]?uid|token|tool[_-]?use[_-]?id|trace[_-]?id|transport[_-]?error|user[_-]?id|version[_-]?id)(?:_|$)/iu;

export function normalizePublicDetailKey(key: string): string {
  return key
    .trim()
    .replace(/([A-Z]+)([A-Z][a-z])/gu, '$1_$2')
    .replace(/([a-z0-9])([A-Z])/gu, '$1_$2')
    .replace(/[\s-]+/gu, '_')
    .toLowerCase();
}

export function isPublicDetailField(key: string, level: 'summary' | 'tree'): boolean {
  const normalized = normalizePublicDetailKey(key);
  if (!normalized || normalized.startsWith('_') || PRIVATE_DETAIL_KEY.test(normalized)) return false;
  // Summary rows omit transport-heavy evidence; expanded data retains its
  // existing public record identity rules in the value projector.
  return (
    level !== 'summary' ||
    (normalized !== 'id' &&
      !/^(?:body_hash_scope|body_sha256|etag|last_modified|response_date|retrieved_at)$/u.test(normalized) &&
      !/(?:^|_)(?:checksum|logs?|trace|transport|uri)(?:_|$)/iu.test(normalized))
  );
}

export function publicDetailFieldLabel(key: string, chinese = false): string {
  const normalizedKey = normalizePublicDetailKey(key);
  const labels: Record<string, ToolPublicDetailTextKey> = {
    limit: 'limit',
    bytes_read: 'bytesRead',
    complete: 'responseComplete',
    content_type: 'contentType',
    requested_url: 'requestedSource',
    url: 'returnedSource',
    body_hash_scope: 'hashScope',
    body_sha256: 'bodyHash',
    etag: 'etag',
    last_modified: 'lastModified',
    response_date: 'responseDate',
    retrieved_at: 'retrievedAt',
    partial: 'partial',
    truncated: 'truncated',
    binary: 'binary',
    background: 'inputBackground',
    pdb_id: 'recordPdbId',
    pdb_ids: 'recordPdbIds',
    comp_id: 'recordCompoundId',
    pmcid: 'recordPmcId',
    pubmed_id: 'recordPubmedId',
    pmid: 'recordPubmedId',
    pmids: 'recordPubmedIds',
    nct_id: 'recordClinicalTrialId',
    nct_ids: 'recordClinicalTrialIds',
    gene: 'recordGene',
    genes: 'recordGenes',
    variant: 'recordVariant',
    variants: 'recordVariants',
    score: 'recordScore',
    chain: 'recordChain',
    ligand: 'recordLigand',
    ligand_resname: 'recordLigandResidue',
    target: 'recordTarget',
    organism: 'recordOrganism',
    cutoff_angstrom: 'recordDistanceCutoff',
    exhaustiveness: 'recordExhaustiveness',
    num_modes: 'recordPoseCount',
    model: 'recordModel',
    assay: 'recordAssay',
    units: 'recordUnits',
    temperature: 'recordTemperature',
    ligands: 'recordLigands',
    structures: 'recordStructures',
    articles: 'recordArticles',
    title: 'recordTitle',
    abstract: 'recordAbstract',
    available: 'recordAvailability',
    reason: 'recordRetrievalNote',
    source: 'recordSource',
    source_url: 'recordSourceUrl',
    uniprot_accession: 'recordUniprot',
    max_candidates: 'recordCandidateLimit',
    max_resolution_angstrom: 'recordResolutionLimit',
    min_carbon_atoms: 'recordMinimumCarbon',
    exclude_common_crystallization_components: 'recordExcludeCrystallization',
    selected: 'recordSelection',
    inspected_candidate_count: 'recordInspectedCandidates',
    search_truncated: 'recordSearchCoverage',
    bytes_written: 'recordBytesWritten',
    created: 'recordCreation',
    success: 'recordOutcome',
    name: 'recordName',
    compatible_environment_count: 'recordCompatibleEnvironments',
    feasible: 'recordFeasibility',
    mode: 'recordCheckMode',
    operation: 'recordOperation',
    recommendation: 'recordRecommendation',
    mutation_blockers: 'recordEnvironmentLimits',
  };
  const label = labels[normalizedKey];
  if (label) return toolPublicDetailText(chinese, label);
  if (normalizedKey === 'doi') return 'DOI';
  if (normalizedKey === 'smiles') return 'SMILES';
  if (normalizedKey === 'ph') return 'pH';
  return normalizedKey.replace(/_/gu, ' ').replace(/\b\w/gu, (character) => character.toUpperCase());
}
