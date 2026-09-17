import React from 'react';
import {
  SettingsGeneratedArtwork,
  SettingsGeneratedIcon,
  type SettingsGeneratedArtworkId,
  type SettingsGeneratedIconId,
} from '../components/SettingsGeneratedAsset';

export type McpVisualTone = 'blue' | 'cyan' | 'teal' | 'green' | 'amber' | 'orange' | 'rose' | 'violet';

type VisualDefinition = {
  glyph: string;
  tone: McpVisualTone;
  iconAsset: SettingsGeneratedIconId;
  artworkAsset: SettingsGeneratedArtworkId;
};

/**
 * Stable connector identifiers are retained for telemetry and accessibility.
 * Rendered marks come exclusively from the reviewed image-2 asset set. A
 * semantic asset may be shared by several services; this avoids inventing
 * service logos while keeping the visual language consistent.
 */
const KNOWN_VISUALS = {
  biomart: { glyph: 'biomart', tone: 'teal', iconAsset: 'connector-database', artworkAsset: 'database' },
  pubmed: { glyph: 'pubmed', tone: 'blue', iconAsset: 'connector-book', artworkAsset: 'book' },
  'clinical-trials': {
    glyph: 'clinical-trials',
    tone: 'cyan',
    iconAsset: 'connector-clipboard',
    artworkAsset: 'clinical',
  },
  chembl: { glyph: 'chembl', tone: 'violet', iconAsset: 'connector-molecule', artworkAsset: 'chemistry' },
  biorxiv: { glyph: 'biorxiv', tone: 'rose', iconAsset: 'connector-book', artworkAsset: 'book' },
  variants: { glyph: 'variants', tone: 'violet', iconAsset: 'connector-dna', artworkAsset: 'dna' },
  'clinical-genomics': {
    glyph: 'clinical-genomics',
    tone: 'blue',
    iconAsset: 'connector-clipboard',
    artworkAsset: 'clinical',
  },
  expression: { glyph: 'expression', tone: 'green', iconAsset: 'connector-bars', artworkAsset: 'expression' },
  regulation: { glyph: 'regulation', tone: 'orange', iconAsset: 'connector-regulation', artworkAsset: 'regulation' },
  'protein-annotation': {
    glyph: 'protein-annotation',
    tone: 'teal',
    iconAsset: 'connector-protein',
    artworkAsset: 'protein',
  },
  rna: { glyph: 'rna', tone: 'rose', iconAsset: 'connector-rna', artworkAsset: 'rna' },
  'structures-interactions': {
    glyph: 'structures-interactions',
    tone: 'cyan',
    iconAsset: 'connector-cube',
    artworkAsset: 'structures',
  },
  'omics-archives': { glyph: 'omics-archives', tone: 'blue', iconAsset: 'storage', artworkAsset: 'database' },
  'genes-ontologies': {
    glyph: 'genes-ontologies',
    tone: 'green',
    iconAsset: 'connector-regulation',
    artworkAsset: 'regulation',
  },
  'drug-regulatory': {
    glyph: 'drug-regulatory',
    tone: 'orange',
    iconAsset: 'connector-regulation',
    artworkAsset: 'regulation',
  },
  'research-resources': {
    glyph: 'research-resources',
    tone: 'amber',
    iconAsset: 'connector-book',
    artworkAsset: 'book',
  },
  'cancer-models': {
    glyph: 'cancer-models',
    tone: 'rose',
    iconAsset: 'connector-protein',
    artworkAsset: 'protein',
  },
  chemistry: { glyph: 'chemistry', tone: 'violet', iconAsset: 'connector-molecule', artworkAsset: 'chemistry' },
  'human-genetics': { glyph: 'human-genetics', tone: 'blue', iconAsset: 'connector-dna', artworkAsset: 'dna' },
  literature: { glyph: 'literature', tone: 'amber', iconAsset: 'connector-book', artworkAsset: 'book' },
  genomes: { glyph: 'genomes', tone: 'cyan', iconAsset: 'connector-dna', artworkAsset: 'dna' },
  cellguide: { glyph: 'cellguide', tone: 'teal', iconAsset: 'connector-protein', artworkAsset: 'protein' },
  zinc: { glyph: 'zinc', tone: 'violet', iconAsset: 'connector-molecule', artworkAsset: 'chemistry' },
  'open-targets-official': {
    glyph: 'open-targets-official',
    tone: 'blue',
    iconAsset: 'connector-regulation',
    artworkAsset: 'regulation',
  },
  'tamarind-bio': {
    glyph: 'tamarind-bio',
    tone: 'green',
    iconAsset: 'connector-protein',
    artworkAsset: 'protein',
  },
  'adaptyv-cloud-lab': {
    glyph: 'adaptyv-cloud-lab',
    tone: 'cyan',
    iconAsset: 'connector-clipboard',
    artworkAsset: 'clinical',
  },
  'ketcher-chemistry': {
    glyph: 'ketcher-chemistry',
    tone: 'violet',
    iconAsset: 'connector-molecule',
    artworkAsset: 'chemistry',
  },
  'omtx-om': {
    glyph: 'omtx-om',
    tone: 'violet',
    iconAsset: 'connector-molecule',
    artworkAsset: 'chemistry',
  },
  'patsnap-chemical-molecular': {
    glyph: 'patsnap-chemical-molecular',
    tone: 'violet',
    iconAsset: 'connector-molecule',
    artworkAsset: 'chemistry',
  },
  'inductive-bio': {
    glyph: 'inductive-bio',
    tone: 'cyan',
    iconAsset: 'connector-bars',
    artworkAsset: 'chemistry',
  },
  'boltz-api-official': {
    glyph: 'boltz-api-official',
    tone: 'blue',
    iconAsset: 'connector-protein',
    artworkAsset: 'structures',
  },
  'renkin-local': {
    glyph: 'renkin-local',
    tone: 'orange',
    iconAsset: 'connector-regulation',
    artworkAsset: 'chemistry',
  },
  'rna-design-local': { glyph: 'rna-design-local', tone: 'rose', iconAsset: 'connector-rna', artworkAsset: 'rna' },
  'idc-rest': { glyph: 'idc-rest', tone: 'cyan', iconAsset: 'connector-database', artworkAsset: 'clinical' },
} as const satisfies Record<string, VisualDefinition>;

