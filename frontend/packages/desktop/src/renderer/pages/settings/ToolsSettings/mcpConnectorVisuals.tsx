import React from 'react';
import { SettingsGeneratedIcon, type SettingsGeneratedIconId } from '../components/SettingsGeneratedAsset';

export type McpVisualTone = 'blue' | 'cyan' | 'teal' | 'green' | 'amber' | 'orange' | 'rose' | 'violet';

type VisualDefinition = {
  glyph: string;
  tone: McpVisualTone;
  iconAsset: SettingsGeneratedIconId;
};

/**
 * Stable connector identifiers are retained for telemetry and accessibility.
 * Rendered marks come exclusively from the reviewed image-2 asset set. A
 * semantic asset may be shared by several services; this avoids inventing
 * service logos while keeping the visual language consistent.
 */
const KNOWN_VISUALS = {
  biomart: { glyph: 'biomart', tone: 'teal', iconAsset: 'connector-database' },
  pubmed: { glyph: 'pubmed', tone: 'blue', iconAsset: 'connector-book' },
  'clinical-trials': {
    glyph: 'clinical-trials',
    tone: 'cyan',
    iconAsset: 'connector-clipboard',
  },
  chembl: { glyph: 'chembl', tone: 'violet', iconAsset: 'connector-molecule' },
  biorxiv: { glyph: 'biorxiv', tone: 'rose', iconAsset: 'connector-book' },
  variants: { glyph: 'variants', tone: 'violet', iconAsset: 'connector-dna' },
  'clinical-genomics': {
    glyph: 'clinical-genomics',
    tone: 'blue',
    iconAsset: 'connector-clipboard',
  },
  expression: { glyph: 'expression', tone: 'green', iconAsset: 'connector-bars' },
  regulation: { glyph: 'regulation', tone: 'orange', iconAsset: 'connector-regulation' },
  'protein-annotation': {
    glyph: 'protein-annotation',
    tone: 'teal',
    iconAsset: 'connector-protein',
  },
  rna: { glyph: 'rna', tone: 'rose', iconAsset: 'connector-rna' },
  'structures-interactions': {
    glyph: 'structures-interactions',
    tone: 'cyan',
    iconAsset: 'connector-cube',
  },
  'omics-archives': { glyph: 'omics-archives', tone: 'blue', iconAsset: 'storage' },
  'genes-ontologies': {
    glyph: 'genes-ontologies',
    tone: 'green',
    iconAsset: 'connector-regulation',
  },
  'drug-regulatory': {
    glyph: 'drug-regulatory',
    tone: 'orange',
    iconAsset: 'connector-regulation',
  },
  'research-resources': {
    glyph: 'research-resources',
    tone: 'amber',
    iconAsset: 'connector-book',
  },
  'cancer-models': {
    glyph: 'cancer-models',
    tone: 'rose',
    iconAsset: 'connector-protein',
  },
  chemistry: { glyph: 'chemistry', tone: 'violet', iconAsset: 'connector-molecule' },
  'human-genetics': { glyph: 'human-genetics', tone: 'blue', iconAsset: 'connector-dna' },
  literature: { glyph: 'literature', tone: 'amber', iconAsset: 'connector-book' },
  genomes: { glyph: 'genomes', tone: 'cyan', iconAsset: 'connector-dna' },
  cellguide: { glyph: 'cellguide', tone: 'teal', iconAsset: 'connector-protein' },
  zinc: { glyph: 'zinc', tone: 'violet', iconAsset: 'connector-molecule' },
  'open-targets-official': {
    glyph: 'open-targets-official',
    tone: 'blue',
    iconAsset: 'connector-regulation',
  },
  'tamarind-bio': {
    glyph: 'tamarind-bio',
    tone: 'green',
    iconAsset: 'connector-protein',
  },
  'adaptyv-cloud-lab': {
    glyph: 'adaptyv-cloud-lab',
    tone: 'cyan',
    iconAsset: 'connector-clipboard',
  },
  'ketcher-chemistry': {
    glyph: 'ketcher-chemistry',
    tone: 'violet',
    iconAsset: 'connector-molecule',
  },
  'omtx-om': {
    glyph: 'omtx-om',
    tone: 'violet',
    iconAsset: 'connector-molecule',
  },
  'patsnap-chemical-molecular': {
    glyph: 'patsnap-chemical-molecular',
    tone: 'violet',
    iconAsset: 'connector-molecule',
  },
  'inductive-bio': {
    glyph: 'inductive-bio',
    tone: 'cyan',
    iconAsset: 'connector-bars',
  },
  'boltz-api-official': {
    glyph: 'boltz-api-official',
    tone: 'blue',
    iconAsset: 'connector-protein',
  },
  'renkin-local': {
    glyph: 'renkin-local',
    tone: 'orange',
    iconAsset: 'connector-regulation',
  },
  'rna-design-local': { glyph: 'rna-design-local', tone: 'rose', iconAsset: 'connector-rna' },
  'idc-rest': { glyph: 'idc-rest', tone: 'cyan', iconAsset: 'connector-database' },
} as const satisfies Record<string, VisualDefinition>;

