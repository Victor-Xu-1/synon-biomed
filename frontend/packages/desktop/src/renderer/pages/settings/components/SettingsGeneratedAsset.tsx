import React from 'react';

/**
 * Image-2 generated artwork used by the settings visual system.
 *
 * The files live in `frontend/public/branding/settings-generated-v3` so the
 * browser can load them without bundling or changing the protected product
 * logo.  Keeping the asset maps here gives every settings route one stable
 * source of truth and makes a future visual revision a versioned directory
 * change instead of scattered component edits.
 */

const ASSET_ROOT = './branding/settings-generated-v3';

/**
 * Bump this value whenever the reviewed image-2 asset set changes.  Static
 * public files can otherwise retain a cached development-time 404 after a new
 * crop is added, leaving the live settings page with a broken image even
 * though the file is present.  One shared revision keeps every module on the
 * same visual asset release.
 */
export const SETTINGS_GENERATED_ASSET_REVISION = '20260831a';

const generatedAssetSrc = (path: string) => `${ASSET_ROOT}/${path}?v=${SETTINGS_GENERATED_ASSET_REVISION}`;

export const SETTINGS_GENERATED_ICON_SRC = {
  account: generatedAssetSrc('icons/account.png'),
  plans: generatedAssetSrc('icons/plans.png'),
  experts: generatedAssetSrc('icons/experts.png'),
  skills: generatedAssetSrc('icons/skills.png'),
  tools: generatedAssetSrc('icons/tools.png'),
  models: generatedAssetSrc('icons/models.png'),
  compute: generatedAssetSrc('icons/compute.png'),
  governance: generatedAssetSrc('icons/governance.png'),
  network: generatedAssetSrc('icons/network.png'),
  credentials: generatedAssetSrc('icons/credentials.png'),
  storage: generatedAssetSrc('icons/storage.png'),
  general: generatedAssetSrc('icons/general.png'),
  'connector-database': generatedAssetSrc('icons/connector-database.png'),
  'connector-book': generatedAssetSrc('icons/connector-book.png'),
  'connector-bars': generatedAssetSrc('icons/connector-bars.png'),
  'connector-dna': generatedAssetSrc('icons/connector-dna.png'),
  'connector-protein': generatedAssetSrc('icons/connector-protein.png'),
  'connector-molecule': generatedAssetSrc('icons/connector-molecule.png'),
  'connector-rna': generatedAssetSrc('icons/connector-rna.png'),
  'connector-clipboard': generatedAssetSrc('icons/connector-clipboard.png'),
  'connector-cube': generatedAssetSrc('icons/connector-cube.png'),
  'connector-regulation': generatedAssetSrc('icons/connector-regulation.png'),
} as const;

export const SETTINGS_GENERATED_ARTWORK_SRC = {
  'account-network': generatedAssetSrc('artwork/account-network.png'),
  database: generatedAssetSrc('artwork/database.png'),
  book: generatedAssetSrc('artwork/book.png'),
  clinical: generatedAssetSrc('artwork/clinical.png'),
  dna: generatedAssetSrc('artwork/dna.png'),
  chemistry: generatedAssetSrc('artwork/chemistry.png'),
  regulation: generatedAssetSrc('artwork/regulation.png'),
  expression: generatedAssetSrc('artwork/expression.png'),
  protein: generatedAssetSrc('artwork/protein.png'),
  rna: generatedAssetSrc('artwork/rna.png'),
  structures: generatedAssetSrc('artwork/structures.png'),
  'network-globe': generatedAssetSrc('artwork/network-globe.png'),
} as const;

export const SETTINGS_GENERATED_EMPTY_SRC = {
  account: generatedAssetSrc('empty/account.png'),
  plans: generatedAssetSrc('empty/plans.png'),
  experts: generatedAssetSrc('empty/experts.png'),
  compute: generatedAssetSrc('empty/compute.png'),
  governance: generatedAssetSrc('empty/governance.png'),
  storage: generatedAssetSrc('empty/storage.png'),
} as const;

export const SETTINGS_GENERATED_NAV_SRC = {
  experts: generatedAssetSrc('icons/nav-experts.png'),
  skills: generatedAssetSrc('icons/nav-skills.png'),
  tools: generatedAssetSrc('icons/nav-tools.png'),
  models: generatedAssetSrc('icons/nav-models.png'),
  compute: generatedAssetSrc('icons/nav-compute.png'),
  governance: generatedAssetSrc('icons/nav-governance.png'),
  network: generatedAssetSrc('icons/nav-network.png'),
  credentials: generatedAssetSrc('icons/nav-credentials.png'),
  storage: generatedAssetSrc('icons/nav-storage.png'),
  general: generatedAssetSrc('icons/nav-general.png'),
} as const;

export type SettingsGeneratedIconId = keyof typeof SETTINGS_GENERATED_ICON_SRC;
export type SettingsGeneratedArtworkId = keyof typeof SETTINGS_GENERATED_ARTWORK_SRC;
export type SettingsGeneratedEmptyId = keyof typeof SETTINGS_GENERATED_EMPTY_SRC;
export type SettingsGeneratedNavId = keyof typeof SETTINGS_GENERATED_NAV_SRC;

type GeneratedImageProps = {
  className?: string;
  alt?: string;
  title?: string;
};

function generatedImageAttributes(alt: string | undefined) {
  return alt
    ? { alt }
    : {
        alt: '',
        'aria-hidden': true as const,
      };
}

export const SettingsGeneratedIcon: React.FC<GeneratedImageProps & { id: SettingsGeneratedIconId }> = ({
  id,
  className,
  alt,
  title,
}) => (
  <img
    className={className}
    src={SETTINGS_GENERATED_ICON_SRC[id]}
    {...generatedImageAttributes(alt)}
    title={title}
    draggable={false}
    decoding='async'
    data-settings-generated-asset={`icon:${id}`}
  />
);

export const SettingsGeneratedArtwork: React.FC<GeneratedImageProps & { id: SettingsGeneratedArtworkId }> = ({
  id,
  className,
  alt,
  title,
}) => (
  <img
    className={className}
    src={SETTINGS_GENERATED_ARTWORK_SRC[id]}
    {...generatedImageAttributes(alt)}
    title={title}
    draggable={false}
    decoding='async'
    data-settings-generated-asset={`artwork:${id}`}
  />
);

export const SettingsGeneratedEmptyArtwork: React.FC<GeneratedImageProps & { id: SettingsGeneratedEmptyId }> = ({
  id,
  className,
  alt,
  title,
}) => (
  <img
    className={className}
    src={SETTINGS_GENERATED_EMPTY_SRC[id]}
    {...generatedImageAttributes(alt)}
    title={title}
    draggable={false}
    decoding='async'
    data-settings-generated-asset={`empty:${id}`}
  />
);

export const SettingsGeneratedNavIcon: React.FC<GeneratedImageProps & { id: SettingsGeneratedNavId }> = ({
  id,
  className,
  alt,
  title,
}) => (
  <img
    className={className}
    src={SETTINGS_GENERATED_NAV_SRC[id]}
    {...generatedImageAttributes(alt)}
    title={title}
    draggable={false}
    decoding='async'
    data-settings-generated-asset={`nav:${id}`}
  />
);