const FALLBACK_VISUALS = [
  { glyph: 'custom-orbit', tone: 'blue', iconAsset: 'connector-regulation', artworkAsset: 'regulation' },
  { glyph: 'custom-grid', tone: 'teal', iconAsset: 'connector-clipboard', artworkAsset: 'clinical' },
  { glyph: 'custom-wave', tone: 'violet', iconAsset: 'connector-rna', artworkAsset: 'rna' },
  { glyph: 'custom-cube', tone: 'cyan', iconAsset: 'connector-cube', artworkAsset: 'structures' },
  { glyph: 'custom-link', tone: 'green', iconAsset: 'connector-protein', artworkAsset: 'protein' },
  { glyph: 'custom-spark', tone: 'amber', iconAsset: 'connector-book', artworkAsset: 'book' },
] as const satisfies readonly VisualDefinition[];

export type McpVisualGlyph =
  | (typeof KNOWN_VISUALS)[keyof typeof KNOWN_VISUALS]['glyph']
  | (typeof FALLBACK_VISUALS)[number]['glyph'];

export type McpConnectorVisual = {
  glyph: McpVisualGlyph;
  tone: McpVisualTone;
  iconAsset: SettingsGeneratedIconId;
  artworkAsset: SettingsGeneratedArtworkId;
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

export function McpConnectorVisualMark({
  visual,
  variant = 'badge',
}: {
  visual: McpConnectorVisual;
  variant?: 'badge' | 'artwork';
}) {
  return (
    <span
      className={`mcp-connector-visual mcp-connector-visual--${variant}`}
      data-mcp-glyph={visual.glyph}
      data-mcp-asset={variant === 'artwork' ? visual.artworkAsset : visual.iconAsset}
      aria-hidden='true'
    >
      {variant === 'artwork' ? (
        <SettingsGeneratedArtwork id={visual.artworkAsset} className='mcp-connector-visual__image' />
      ) : (
        <SettingsGeneratedIcon id={visual.iconAsset} className='mcp-connector-visual__image' />
      )}
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
