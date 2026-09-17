import { toolPublicDetailText, type ToolPublicDetailTextKey } from '@/renderer/services/i18n/toolPublicDetailLocale';

export type ToolActivityKind =
  | 'analysis'
  | 'inspect'
  | 'search'
  | 'fetch'
  | 'save'
  | 'edit'
  | 'prepare'
  | 'plan'
  | 'environment'
  | 'compute'
  | 'memory'
  | 'access'
  | 'other';

export type ToolPublicDetailKind =
  | 'analysis'
  | 'inspection'
  | 'research'
  | 'retrieval'
  | 'artifact'
  | 'file'
  | 'method'
  | 'plan'
  | 'environment'
  | 'compute'
  | 'memory'
  | 'access'
  | 'generic';

type ToolActivityDefinition = {
  kind: ToolActivityKind;
  detailKind?: ToolPublicDetailKind;
  families?: ToolActivityFamily[];
  hidden?: boolean;
  showIdentity?: boolean;
  showGenericOutput?: boolean;
};

type ToolActivityFamily = 'plan' | 'research' | 'setup' | 'execution' | 'other';

export type ToolPublicPresentationPolicy = {
  detailKind: ToolPublicDetailKind;
  showIdentity: boolean;
  showGenericOutput: boolean;
  inputBlockMode: 'inline' | 'disclosure';
  collectionsInitiallyExpanded: boolean;
};

const TOOL_ACTIVITY_DEFINITIONS: Record<string, ToolActivityDefinition> = {
  bash: { kind: 'analysis' },
  powershell: { kind: 'analysis' },
  python: { kind: 'analysis' },
  r: { kind: 'analysis' },
  repl: { kind: 'analysis' },
  operon: { kind: 'analysis' },
  code_execution: { kind: 'analysis' },
  read_file: { kind: 'inspect' },
  read_artifact_lineage: { kind: 'inspect' },
  list_artifacts: { kind: 'inspect', detailKind: 'artifact' },
  web_search: { kind: 'search' },
  websearch: { kind: 'search' },
  webresearch: { kind: 'search' },
  web_research: { kind: 'search' },
  web_fetch: { kind: 'fetch' },
  fetch_article_fulltext: { kind: 'fetch' },
  download_public_scientific_file: { kind: 'fetch', detailKind: 'retrieval' },
  search_skills: { kind: 'prepare', detailKind: 'method', showGenericOutput: false },
  tool_search_tool_regex: { kind: 'prepare', detailKind: 'method', showGenericOutput: false },
  skill: { kind: 'prepare', detailKind: 'method', showGenericOutput: false },
  load_skill: { kind: 'prepare', detailKind: 'method', showGenericOutput: false },
  save_artifacts: { kind: 'save' },
  edit_file: { kind: 'edit' },
  write_file: { kind: 'edit' },
  replace: { kind: 'edit' },
  delete_host_files: { kind: 'edit', detailKind: 'file' },
  generate_plan: {
    kind: 'plan',
    detailKind: 'plan',
    showIdentity: false,
    showGenericOutput: false,
  },
  manage_environments: { kind: 'environment' },
  manage_packages: { kind: 'environment' },
  ask_about_compute: { kind: 'compute', detailKind: 'compute' },
  compute_details: { kind: 'compute', detailKind: 'compute' },
  compute_provider: { kind: 'compute', detailKind: 'compute', families: ['execution'] },
  list_compute: { kind: 'compute', detailKind: 'compute', families: ['setup'] },
  ssh: { kind: 'compute', detailKind: 'compute', families: ['execution'] },
  scp: { kind: 'compute', detailKind: 'compute', families: ['execution'] },
  submit_job: { kind: 'compute', detailKind: 'compute', families: ['execution'] },
  wait_job: { kind: 'compute', detailKind: 'compute', families: ['execution'] },
  cancel_job: { kind: 'compute', detailKind: 'compute', families: ['execution'] },
  running_jobs: { kind: 'compute', detailKind: 'compute', families: ['execution'] },
  set_concurrency_limit: { kind: 'compute', detailKind: 'compute', families: ['execution'] },
  list_host_grants: { kind: 'access', detailKind: 'access' },
  request_host_access: { kind: 'access', detailKind: 'access' },
  request_network_access: { kind: 'access', detailKind: 'access' },
  read_memory: { kind: 'memory', detailKind: 'memory', families: ['research'] },
  search_memory: { kind: 'memory', detailKind: 'memory', families: ['research'] },
  write_memory: { kind: 'memory', detailKind: 'memory', families: ['execution'] },
  // AskUser owns its own live and history cards. Progress settlement and
  // notification waiting belong to the task-status surface. Rendering them
  // again as ordinary rows would create a competing public state path.
  ask_user: { kind: 'other', hidden: true },
  wait_for_notification: { kind: 'other', hidden: true },
  update_step_status: { kind: 'other', hidden: true },
  boundary: { kind: 'other', hidden: true },
  submit_output: { kind: 'other', hidden: true },
  verdict: { kind: 'other', hidden: true },
  record_summary: { kind: 'other', hidden: true },
  summarize_conversation: { kind: 'other', hidden: true },
  emit_memories: { kind: 'other', hidden: true },
  report_input_files: { kind: 'other', hidden: true },
  select_relevant_inputs: { kind: 'other', hidden: true },
  select_skills: { kind: 'other', hidden: true },
  create_work_item: { kind: 'other', hidden: true },
  read_onboarding_attachment: { kind: 'other', hidden: true },
};