const FALLBACK_VISUALS = [
  { glyph: 'custom-orbit', tone: 'blue', iconAsset: 'connector-regulation' },
  { glyph: 'custom-grid', tone: 'teal', iconAsset: 'connector-clipboard' },
  { glyph: 'custom-wave', tone: 'violet', iconAsset: 'connector-rna' },
  { glyph: 'custom-cube', tone: 'cyan', iconAsset: 'connector-cube' },
  { glyph: 'custom-link', tone: 'green', iconAsset: 'connector-protein' },
  { glyph: 'custom-spark', tone: 'amber', iconAsset: 'connector-book' },
] as const satisfies readonly VisualDefinition[];

export type McpVisualGlyph =
  | (typeof KNOWN_VISUALS)[keyof typeof KNOWN_VISUALS]['glyph']
  | (typeof FALLBACK_VISUALS)[number]['glyph'];

export type McpConnectorVisual = {
  glyph: McpVisualGlyph;
  tone: McpVisualTone;
  iconAsset: SettingsGeneratedIconId;
};

export const KNOWN_MCP_CONNECTOR_VISUALS: Readonly<Record<string, McpConnectorVisual>> = KNOWN_VISUALS;

export function resolveMcpConnectorVisual(name: string, displayName = ''): McpConnectorVisual {
  const candidates = [name, displayName]
    .map(normalizeConnectorName)
    .filter((value, index, values) => value && values.indexOf(value) === index);
  for (const candidate of candidates) {
    const canonical = candidate === 'om-omtx' ? 'omtx-om' : candidate;
    const known = KNOWN_VISUALS[canonical as keyof typeof KNOWN_VISUALS];
    if (known) return known;
  }
  const searchable = candidates.join('-');
  for (const [key, visual] of Object.entries(KNOWN_VISUALS)) {
    if (searchable.includes(key)) return visual;
  }
  return FALLBACK_VISUALS[stableHash(searchable || 'mcp') % FALLBACK_VISUALS.length]!;
}

export function McpConnectorVisualMark({ visual }: { visual: McpConnectorVisual }) {
  return (
    <span
      className='mcp-connector-visual mcp-connector-visual--badge'
      data-mcp-glyph={visual.glyph}
      data-mcp-asset={visual.iconAsset}
      aria-hidden='true'
    >
      <SettingsGeneratedIcon id={visual.iconAsset} className='mcp-connector-visual__image' />
    </span>
  );
}

function normalizeConnectorName(value: string): string {
  const suffix =
    value
      .trim()
      .toLowerCase()
      .replace(/^bundled:/, '')
      .split('/')
      .pop() || '';
  return suffix.replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '');
}

function stableHash(value: string): number {
  let hash = 2166136261;
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index);
    hash = Math.imul(hash, 16777619);
  }
  return hash >>> 0;
}
