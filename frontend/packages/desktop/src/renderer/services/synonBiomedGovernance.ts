export type SynonBiomedGovernanceOptions = {
  baseUrl?: string;
  fetchImpl?: typeof fetch;
};

export type SynonBiomedGrant = {
  kind: string;
  key: string;
  scope: string;
  tier: string;
};

export type SynonBiomedProvenanceClass = {
  name: string;
  count: number;
  withRealChecksum: number;
};

export type SynonBiomedGovernanceSnapshot = {
  gateway: {
    healthy: boolean;
    service: string;
    agentsRegistered: number;
  };
  memory: {
    enabled: boolean;
    override: boolean | null;
    totalRows: number;
    entities: number;
    sessions: number;
    categories: number;
  };
  approvals: {
    grants: SynonBiomedGrant[];
    total: number;
    allowed: number;
    denied: number;
  };
  provenance: {
    computedAt: string | null;
    durationMs: number;
    classes: SynonBiomedProvenanceClass[];
    totalArtifacts: number;
    pendingMappings: number;
    tornFinalRows: number;
    orphanEdgeRows: number;
    integrityIssues: number;
  };
};

type RecordValue = Record<string, unknown>;

export async function loadSynonBiomedGovernance(
  options: SynonBiomedGovernanceOptions = {}
): Promise<SynonBiomedGovernanceSnapshot> {
  const [healthPayload, memoryEnabledPayload, memoryContextPayload, grantsPayload, provenancePayload] =
    await Promise.all([
      getJson('/api/health', options),
      getJson('/api/memory/enabled', options),
      getJson('/api/memory/context', options),
      getJson('/api/approvals/grants', options),
      getJson('/api/system/provenance-census', options),
    ]);

  const health = asRecord(healthPayload);
  const memoryEnabled = asRecord(memoryEnabledPayload);
  const memoryContext = asRecord(memoryContextPayload);
  const grantRecords = arrayValue(asRecord(grantsPayload)?.grants).map(asRecord).filter(isRecord);
  const provenance = asRecord(provenancePayload);
  const classes = arrayValue(provenance?.classes).map(toProvenanceClass).filter(isProvenanceClass);
  const pendingMappings = numberValue(asRecord(provenance?.pending_mappings)?.total);
  const tornFinalRows = numberValue(provenance?.torn_final_rows);
  const orphanEdgeRows = numberValue(provenance?.orphan_edge_rows);
  const grants = grantRecords.map(toGrant);

  return {
    gateway: {
      healthy: stringValue(health?.status) === 'healthy',
      service: stringValue(health?.service) || 'gateway',
      agentsRegistered: numberValue(health?.agents_registered),
    },
    memory: {
      enabled: booleanValue(memoryEnabled?.enabled),
      override: nullableBooleanValue(memoryEnabled?.override),
      totalRows: numberValue(memoryContext?.total_user_rows),
      entities: arrayValue(memoryContext?.entities).length,
      sessions: arrayValue(memoryContext?.sessions).length,
      categories: arrayValue(memoryContext?.categories).length,
    },
    approvals: {
      grants,
      total: grants.length,
      allowed: grants.filter((grant) => grant.tier === 'allow').length,
      denied: grants.filter((grant) => grant.tier === 'deny').length,
    },
    provenance: {
      computedAt: nullableStringValue(provenance?.computed_at),
      durationMs: numberValue(provenance?.duration_ms),
      classes,
      totalArtifacts: classes.reduce((total, item) => total + item.count, 0),
      pendingMappings,
      tornFinalRows,
      orphanEdgeRows,
      integrityIssues: pendingMappings + tornFinalRows + orphanEdgeRows,
    },
  };
}

async function getJson(path: string, options: SynonBiomedGovernanceOptions): Promise<unknown> {
  const response = await (options.fetchImpl ?? fetch)(toGatewayUrl(path, options.baseUrl), {
    headers: { Accept: 'application/json' },
  });
  if (!response.ok) {
    throw new Error(`Synon Biomed governance request failed: ${response.status} ${path}`);
  }
  return response.json();
}

function toGatewayUrl(path: string, baseUrl = ''): string {
  return `${baseUrl.replace(/\/+$/, '')}${path}`;
}

function toGrant(record: RecordValue): SynonBiomedGrant {
  return {
    kind: stringValue(record.kind),
    key: stringValue(record.key),
    scope: stringValue(record.scope),
    tier: stringValue(record.tier),
  };
}

function toProvenanceClass(value: unknown): SynonBiomedProvenanceClass | null {
  const record = asRecord(value);
  const name = stringValue(record?.class);
  if (!name) return null;
  return {
    name,
    count: numberValue(record?.n),
    withRealChecksum: numberValue(record?.with_real_checksum),
  };
}

function asRecord(value: unknown): RecordValue | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? (value as RecordValue) : null;
}

function isRecord(value: RecordValue | null): value is RecordValue {
  return value !== null;
}

function isProvenanceClass(value: SynonBiomedProvenanceClass | null): value is SynonBiomedProvenanceClass {
  return value !== null;
}

function arrayValue(value: unknown): unknown[] {
  return Array.isArray(value) ? value : [];
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value : '';
}

function nullableStringValue(value: unknown): string | null {
  const string = stringValue(value);
  return string || null;
}

function numberValue(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0;
}

function booleanValue(value: unknown): boolean {
  return value === true;
}

function nullableBooleanValue(value: unknown): boolean | null {
  return typeof value === 'boolean' ? value : null;
}