function publicOperationName(normalized: string): string {
  const namespaced = normalized.match(/^mcp(?:__|_)(?:.+?)(?:__)(.+)$/u);
  return namespaced?.[1] ?? normalized;
}

function inferredActivityKind(normalized: string): ToolActivityKind {
  const operation = publicOperationName(normalized);
  if (/(?:^|[_-])(?:search|query|lookup|find)(?:[_-]|$)/u.test(operation)) return 'search';
  if (
    /(?:^|[_-])(?:get|retrieve)(?:[_-])(?:abstract|article|document|file|fulltext|paper|record|source|structure|study)(?:[_-]|$)/u.test(
      operation
    )
  ) {
    return 'fetch';
  }
  if (/(?:^|[_-])(?:fetch|request|download|open[_-]?url)(?:[_-]|$)/u.test(operation)) return 'fetch';
  if (/(?:^|[_-])(?:read|list|get|inspect|describe|resolve|select)(?:[_-]|$)/u.test(operation)) return 'inspect';
  if (/(?:^|[_-])(?:save|export|publish)(?:[_-]|$)/u.test(operation)) return 'save';
  if (/(?:^|[_-])(?:write|edit|patch|delete|remove|update[_-]?file)(?:[_-]|$)/u.test(operation)) return 'edit';
  if (
    /(?:^|[_-])(?:aggregate|align(?:ment)?|analy(?:se|sis|ze)|build|calculate|calculation|cluster(?:ing)?|compute|convert|count|dock(?:ing)?|fit(?:ting)?|generate|generation|liftover|link|map|model(?:ing)?|plot(?:ting)?|predict(?:ion)?|render(?:ing)?|score|scoring|simulate|simulation|translate|visuali[sz](?:e|ation))(?:[_-]|$)/u.test(
      operation
    )
  ) {
    return 'analysis';
  }
  // Scientific MCP catalogs frequently use resource nouns rather than an
  // imperative verb (for example `eqtl_associations`, `gene_variants`, or
  // `bindingdb_ligands_by_target`). Those calls retrieve/inspect evidence;
  // leaving them as a generic operation loses both the public label and the
  // research-stage grouping semantics.
  if (
    /(?:^|[_-])(?:actionability|admet|annotations?|associations?|attributes?|bioactivity|citations?|clusters?|complexes|conservation|constraint|coverage|datasets?|dependencies|details?|dosage|entries|entry|equivalents?|evidence|expression|famil(?:y|ies)|frequency|genes?|homology|info|instances?|interactions?|ligands?|matrices|mechanisms?|metadata|models?|mutation|networks?|options|overlap|papers?|phenotypes?|prediction|projects?|proteins?|reactions?|records?|references?|region|releases|samples?|scores?|sequence|sizes?|species|statistics|structures?|studies|targets?|taxa|terms?|tfbs|tissues?|tracks?|trials?|variants?|venue|versions|xrefs)(?:[_-]|$)/u.test(
      operation
    ) ||
    /(?:^|[_-])(?:accession|id)[_-]to[_-](?:accession|id)(?:[_-]|$)/u.test(operation)
  ) {
    return 'inspect';
  }
  return 'other';
}

function activityDefinition(name: string): ToolActivityDefinition {
  const normalized = name.trim().toLowerCase();
  return TOOL_ACTIVITY_DEFINITIONS[normalized] ?? { kind: inferredActivityKind(normalized) };
}

export function getToolActivityKind(name: string): ToolActivityKind {
  return activityDefinition(name).kind;
}

export function getToolPublicDetailKind(name: string): ToolPublicDetailKind {
  const definition = activityDefinition(name);
  if (definition.detailKind) return definition.detailKind;
  switch (definition.kind) {
    case 'analysis':
      return 'analysis';
    case 'inspect':
      return 'inspection';
    case 'search':
      return 'research';
    case 'fetch':
      return 'retrieval';
    case 'save':
      return 'artifact';
    case 'edit':
      return 'file';
    case 'prepare':
      return 'method';
    case 'plan':
      return 'plan';
    case 'environment':
      return 'environment';
    case 'compute':
      return 'compute';
    case 'memory':
      return 'memory';
    case 'access':
      return 'access';
    default:
      return 'generic';
  }
}

