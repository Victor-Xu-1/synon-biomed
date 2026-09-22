/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

/**
 * Connector domains — the domain dimension the merged library page's tools tab
 * filters on.
 *
 * The connector catalog carries no domain field: `SynonBiomedMcpServer` has no
 * category, and the bundled catalog
 * (`internal/mcpdirectory/bundled_metadata.json`) declares none either — only
 * the two install candidates in `optional_metadata.json` have one, and they are
 * not installed connectors. The domain is therefore derived from metadata this
 * repository already reviews: `mcpConnectorVisuals.ts` classifies every catalog
 * connector into a reviewed biomedical glyph. `GLYPH_DOMAINS` groups those
 * reviewed glyphs into domains, `NAME_DOMAINS` covers the one catalog connector
 * that table does not list, and everything else falls to 'custom' (self-hosted)
 * or 'other' (an unclassified catalog addition) rather than being guessed.
 */

import type { SynonBiomedMcpServer } from '@/renderer/services/synonBiomedCapabilities';
import { findKnownMcpConnectorVisual } from './mcpConnectorVisuals';

export type ConnectorDomainId =
  | 'all'
  | 'literature'
  | 'chemistry'
  | 'genomics'
  | 'clinical'
  | 'structure'
  | 'omics'
  | 'pathway'
  | 'rna'
  | 'imaging'
  | 'platform'
  | 'custom'
  | 'other';

/** Display order of the domain filter; 'all' always leads. */
export const CONNECTOR_DOMAIN_IDS: readonly ConnectorDomainId[] = [
  'all',
  'literature',
  'chemistry',
  'genomics',
  'clinical',
  'structure',
  'omics',
  'pathway',
  'rna',
  'imaging',
  'platform',
  'custom',
  'other',
];

type ClassifiedDomain = Exclude<ConnectorDomainId, 'all' | 'custom' | 'other'>;

/** Reviewed connector glyph (mcpConnectorVisuals.ts) → domain. */
const GLYPH_DOMAINS: Readonly<Record<string, ClassifiedDomain>> = {
  pubmed: 'literature',
  biorxiv: 'literature',
  literature: 'literature',

  chembl: 'chemistry',
  zinc: 'chemistry',
  chemistry: 'chemistry',
  'ketcher-chemistry': 'chemistry',
  'omtx-om': 'chemistry',
  'patsnap-chemical-molecular': 'chemistry',
  'inductive-bio': 'chemistry',
  'renkin-local': 'chemistry',

  biomart: 'genomics',
  variants: 'genomics',
  'human-genetics': 'genomics',
  genomes: 'genomics',
  'genes-ontologies': 'genomics',
  'cancer-models': 'genomics',

  'clinical-trials': 'clinical',
  'clinical-genomics': 'clinical',
  'drug-regulatory': 'clinical',

  'protein-annotation': 'structure',
  'structures-interactions': 'structure',
  'boltz-api-official': 'structure',
  'tamarind-bio': 'structure',
  'adaptyv-cloud-lab': 'structure',
  cellguide: 'structure',

  expression: 'omics',
  'omics-archives': 'omics',

  regulation: 'pathway',
  'open-targets-official': 'pathway',

  rna: 'rna',
  'rna-design-local': 'rna',

  'idc-rest': 'imaging',

  'research-resources': 'platform',
};

/** Catalog connectors the reviewed visual table does not classify. */
const NAME_DOMAINS: Readonly<Record<string, ClassifiedDomain>> = {
  'synon-research': 'platform',
};

/** The catalog identifier a connector is grouped by, e.g. `bundled:pubmed`. */
function normalizedConnectorName(name: string): string {
  return (
    name
      .trim()
      .toLowerCase()
      .replace(/^bundled:/, '')
      .split('/')
      .pop() || ''
  );
}

export function resolveConnectorDomain(
  server: Pick<SynonBiomedMcpServer, 'name' | 'displayName' | 'source'>,
  isCustom: boolean
): ConnectorDomainId {
  if (isCustom || server.source.trim().toLowerCase() === 'custom') return 'custom';
  const known = findKnownMcpConnectorVisual(server.name, server.displayName);
  if (known) return GLYPH_DOMAINS[known.glyph] ?? 'other';
  return NAME_DOMAINS[normalizedConnectorName(server.name)] ?? 'other';
}