export function getToolPublicPresentationPolicy(name: string): ToolPublicPresentationPolicy {
  const definition = activityDefinition(name);
  const detailKind = getToolPublicDetailKind(name);
  return {
    detailKind,
    showIdentity: definition.showIdentity ?? detailKind !== 'plan',
    showGenericOutput: definition.showGenericOutput ?? (detailKind !== 'plan' && detailKind !== 'method'),
    // Commands and analysis code are immediately useful evidence, matching
    // the reference execution card. File bodies/diffs can be large and are a
    // genuine third disclosure level, so keep their labels visible and reveal
    // the content only on request.
    inputBlockMode: detailKind === 'file' ? 'disclosure' : 'inline',
    // Lists remain inspectable without making a long transcript expand every
    // environment/package/artifact collection on first open.
    collectionsInitiallyExpanded: false,
  };
}

const GROUPING_FAMILIES: Record<ToolActivityKind, ToolActivityFamily[]> = {
  analysis: ['execution'],
  inspect: ['research', 'execution'],
  search: ['research'],
  fetch: ['research'],
  save: ['execution'],
  edit: ['execution'],
  prepare: ['setup'],
  plan: ['plan'],
  environment: ['setup'],
  compute: ['setup'],
  memory: ['research', 'execution'],
  access: ['setup'],
  other: ['other'],
};

function groupingFamilies(name: string): ToolActivityFamily[] {
  const definition = activityDefinition(name);
  return definition.families ?? GROUPING_FAMILIES[definition.kind];
}

/**
 * Assistant prose is the primary visual boundary. When a model emits several
 * adjacent operations without prose, only operations from one coherent public
 * work family share a card. Inspection may accompany either research or an
 * execution stage; plans remain standalone.
 */
export function canGroupToolActivities(leftName: string, rightName: string): boolean {
  return canGroupToolActivitySequence([leftName, rightName]);
}

/**
 * A complete visual group must retain at least one shared work family across
 * every child. Pairwise checks alone allow an ambiguous inspection/compute row
 * to bridge two incompatible stages (for example search -> inspect -> run).
 * Intersecting the family set across the whole candidate group prevents that
 * semantic leak while preserving the reference's compact adjacent batches.
 */
export function canGroupToolActivitySequence(names: string[]): boolean {
  if (names.length <= 1) return true;
  let sharedFamilies: Set<ToolActivityFamily> | null = null;
  for (const name of names) {
    if (getToolActivityKind(name) === 'plan') return false;
    const families = new Set(groupingFamilies(name));
    sharedFamilies =
      sharedFamilies === null ? families : new Set([...sharedFamilies].filter((family) => families.has(family)));
    if (sharedFamilies.size === 0) return false;
  }
  return true;
}

export function isPublicToolActivity(name: string): boolean {
  return activityDefinition(name).hidden !== true;
}

export function toolActivityLabel(kind: ToolActivityKind, chinese: boolean): string {
  const keys: Record<ToolActivityKind, ToolPublicDetailTextKey> = {
    analysis: 'activityAnalysis',
    inspect: 'activityInspect',
    search: 'activitySearch',
    fetch: 'activityFetch',
    save: 'activitySave',
    edit: 'activityEdit',
    prepare: 'activityPrepare',
    plan: 'activityPlan',
    environment: 'activityEnvironment',
    compute: 'activityCompute',
    memory: 'activityMemory',
    access: 'activityAccess',
    other: 'activityOther',
  };
  return toolPublicDetailText(chinese, keys[kind]);
}

export function toolActivityGroupPhrase(kind: ToolActivityKind, count: number, chinese: boolean): string {
  const pluralKeys: Record<ToolActivityKind, ToolPublicDetailTextKey> = {
    analysis: 'groupAnalysis',
    inspect: 'groupInspect',
    search: 'groupSearch',
    fetch: 'groupFetch',
    save: 'groupSave',
    edit: 'groupEdit',
    prepare: 'groupPrepare',
    plan: 'groupPlan',
    environment: 'groupEnvironment',
    compute: 'groupCompute',
    memory: 'groupMemory',
    access: 'groupAccess',
    other: 'groupOther',
  };
  const singularKeys: Record<ToolActivityKind, ToolPublicDetailTextKey> = {
    analysis: 'groupAnalysisOne',
    inspect: 'groupInspectOne',
    search: 'groupSearchOne',
    fetch: 'groupFetchOne',
    save: 'groupSaveOne',
    edit: 'groupEditOne',
    prepare: 'groupPrepareOne',
    plan: 'groupPlanOne',
    environment: 'groupEnvironmentOne',
    compute: 'groupComputeOne',
    memory: 'groupMemoryOne',
    access: 'groupAccessOne',
    other: 'groupOtherOne',
  };
  return toolPublicDetailText(chinese, !chinese && count === 1 ? singularKeys[kind] : pluralKeys[kind], { count });
}
