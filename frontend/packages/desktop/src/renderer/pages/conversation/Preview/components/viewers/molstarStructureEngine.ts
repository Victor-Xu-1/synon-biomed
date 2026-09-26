/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { PluginUIContext } from 'molstar/lib/mol-plugin-ui/context';
import type { PluginUISpec } from 'molstar/lib/mol-plugin-ui/spec';
import { OrderedSet } from 'molstar/lib/mol-data/int';
import { Vec2, Vec3, Vec4 } from 'molstar/lib/mol-math/linear-algebra';
import { Bond, StructureElement, Unit } from 'molstar/lib/mol-model/structure';
import type { Structure } from 'molstar/lib/mol-model/structure';
import type { ElementIndex } from 'molstar/lib/mol-model/structure/model/indexing';
import { StructureQuery } from 'molstar/lib/mol-model/structure/query/query';
import { isProtein, type MoleculeType } from 'molstar/lib/mol-model/structure/model/types';
import { MolScriptBuilder as MS } from 'molstar/lib/mol-script/language/builder';
import {
  StructureSelectionQueries,
  StructureSelectionQuery,
} from 'molstar/lib/mol-plugin-state/helpers/structure-selection-query';
import {
  presetStaticComponent,
  PresetStructureRepresentations,
  StructureRepresentationPresetProvider,
} from 'molstar/lib/mol-plugin-state/builder/structure/representation-preset';
import { InteractionsProvider, type InteractionsParams } from 'molstar/lib/mol-model-props/computed/interactions';
import { InteractionsRepresentationProvider } from 'molstar/lib/mol-model-props/computed/representations/interactions';
import { InteractionTypeColorThemeProvider } from 'molstar/lib/mol-model-props/computed/themes/interaction-type';
import { StateObjectRef, StateSelection } from 'molstar/lib/mol-state';
import { Task } from 'molstar/lib/mol-task';
import { PluginBehaviors } from 'molstar/lib/mol-plugin/behavior';
import { setSubtreeVisibility } from 'molstar/lib/mol-plugin/behavior/static/state';
import { PluginCommands } from 'molstar/lib/mol-plugin/commands';
import { PluginConfig } from 'molstar/lib/mol-plugin/config';
import { UpdateTrajectory } from 'molstar/lib/mol-plugin-state/actions/structure';
import { PluginStateObject } from 'molstar/lib/mol-plugin-state/objects';
import { StateTransforms } from 'molstar/lib/mol-plugin-state/transforms';
import type { StructureRef } from 'molstar/lib/mol-plugin-state/manager/structure/hierarchy-state';
import { CubeProvider, DxProvider } from 'molstar/lib/mol-plugin-state/formats/volume';
import { MmcifFormat } from 'molstar/lib/mol-model-formats/structure/mmcif';
import { resolveSurfaceColorSettings, type MolstarElectrostaticVolume } from './molstarElectrostaticTheme';
import {
  isDisplayLigandResidueName,
  summarizeStructureComposition,
  type MolstarStructureComposition,
  type StructureObjectKind,
} from './structureComposition';
import { Color } from 'molstar/lib/mol-util/color/index';
import 'molstar/build/viewer/molstar.css';
import { formatMolstarPosePdb, type MolstarPoseAtom } from './molstarPose';
import {
  applyDockingLigandBondDefinitions,
  createDockingLigandShapeData,
  formatDockingLigandMolBlock,
  resolveDockingLigandShapeResidueNames,
  SynonBiomedDockingLigandAtoms,
  SynonBiomedDockingLigandBonds,
  type DockingLigandBondDefinition,
} from './molstarDockingLigandShape';
import {
  createMolstarInteractionStrengthLabelData,
  SynonBiomedInteractionStrengthLabels,
  type MolstarInteractionStrengthRecord,
} from './molstarInteractionStrengthLabels';
import {
  createMolstarInteractionFeatureAnchorData,
  SynonBiomedInteractionFeatureAnchors,
} from './molstarInteractionAnchors';
import {
  disposeMolstarUiResources,
  MOLSTAR_THUMBNAIL_PIXEL_SCALE,
  MOLSTAR_VIEWPORT_PIXEL_SCALE,
} from './molstarViewportPerformance';

export type MolstarTrajectoryFormat = 'mmcif' | 'pdb' | 'pdbqt' | 'pqr' | 'gro' | 'xyz' | 'mol' | 'sdf' | 'mol2';

export type MolstarStructureEngineMode = 'viewport' | 'thumbnail';

export type MolstarRepresentationPreset = 'auto' | 'polymer-and-ligand' | 'molecular-surface' | 'illustrative';

export type MolstarViewRepresentation =
  | 'initial'
  | 'ball-and-stick'
  | 'line'
  | 'surface'
  | 'pocket-surface'
  | 'ligand-surface'
  | 'electrostatic';

export type MolstarViewLayer = Exclude<MolstarViewRepresentation, 'initial' | 'electrostatic'>;

export type MolstarPocketLayerPlan = {
  layers: MolstarViewLayer[];
  hasBallAndStick: boolean;
  hasLine: boolean;
  hasProteinSurface: boolean;
  hasPocketSurface: boolean;
  hasLigandSurface: boolean;
  hasProteinOverlay: boolean;
};

export const resolvePocketLayerPlan = (requestedLayers: readonly MolstarViewLayer[]): MolstarPocketLayerPlan => {
  const layers = [...new Set(requestedLayers)];
  const hasBallAndStick = layers.includes('ball-and-stick');
  const hasLine = layers.includes('line');
  const hasProteinSurface = layers.includes('surface');
  const hasPocketSurface = layers.includes('pocket-surface');
  const hasLigandSurface = layers.includes('ligand-surface');
  return {
    layers,
    hasBallAndStick,
    hasLine,
    hasProteinSurface,
    hasPocketSurface,
    hasLigandSurface,
    hasProteinOverlay: hasBallAndStick || hasLine || hasProteinSurface,
  };
};

export type MolstarDockingProjectionScope = 'pocket' | 'structure';
export type MolstarDockingProjectionState = 'overview' | MolstarDockingProjectionScope;

/**
 * A protein surface needs the complete receptor. All other pocket overlays can
 * stay on the bounded ligand neighbourhood used by the focused pocket view.
 */
export const resolveDockingProjectionScope = (
  requestedLayers: readonly MolstarViewLayer[]
): MolstarDockingProjectionScope =>
  resolvePocketLayerPlan(requestedLayers).hasProteinSurface ? 'structure' : 'pocket';

/** The fixed receptor is an overview/pocket companion, never a second full-receptor view. */
export const isDockingFixedProteinHidden = (
  proteinVisible: boolean,
  projectionScope: MolstarDockingProjectionState
): boolean => !proteinVisible || projectionScope === 'structure';

export type MolstarPocketInteractionRenderVisibility = {
  nativeHidden: boolean;
  strengthHidden: boolean;
};

/** Keeps native interaction lines authoritative while report data only adds strength labels. */
export const resolvePocketInteractionRenderVisibility = (
  strengthLabelCount: number
): MolstarPocketInteractionRenderVisibility => {
  const hasStrengthLabels = strengthLabelCount > 0;
  return {
    nativeHidden: false,
    strengthHidden: !hasStrengthLabels,
  };
};

const resolveMmcifBondOrder = (value: string): number => {
  switch (value.trim().toLowerCase()) {
    case 'doub':
    case 'double':
      return 2;
    case 'trip':
    case 'triple':
      return 3;
    default:
      return 1;
  }
};

const readMmcifLigandBondDefinitions = (structure: Structure, residueName: string): DockingLigandBondDefinition[] => {
  const sourceData = structure.model.sourceData;
  if (!MmcifFormat.is(sourceData)) return [];
  const category = sourceData.data.frame.categories.chem_comp_bond;
  if (!category) return [];
  const component = category.getField('comp_id');
  const atomIdA = category.getField('atom_id_1');
  const atomIdB = category.getField('atom_id_2');
  const valueOrder = category.getField('value_order');
  const aromaticFlag = category.getField('pdbx_aromatic_flag');
  if (!component || !atomIdA || !atomIdB || !valueOrder) return [];
  const target = residueName.trim().toUpperCase();
  const definitions: DockingLigandBondDefinition[] = [];
  for (let row = 0; row < category.rowCount; row += 1) {
    if (component.str(row).trim().toUpperCase() !== target) continue;
    definitions.push({
      atomIdA: atomIdA.str(row),
      atomIdB: atomIdB.str(row),
      order: resolveMmcifBondOrder(valueOrder.str(row)),
      aromatic: aromaticFlag?.str(row).trim().toUpperCase() === 'Y',
    });
  }
  return definitions;
};

export const isElectrostaticSurfaceRepresentation = (representation: MolstarViewRepresentation): boolean =>
  representation === 'surface' ||
  representation === 'pocket-surface' ||
  representation === 'ligand-surface' ||
  representation === 'electrostatic';

export const isStructureLigandCarbonColorRepresentation = (representationType: string): boolean =>
  representationType === 'ball-and-stick' || representationType === 'line';

export type MolstarSurfaceOptions = {
  probeRadius: number;
  alpha: number;
};

export type MolstarPocketOptions = {
  expandRadius: number;
  showDistances: boolean;
  displayLayers?: readonly MolstarViewLayer[];
  ligandColor?: Color;
  ligandColors?: readonly Color[];
  focusCamera?: boolean;
  interactionVisibility?: MolstarPocketInteractionVisibility;
};

export const shouldFocusPocketCamera = (options: Pick<MolstarPocketOptions, 'focusCamera'>): boolean =>
  options.focusCamera !== false;

export function applyDockingVisibilitySwap(
  previousRefs: readonly string[],
  nextRefs: readonly string[],
  setHidden: (ref: string, hidden: boolean) => void
): void {
  // Both updates happen in the same JavaScript turn. The candidate graph is
  // revealed before the old graph is hidden, so Mol* never paints an empty
  // viewport between poses; because the candidate was built hidden, it also
  // cannot leak its default representation while being prepared.
  nextRefs.forEach((ref) => setHidden(ref, false));
  previousRefs.forEach((ref) => setHidden(ref, true));
}

export const shouldReuseDockingLigandVisual = (
  cachedColor: Color | undefined,
  nextColor: Color,
  graphAvailable: boolean
): boolean => graphAvailable && cachedColor === nextColor;

export type DockingRefinementState = 'pending' | 'ready' | 'failed';

export function queueDockingRefinement<T>(
  refine: () => Promise<T>,
  onStateChange: (state: DockingRefinementState, reason?: unknown) => void
): Promise<T> {
  onStateChange('pending');
  // Starting from a microtask is intentional: the click handler can commit and
  // paint the selected ligand before projection, surface and interaction work
  // gets any main-thread time.
  return Promise.resolve()
    .then(refine)
    .then((result) => {
      onStateChange('ready');
      return result;
    })
    .catch((reason: unknown) => {
      onStateChange('failed', reason);
      throw reason;
    });
}

export function createLatestDockingRefinementQueue(delayMs = 120): {
  queue: <T>(
    refine: () => Promise<T>,
    onStateChange: (state: DockingRefinementState, reason?: unknown) => void
  ) => Promise<T | undefined>;
  cancel: () => void;
} {
  let generation = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let settleScheduled: (() => void) | undefined;
  const cancel = () => {
    generation += 1;
    if (timer !== undefined) {
      clearTimeout(timer);
      settleScheduled?.();
    }
    timer = undefined;
    settleScheduled = undefined;
  };
  return {
    queue<T>(refine: () => Promise<T>, onStateChange: (state: DockingRefinementState, reason?: unknown) => void) {
      cancel();
      const scheduledGeneration = generation;
      onStateChange('pending');
      return new Promise<T | undefined>((resolve, reject) => {
        settleScheduled = () => resolve(undefined);
        timer = setTimeout(
          () => {
            timer = undefined;
            settleScheduled = undefined;
            void queueDockingRefinement(refine, (state, reason) => {
              if (scheduledGeneration !== generation || state === 'pending') return;
              onStateChange(state, reason);
            }).then(
              (result) => resolve(scheduledGeneration === generation ? result : undefined),
              (reason: unknown) => {
                if (scheduledGeneration === generation) reject(reason);
                else resolve(undefined);
              }
            );
          },
          Math.max(0, delayMs)
        );
      });
    },
    cancel,
  };
}

export type MolstarLigandComparisonOptions = MolstarPocketOptions & {
  primaryColor: Color;
  secondaryColor: Color;
};

export type MolstarPocketSummary = {
  hasLigand: boolean;
  ligandCount: number;
  residueCount: number;
  distanceCount: number;
};

export type MolstarSelectionSummary = {
  atomCount: number;
  residueCount: number;
  chainCount: number;
};

export type MolstarTrajectoryModelState = {
  index: number;
  count: number;
};

export type MolstarLigandDepictionSource = {
  residueName: string;
  interactionResidueName: string;
  molBlock: string;
  complexPdb?: string;
  atomCount: number;
  hasProtein: boolean;
};

export type MolstarScreenPoint = {
  x: number;
  y: number;
};

export type MolstarScreenRectangle = {
  left: number;
  top: number;
  width: number;
  height: number;
};

export const STANDARD_PROTEIN_SURFACE_TYPE_PARAMS = {
  // Keep every molecular surface on one opacity contract so stacked protein,
  // pocket, and ligand layers remain predictable.
  probeRadius: 1.4,
  alpha: 0.7,
  quality: 'medium',
} as const;

export const ELECTROSTATIC_SURFACE_TYPE_PARAMS = {
  ...STANDARD_PROTEIN_SURFACE_TYPE_PARAMS,
  alpha: 0.7,
} as const;

export const POCKET_SURFACE_TYPE_PARAMS = {
  ...STANDARD_PROTEIN_SURFACE_TYPE_PARAMS,
  alpha: 0.7,
} as const;

type MolstarSurfaceScope = 'protein' | 'pocket' | 'ligand';

export const shouldIncludeProteinContextForSurface = (scope: MolstarSurfaceScope): boolean => scope !== 'protein';

export const resolveElectrostaticSurfaceVolumeRole = (
  scope: MolstarSurfaceScope,
  composition: Pick<MolstarStructureComposition, 'hasProtein' | 'hasLigand'>
): MolstarElectrostaticMapRole =>
  scope === 'ligand' || (!composition.hasProtein && composition.hasLigand) ? 'ligand' : 'protein';

export type MolstarSurfaceComponentType = 'protein' | 'ligand' | 'all';

export type MolstarElectrostaticInputSource = {
  content: string;
  ligands: readonly MolstarElectrostaticLigandInputSource[];
  atomCount: number;
};

export type MolstarElectrostaticLigandInputSource = {
  key: string;
  molBlock: string;
  atomCount: number;
};

export type MolstarElectrostaticMapRole = 'protein' | 'ligand';

export type MolstarElectrostaticPotentialSource = {
  source: string;
  label: string;
};

export type MolstarElectrostaticPotentialSources = {
  protein?: MolstarElectrostaticPotentialSource;
  ligands?: Readonly<Record<string, MolstarElectrostaticPotentialSource>>;
};

type MolstarElectrostaticVolumeHandle = MolstarElectrostaticVolume & {
  sourceRef: string;
};

export type MolstarElectrostaticVolumes = {
  protein?: MolstarElectrostaticVolume;
  ligand?: MolstarElectrostaticVolume;
  ligands?: Readonly<Record<string, MolstarElectrostaticVolume>>;
};

type MolstarElectrostaticVolumeHandles = {
  protein?: MolstarElectrostaticVolumeHandle;
  ligand?: MolstarElectrostaticVolumeHandle;
  ligands?: Record<string, MolstarElectrostaticVolumeHandle>;
};

const normalizeElectrostaticLigandKey = (value: string): string => value.trim().toUpperCase();

export const resolveDockingLigandElectrostaticSurfaces = (
  residueNames: readonly string[],
  volumes: MolstarElectrostaticVolumes
): Array<{ residueName: string; volume: MolstarElectrostaticVolume }> => {
  const seen = new Set<string>();
  return residueNames.flatMap((residueName) => {
    const normalized = normalizeElectrostaticLigandKey(residueName);
    if (!normalized || seen.has(normalized)) return [];
    seen.add(normalized);
    const volume = volumes.ligands?.[normalized];
    if (!volume) throw new Error(`MOLSTAR_ELECTROSTATIC_LIGAND_VOLUME_MISSING:${normalized}`);
    return [{ residueName: normalized, volume }];
  });
};

export const resolveSurfaceComponentType = (hasProtein: boolean, hasLigand: boolean): MolstarSurfaceComponentType => {
  if (hasProtein) return 'protein';
  if (hasLigand) return 'ligand';
  return 'all';
};

export {
  resolveElectrostaticColorTheme,
  resolveElectrostaticColorParams,
  ELECTROSTATIC_NEUTRAL_COLOR,
} from './molstarElectrostaticTheme';

export const DEFAULT_POCKET_EXPAND_RADIUS = 4.5;
export const LIGAND_DOUBLE_CLICK_WINDOW_MS = 320;

export type MolstarStructureEngine = {
  readonly plugin: PluginUIContext;
  readonly load: (
    source: string | ArrayBuffer,
    filename: string,
    format: string
  ) => Promise<MolstarStructureComposition>;
  readonly add: (
    source: string | ArrayBuffer,
    filename: string,
    format: string
  ) => Promise<MolstarStructureComposition>;
  readonly loadDockingEnsemble: (
    source: string,
    filename: string,
    format: string,
    initialLigandResidueName: string,
    options: MolstarPocketOptions
  ) => Promise<MolstarPocketSummary>;
  readonly resize: () => void;
  readonly resetCamera: () => void;
  readonly clickNativeControl: (controlId: string) => void;
  readonly isNativeControlActive: (controlId: string) => boolean;
  readonly applyRepresentationPreset: (
    preset: MolstarRepresentationPreset,
    options?: MolstarSurfaceOptions
  ) => Promise<void>;
  readonly applyRepresentationStyle: (representation: MolstarViewRepresentation) => Promise<void>;
  readonly applyPocketFocus: (options: MolstarPocketOptions) => Promise<MolstarPocketSummary>;
  readonly setPocketInteractionStrengths: (
    records: readonly MolstarInteractionStrengthRecord[],
    visibility?: MolstarPocketInteractionVisibility
  ) => Promise<void>;
  readonly applyDockingPocket: (
    ligandResidueName: string,
    options: MolstarPocketOptions
  ) => Promise<MolstarPocketSummary>;
  readonly clearDockingPocket: (ligandResidueNames: readonly string[]) => Promise<MolstarPocketSummary>;
  readonly replaceDockingComparison: (
    primaryResidueName: string,
    secondaryResidueName: string,
    options: MolstarLigandComparisonOptions
  ) => Promise<MolstarPocketSummary>;
  readonly replaceDockingSelection: (
    ligandResidueNames: readonly string[],
    options: MolstarPocketOptions
  ) => Promise<MolstarPocketSummary>;
  readonly clearDockingSelection: () => Promise<MolstarPocketSummary>;
  readonly cancelDockingRefinement: () => void;
  readonly setDockingProteinVisible: (visible: boolean) => void;
  readonly replaceDockingLayers: (
    ligandResidueNames: readonly string[],
    layers: readonly MolstarViewLayer[],
    ligandColors?: readonly Color[]
  ) => Promise<MolstarPocketSummary>;
  readonly clearPocketFocus: () => Promise<void>;
  readonly setSelectionMode: (enabled: boolean) => void;
  readonly selectScreenPoint: (point: MolstarScreenPoint, additive?: boolean) => MolstarSelectionSummary;
  readonly selectScreenRectangle: (rectangle: MolstarScreenRectangle, additive?: boolean) => MolstarSelectionSummary;
  readonly clearSelection: () => MolstarSelectionSummary;
  readonly getSelectionSummary: () => MolstarSelectionSummary;
  readonly getSelectedLigandResidueName: () => string | undefined;
  readonly getTrajectoryModelState: () => MolstarTrajectoryModelState;
  readonly getStructureComposition: () => MolstarStructureComposition;
  readonly getPrimaryLigandDepictionSource: () => MolstarLigandDepictionSource | undefined;
  readonly getElectrostaticInputSource: (
    ligandResidueNames?: readonly string[]
  ) => MolstarElectrostaticInputSource | undefined;
  readonly setElectrostaticPotentials: (
    sources: MolstarElectrostaticPotentialSources,
    range: readonly [number, number]
  ) => Promise<void>;
  readonly setStructureObjectVisible: (kind: StructureObjectKind, visible: boolean) => void;
  readonly setStructureLigandColor: (color: Color) => Promise<void>;
  readonly advanceTrajectoryModel: (by: number) => Promise<MolstarTrajectoryModelState>;
  readonly exportCurrentPose: (options?: { selectionOnly?: boolean }) => {
    content: string;
    atomCount: number;
    selectionOnly: boolean;
  };
  readonly setBackgroundColor: (color: number) => void;
  readonly captureImage: (options?: { width?: number; height?: number; backgroundColor?: number }) => Promise<string>;
  readonly dispose: () => void;
};

type MolstarStructureEngineOptions = {
  mode?: MolstarStructureEngineMode;
  onSelectionChange?: (summary: MolstarSelectionSummary) => void;
};

type CreateDefaultPluginUISpec = () => PluginUISpec;

type PocketDistancePair = {
  ligandUnit: Unit.Atomic;
  ligandIndex: number;
  partnerUnit: Unit.Atomic;
  partnerIndex: number;
  distance: number;
  residueKey: string;
};

const POCKET_DISTANCE_TAG = 'synon-biomed-pocket-distance';
export const POCKET_DISTANCE_MARKER_LIMIT = 8;
export const STANDARD_LIGAND_STICK_TYPE_PARAMS = {
  // Match Mol*'s native ball-and-stick scale so bonds remain visually
  // continuous without atom spheres dominating the ligand silhouette.
  sizeFactor: 0.15,
  sizeAspectRatio: 2 / 3,
  ignoreHydrogens: true,
  ignoreHydrogensVariant: 'all',
} as const;

export const POCKET_LIGAND_STICK_TYPE_PARAMS = STANDARD_LIGAND_STICK_TYPE_PARAMS;

export const INITIAL_REPRESENTATION_PRESET_PARAMS: StructureRepresentationPresetProvider.Params<
  typeof PresetStructureRepresentations.auto
> = {
  // Keep polar hydrogens available for chemically meaningful hydrogen-bond
  // inspection while removing the dense non-polar hydrogen shell by default.
  ignoreHydrogens: true,
  ignoreHydrogensVariant: 'non-polar',
  ignoreLight: undefined,
  quality: undefined,
  theme: undefined,
};

const SYNON_VIEW_PRESET_PARAMS = {
  ...StructureRepresentationPresetProvider.CommonParams,
};

/**
 * Builds the replacement graph before Mol* retires the previous graph.
 * ComponentManager.applyPreset synchronizes obsolete components only after
 * this provider commits its new representations, so the canvas never enters
 * the empty intermediate state produced by applying the `empty` preset first.
 */
export const createSynonViewRepresentationPreset = (
  representation: Exclude<MolstarViewRepresentation, 'initial'>,
  electrostaticVolumes: MolstarElectrostaticVolumes = {}
) =>
  StructureRepresentationPresetProvider({
    id: `preset-synon-biomed-${representation}`,
    display: { name: `Synon Biomed ${representation}` },
    params: () => SYNON_VIEW_PRESET_PARAMS,
    async apply(ref, params, plugin) {
      const structureCell = StateObjectRef.resolveAndCheck(plugin.state.data, ref);
      const structureData = structureCell?.obj?.data;
      if (!structureCell || !structureData) return {};
      const ligandObject = summarizeStructureComposition(structureData).objects.find(
        (object) => object.kind === 'ligand'
      );
      const allLigandLoci = StructureQuery.loci(StructureSelectionQueries.ligand.query, structureData);
      const displayLigandLoci = ligandObject?.residueNames.length
        ? filterStructureLociByResidueNames(allLigandLoci, ligandObject.residueNames)
        : allLigandLoci;
      const displayLigandExpression = StructureElement.Bundle.toExpression(
        StructureElement.Bundle.fromLoci(displayLigandLoci)
      );
      const createDisplayLigandComponent = (label: string, key: string) => {
        if (StructureElement.Loci.isEmpty(displayLigandLoci)) return undefined;
        return plugin.builders.structure.tryCreateComponentFromSelection(
          structureCell,
          StructureSelectionQuery(label, displayLigandExpression),
          key,
          { label }
        );
      };

      if (
        representation === 'surface' ||
        representation === 'pocket-surface' ||
        representation === 'ligand-surface' ||
        representation === 'electrostatic'
      ) {
        // All surface scopes share one physical color scale, but protein and
        // pocket surfaces use the receptor-only field while ligand surfaces
        // use the ligand-only field. Opposing interface patches therefore
        // remain comparable without sampling one combined complex potential.
        const electrostatic = isElectrostaticSurfaceRepresentation(representation);
        const surfaceScope: MolstarSurfaceScope =
          representation === 'pocket-surface' ? 'pocket' : representation === 'ligand-surface' ? 'ligand' : 'protein';

        const protein =
          surfaceScope === 'protein'
            ? await presetStaticComponent(plugin, structureCell, 'protein', {
                label: electrostatic ? 'Protein electrostatic surface' : 'Protein surface',
              })
            : undefined;
        const standaloneLigand =
          surfaceScope === 'ligand'
            ? await createDisplayLigandComponent(
                electrostatic ? 'Ligand electrostatic surface' : 'Ligand surface',
                'synon-biomed-view-ligand-surface-component'
              )
            : undefined;
        const pocket =
          surfaceScope === 'pocket'
            ? await plugin.builders.structure.tryCreateComponentFromSelection(
                structureCell,
                StructureSelectionQuery(
                  'Pocket surface',
                  MS.struct.modifier.intersectBy({
                    0: MS.struct.modifier.includeSurroundings({
                      0: displayLigandExpression,
                      radius: DEFAULT_POCKET_EXPAND_RADIUS,
                      'as-whole-residues': true,
                    }),
                    by: StructureSelectionQueries.protein.expression,
                  })
                ),
                'synon-biomed-pocket-surface-component',
                { label: 'Pocket surface' }
              )
            : undefined;
        const surfaceComponentType = resolveSurfaceComponentType(Boolean(protein), Boolean(standaloneLigand));
        const all =
          surfaceScope !== 'pocket' && surfaceComponentType === 'all'
            ? await presetStaticComponent(plugin, structureCell, 'all', {
                label: electrostatic ? 'Electrostatic molecular surface' : 'Molecular surface',
              })
            : undefined;
        const fallbackProtein =
          !pocket && surfaceScope === 'pocket'
            ? await presetStaticComponent(plugin, structureCell, 'protein', {
                label: 'Protein surface',
              })
            : undefined;
        const surfaceComponent = pocket ?? protein ?? standaloneLigand ?? fallbackProtein ?? all;
        // A charged ligand in mmCIF must not select the protein's color theme.
        const surfaceStructure = surfaceComponent?.obj?.data ?? structureData;
        const surfaceComposition = summarizeStructureComposition(surfaceStructure);
        const surfaceVolumeRole = resolveElectrostaticSurfaceVolumeRole(surfaceScope, surfaceComposition);
        const surfaceColors = resolveSurfaceColorSettings(surfaceStructure, electrostaticVolumes[surfaceVolumeRole]);
        const ligand =
          surfaceScope !== 'ligand'
            ? await createDisplayLigandComponent('Ligand sticks', 'synon-biomed-view-ligand-sticks-component')
            : undefined;
        const proteinContext = shouldIncludeProteinContextForSurface(surfaceScope)
          ? await presetStaticComponent(plugin, structureCell, 'protein', {
              label: 'Protein context',
            })
          : undefined;
        const components = {
          protein,
          standaloneLigand,
          pocket,
          fallbackProtein,
          all,
          ligand,
          proteinContext,
        };
        // Component creation commits its own state update. Build the
        // representation transaction afterwards so it sees those new refs;
        // creating this builder earlier can retain a stale state snapshot and
        // fail with "Could not find node" during a rapid docking style swap.
        const { update, builder } = StructureRepresentationPresetProvider.reprBuilder(plugin, params, structureData);
        const representations = {
          surface: builder.buildRepresentation(
            update,
            surfaceComponent,
            {
              type: 'molecular-surface',
              typeParams:
                surfaceScope === 'pocket'
                  ? POCKET_SURFACE_TYPE_PARAMS
                  : electrostatic
                    ? ELECTROSTATIC_SURFACE_TYPE_PARAMS
                    : STANDARD_PROTEIN_SURFACE_TYPE_PARAMS,
              ...surfaceColors,
            },
            { tag: 'synon-biomed-view-style-surface' }
          ),
          ligand: builder.buildRepresentation(
            update,
            ligand,
            {
              type: 'ball-and-stick',
              typeParams: STANDARD_LIGAND_STICK_TYPE_PARAMS,
              color: 'element-symbol',
            },
            { tag: 'synon-biomed-view-style-ligand' }
          ),
          proteinContext: builder.buildRepresentation(
            update,
            proteinContext,
            {
              type: 'cartoon',
              typeParams: { alpha: 0.24, sizeFactor: 0.16 },
              color: 'uniform',
              colorParams: { value: POCKET_COLORS.outsideProtein },
            },
            { tag: 'synon-biomed-view-style-protein-context' }
          ),
        };
        await update.commit({ revertOnError: true });
        return { components, representations };
      }

      const protein = await presetStaticComponent(plugin, structureCell, 'protein', { label: 'Protein' });
      const ligand = await createDisplayLigandComponent('Ligand', 'synon-biomed-view-ligand-component');
      const all =
        !protein && !ligand
          ? await presetStaticComponent(plugin, structureCell, 'all', {
              label: representation === 'line' ? 'All atoms as lines' : 'All atoms as ball-and-stick',
            })
          : undefined;
      const components = { protein, ligand, all };
      const { update, builder } = StructureRepresentationPresetProvider.reprBuilder(plugin, params, structureData);
      const build = (component: typeof all, tag: string) =>
        builder.buildRepresentation(
          update,
          component,
          representation === 'line'
            ? {
                type: 'line',
                typeParams: {
                  sizeFactor: 0.7,
                  lineSizeAttenuation: false,
                  ignoreHydrogens: true,
                  ignoreHydrogensVariant: 'all',
                },
                color: 'element-symbol',
              }
            : {
                type: 'ball-and-stick',
                typeParams: {
                  sizeFactor: 0.38,
                  sizeAspectRatio: 0.72,
                  ignoreHydrogens: true,
                  ignoreHydrogensVariant: 'all',
                },
                color: 'element-symbol',
              },
          { tag }
        );
      const representations = {
        protein: build(protein, 'synon-biomed-view-style-protein'),
        ligand: build(ligand, 'synon-biomed-view-style-ligand'),
        all: build(all, 'synon-biomed-view-style-all'),
      };
      await update.commit({ revertOnError: true });
      return { components, representations };
    },
  });
const POCKET_COLORS = {
  outsideProtein: Color(0x94a3b8),
  pocketCarbon: Color(0x64748b),
  ligandCarbon: Color(0x0f766e),
  label: Color(0x334155),
  labelBackground: Color(0xf8fafc),
  distance: Color(0x1d4ed8),
};
const POCKET_NATIVE_INTERACTION_TAGS = [
  'synon-biomed-pocket-interactions',
  'synon-biomed-pocket-water-bridges',
] as const;

/** Every docking visual owned by the receptor-side visibility control. */
export const DOCKING_PROTEIN_VISIBILITY_TAGS = [
  'synon-biomed-docking-fixed-protein',
  'synon-biomed-pocket-protein',
  'synon-biomed-pocket-residues',
  'synon-biomed-pocket-labels',
  'synon-biomed-pocket-layer-ball-and-stick',
  'synon-biomed-pocket-layer-line',
  'synon-biomed-pocket-layer-protein-surface',
  'synon-biomed-pocket-layer-pocket-surface',
  'synon-biomed-layer-initial-protein',
  'synon-biomed-layer-ball-and-stick',
  'synon-biomed-layer-line',
  'synon-biomed-layer-protein-surface',
  'synon-biomed-layer-pocket-surface',
  'synon-biomed-layer-protein-context',
  'synon-biomed-pocket-interaction-feature-anchors',
  'synon-biomed-pocket-interaction-strengths',
  ...POCKET_NATIVE_INTERACTION_TAGS,
] as const;

export const POCKET_INTERACTION_PROVIDER_NAMES = [
  'ionic',
  'pi-stacking',
  'cation-pi',
  'halogen-bonds',
  'hydrogen-bonds',
  'weak-hydrogen-bonds',
  'hydrophobic',
  'metal-coordination',
] as const;

export const POCKET_INTERACTION_BRIDGE_NAMES = ['water-bridges'] as const;

export type MolstarPocketInteractionName =
  | (typeof POCKET_INTERACTION_PROVIDER_NAMES)[number]
  | (typeof POCKET_INTERACTION_BRIDGE_NAMES)[number];
export type MolstarPocketInteractionVisibility = Partial<Record<MolstarPocketInteractionName, boolean>>;

export const POCKET_INTERACTION_VISUAL_GROUPS = {
  ligand: ['intra-unit', 'inter-unit'],
  pocketBridges: ['bridge'],
} as const;

const POCKET_INTERACTION_LINE_TYPE_PARAMS = {
  // The pocket sticks intentionally hide hydrogens. Keep the interaction
  // renderer on the same visible-atom contract so hydrogen-bond endpoints do
  // not attach to an atom the user cannot see.
  ignoreHydrogens: true,
  ignoreHydrogensVariant: 'all',
  sizeFactor: 0.14,
  dashCount: 6,
  dashScale: 0.72,
  linkScale: 0.82,
  linkSpacing: 0.45,
} as const;

export const POCKET_INTERACTION_REPRESENTATION_TYPE_PARAMS = {
  ...POCKET_INTERACTION_LINE_TYPE_PARAMS,
  includeParent: true,
  parentDisplay: 'between',
  aromaticScale: 0.9,
  aromaticSpacing: 0.4,
  aromaticDashCount: 4,
} as const;

type MappedInteractionParam = {
  readonly map: (name: string) => { readonly defaultValue: unknown };
};

type InteractionParam = { name: 'on' | 'off'; params: unknown };

export function createPocketInteractionProps(
  definitions: Pick<InteractionsParams, 'providers' | 'bridges'>,
  visibility: MolstarPocketInteractionVisibility = {}
): {
  providers: Record<(typeof POCKET_INTERACTION_PROVIDER_NAMES)[number], InteractionParam>;
  bridges: Record<(typeof POCKET_INTERACTION_BRIDGE_NAMES)[number], InteractionParam>;
} {
  const providerDefinitions = definitions.providers.params as unknown as Record<
    (typeof POCKET_INTERACTION_PROVIDER_NAMES)[number],
    MappedInteractionParam
  >;
  const bridgeDefinitions = definitions.bridges.params as unknown as Record<
    (typeof POCKET_INTERACTION_BRIDGE_NAMES)[number],
    MappedInteractionParam
  >;
  const state = (name: MolstarPocketInteractionName): InteractionParam => {
    const enabled = visibility[name] !== false;
    const definition =
      name === 'water-bridges'
        ? bridgeDefinitions[name]
        : providerDefinitions[name as (typeof POCKET_INTERACTION_PROVIDER_NAMES)[number]];
    return {
      name: enabled ? 'on' : 'off',
      params: definition.map(enabled ? 'on' : 'off').defaultValue,
    };
  };
  return {
    providers: Object.fromEntries(POCKET_INTERACTION_PROVIDER_NAMES.map((name) => [name, state(name)])) as Record<
      (typeof POCKET_INTERACTION_PROVIDER_NAMES)[number],
      InteractionParam
    >,
    bridges: Object.fromEntries(POCKET_INTERACTION_BRIDGE_NAMES.map((name) => [name, state(name)])) as Record<
      (typeof POCKET_INTERACTION_BRIDGE_NAMES)[number],
      InteractionParam
    >,
  };
}

export function createCompletePocketInteractionProps(definitions: Pick<InteractionsParams, 'providers' | 'bridges'>): {
  providers: Record<(typeof POCKET_INTERACTION_PROVIDER_NAMES)[number], InteractionParam>;
  bridges: Record<(typeof POCKET_INTERACTION_BRIDGE_NAMES)[number], InteractionParam>;
} {
  return createPocketInteractionProps(definitions);
}

type AtomicResidueTypeLookup = {
  readonly residueIndex: ArrayLike<number>;
  readonly model: {
    readonly atomicHierarchy: {
      readonly derived: {
        readonly residue: { readonly moleculeType: ArrayLike<MoleculeType> };
      };
    };
  };
};

export function isProteinAtom(unit: AtomicResidueTypeLookup, index: number): boolean {
  const residueIndex = unit.residueIndex[index];
  return isProtein(unit.model.atomicHierarchy.derived.residue.moleculeType[residueIndex]);
}

export function isLigandLociClick(clickedLoci: StructureElement.Loci, ligandLoci: StructureElement.Loci): boolean {
  return (
    clickedLoci.structure === ligandLoci.structure &&
    !StructureElement.Loci.isEmpty(clickedLoci) &&
    StructureElement.Loci.areIntersecting(clickedLoci, ligandLoci)
  );
}

export function normalizeStructureElementClickLoci(clickedLoci: unknown): StructureElement.Loci | undefined {
  if (StructureElement.Loci.is(clickedLoci)) return clickedLoci;
  // A default Mol* stick representation can report a single picked bond,
  // while a thicker representation may report both sides of the bond.
  // Both are valid ligand hits and must enter the same double-click path.
  if (Bond.isLoci(clickedLoci) && clickedLoci.bonds.length > 0) {
    return Bond.toFirstStructureElementLoci(clickedLoci);
  }
  return undefined;
}

export function resolvePocketLigandLoci(
  structure: Structure,
  targetLigandLoci: StructureElement.Loci | undefined,
  queryLigandLoci: (structure: Structure) => StructureElement.Loci
): StructureElement.Loci {
  if (targetLigandLoci?.structure === structure && !StructureElement.Loci.isEmpty(targetLigandLoci)) {
    return targetLigandLoci;
  }
  return queryLigandLoci(structure);
}

export function filterStructureLociByResidueName(
  loci: StructureElement.Loci,
  residueName: string
): StructureElement.Loci {
  return filterStructureLociByResidueNames(loci, [residueName]);
}

export function filterStructureLociByResidueNames(
  loci: StructureElement.Loci,
  residueNames: readonly string[]
): StructureElement.Loci {
  const expectedNames = new Set(residueNames.map((name) => name.trim().toUpperCase()).filter(Boolean));
  if (expectedNames.size === 0) {
    return StructureElement.Loci(loci.structure, []);
  }

  const elements = loci.elements.flatMap((element) => {
    const unit = element.unit;
    if (!Unit.isAtomic(unit)) return [];
    const hierarchy = unit.model.atomicHierarchy;
    const selected: StructureElement.UnitIndex[] = [];
    OrderedSet.forEach(element.indices, (index) => {
      const atomIndex = unit.elements[index];
      const authName = hierarchy.atoms.auth_comp_id.value(atomIndex).trim().toUpperCase();
      const labelName = hierarchy.atoms.label_comp_id.value(atomIndex).trim().toUpperCase();
      if (expectedNames.has(authName) || expectedNames.has(labelName)) {
        selected.push(index as StructureElement.UnitIndex);
      }
    });
    return selected.length > 0 ? [{ unit, indices: OrderedSet.ofSortedArray(selected) }] : [];
  });
  return StructureElement.Loci(loci.structure, elements);
}

export function resolveStructureLociResidueNames(loci: StructureElement.Loci): string[] {
  const residueNames = new Set<string>();
  for (const element of loci.elements) {
    if (!Unit.isAtomic(element.unit)) continue;
    const hierarchy = element.unit.model.atomicHierarchy;
    OrderedSet.forEach(element.indices, (index) => {
      const atomIndex = element.unit.elements[index];
      const residueName = normalizeElectrostaticLigandKey(
        hierarchy.atoms.auth_comp_id.value(atomIndex) || hierarchy.atoms.label_comp_id.value(atomIndex)
      );
      if (residueName) residueNames.add(residueName);
    });
  }
  return [...residueNames];
}

/** Selects one user-facing ligand pose, preferring size and then occupancy. */
export function selectPrimaryPocketLigandLoci(loci: StructureElement.Loci): StructureElement.Loci {
  type Candidate = {
    unit: StructureElement.Loci['elements'][number]['unit'];
    indices: StructureElement.UnitIndex[];
    residueName: string;
    occupancyTotal: number;
    occupancyCount: number;
  };

  const candidates = new Map<string, Candidate>();
  for (const element of loci.elements) {
    const unit = element.unit;
    if (!Unit.isAtomic(unit)) continue;
    const hierarchy = unit.model.atomicHierarchy;
    OrderedSet.forEach(element.indices, (index) => {
      const atom = unit.elements[index];
      const residueIndex = unit.residueIndex[atom];
      const altId = hierarchy.atoms.label_alt_id.value(atom).trim();
      const key = `${unit.id}:${residueIndex}:${altId}`;
      const residueName = (
        hierarchy.atoms.auth_comp_id.value(atom).trim() || hierarchy.atoms.label_comp_id.value(atom).trim()
      ).toUpperCase();
      const occupancy = unit.model.atomicConformation.occupancy.value(atom);
      const candidate = candidates.get(key) ?? {
        unit,
        indices: [],
        residueName,
        occupancyTotal: 0,
        occupancyCount: 0,
      };
      candidate.indices.push(index as StructureElement.UnitIndex);
      if (Number.isFinite(occupancy)) {
        candidate.occupancyTotal += occupancy;
        candidate.occupancyCount += 1;
      }
      candidates.set(key, candidate);
    });
  }

  const allCandidates = [...candidates.values()];
  const displayCandidates = allCandidates.filter((candidate) => isDisplayLigandResidueName(candidate.residueName));
  const primary = (displayCandidates.length > 0 ? displayCandidates : allCandidates).toSorted((left, right) => {
    const atomCountOrder = right.indices.length - left.indices.length;
    if (atomCountOrder !== 0) return atomCountOrder;
    const leftOccupancy = left.occupancyCount > 0 ? left.occupancyTotal / left.occupancyCount : 0;
    const rightOccupancy = right.occupancyCount > 0 ? right.occupancyTotal / right.occupancyCount : 0;
    return rightOccupancy - leftOccupancy;
  })[0];
  if (!primary) return StructureElement.Loci(loci.structure, []);
  return StructureElement.Loci(loci.structure, [
    {
      unit: primary.unit,
      indices: OrderedSet.ofSortedArray(primary.indices),
    },
  ]);
}

export function isStructureDoubleClick(
  previousLoci: StructureElement.Loci | undefined,
  previousAt: number | undefined,
  currentLoci: StructureElement.Loci,
  currentAt: number,
  windowMs = LIGAND_DOUBLE_CLICK_WINDOW_MS
): boolean {
  return Boolean(
    previousLoci &&
    previousAt !== undefined &&
    previousLoci.structure === currentLoci.structure &&
    currentAt >= previousAt &&
    currentAt - previousAt <= windowMs
  );
}

type MolstarStructureLociFocusOptions = {
  durationMs: number;
  extraRadius: number;
  minRadius: number;
  optimizeDirection: boolean;
};

export function focusStructureLociFromDoubleClick(
  previousLoci: StructureElement.Loci | undefined,
  previousAt: number | undefined,
  currentLoci: StructureElement.Loci,
  currentAt: number,
  focusLoci: (loci: StructureElement.Loci, options: MolstarStructureLociFocusOptions) => void
): boolean {
  if (!isStructureDoubleClick(previousLoci, previousAt, currentLoci, currentAt)) return false;
  focusLoci(currentLoci, {
    durationMs: 260,
    extraRadius: 2,
    minRadius: 5.5,
    optimizeDirection: true,
  });
  return true;
}

export function createMolstarViewportSpec(createDefaultPluginUISpec: CreateDefaultPluginUISpec): PluginUISpec {
  const spec = createDefaultPluginUISpec();
  // Keep the ligand-centered pocket camera target stable. Mol*'s default
  // focus handlers recenter on a clicked residue and reset on empty canvas.
  spec.behaviors = spec.behaviors.filter(
    (behavior) =>
      behavior.transformer !== PluginBehaviors.Camera.FocusLoci &&
      behavior.transformer !== PluginBehaviors.Representation.FocusLoci
  );
  spec.config = [
    ...(spec.config ?? []),
    // Synon renders one localized trajectory navigator. Disable Mol*'s native
    // English duplicate so model switching has one visible control authority.
    [PluginConfig.Viewport.ShowTrajectoryControls, false],
  ];
  configureViewportSpec(spec);
  return spec;
}

function getPocketDistancePairs(
  ligandLoci: StructureElement.Loci,
  structure: Structure,
  maxDistance: number
): { pairs: PocketDistancePair[]; residueCount: number } {
  const ligandMembership = new Map<number, Set<number>>();
  const ligandAtoms: Array<{
    unit: Unit.Atomic;
    index: number;
    x: number;
    y: number;
    z: number;
  }> = [];
  const position = Vec3();

  for (const element of ligandLoci.elements) {
    const unit = element.unit;
    if (!Unit.isAtomic(unit)) continue;
    const membership = ligandMembership.get(unit.id) ?? new Set<number>();
    OrderedSet.forEach(element.indices, (index) => {
      membership.add(index);
      unit.conformation.position(unit.elements[index], position);
      ligandAtoms.push({
        unit,
        index,
        x: position[0],
        y: position[1],
        z: position[2],
      });
    });
    ligandMembership.set(unit.id, membership);
  }

  const nearestByResidue = new Map<string, PocketDistancePair>();
  const partnerPosition = Vec3();
  const boundedMaxDistance = Math.max(3, Math.min(10, maxDistance));
  const boundedMaxDistanceSquared = boundedMaxDistance * boundedMaxDistance;

  for (const unit of structure.units) {
    if (!Unit.isAtomic(unit) || unit.proteinElements.length === 0) continue;
    const ligandUnitMembership = ligandMembership.get(unit.id);
    for (let index = 0; index < unit.elements.length; index += 1) {
      if (ligandUnitMembership?.has(index) || !isProteinAtom(unit, index)) continue;

      unit.conformation.position(unit.elements[index], partnerPosition);
      let nearest: PocketDistancePair | undefined;
      for (const ligandAtom of ligandAtoms) {
        const dx = ligandAtom.x - partnerPosition[0];
        const dy = ligandAtom.y - partnerPosition[1];
        const dz = ligandAtom.z - partnerPosition[2];
        const distanceSquared = dx * dx + dy * dy + dz * dz;
        if (distanceSquared > boundedMaxDistanceSquared) continue;
        if (!nearest || distanceSquared < nearest.distance * nearest.distance) {
          nearest = {
            ligandUnit: ligandAtom.unit,
            ligandIndex: ligandAtom.index,
            partnerUnit: unit,
            partnerIndex: index,
            distance: Math.sqrt(distanceSquared),
            residueKey: `${unit.id}:${unit.residueIndex[index]}`,
          };
        }
      }

      if (!nearest) continue;
      const previous = nearestByResidue.get(nearest.residueKey);
      if (!previous || nearest.distance < previous.distance) {
        nearestByResidue.set(nearest.residueKey, nearest);
      }
    }
  }

  const pairs = [...nearestByResidue.values()].toSorted((a, b) => a.distance - b.distance);
  return { pairs, residueCount: pairs.length };
}

function singleAtomLoci(structure: Structure, unit: Unit.Atomic, index: number): StructureElement.Loci {
  return StructureElement.Loci(structure, [
    {
      unit,
      indices: OrderedSet.ofSingleton(index as StructureElement.UnitIndex),
    },
  ]);
}

const EMPTY_SELECTION_SUMMARY: MolstarSelectionSummary = {
  atomCount: 0,
  residueCount: 0,
  chainCount: 0,
};

export function summarizeMolstarSelection(loci: StructureElement.Loci | undefined): MolstarSelectionSummary {
  if (!loci || StructureElement.Loci.isEmpty(loci)) return EMPTY_SELECTION_SUMMARY;

  const residues = new Set<string>();
  const chains = new Set<string>();
  for (const element of loci.elements) {
    const unit = element.unit;
    if (!Unit.isAtomic(unit)) continue;
    OrderedSet.forEach(element.indices, (index) => {
      residues.add(`${unit.id}:${unit.residueIndex[index]}`);
      chains.add(`${unit.id}:${unit.chainIndex[index]}`);
    });
  }

  return {
    atomCount: StructureElement.Loci.size(loci),
    residueCount: residues.size,
    chainCount: chains.size,
  };
}

type PoseColumn = {
  readonly isDefined?: boolean;
  readonly value: (index: number) => unknown;
};

const readPoseColumn = (column: PoseColumn | undefined, index: number): unknown => {
  if (!column || column.isDefined === false) return undefined;
  try {
    return column.value(index);
  } catch {
    return undefined;
  }
};

const readPoseString = (column: PoseColumn | undefined, index: number, fallback: string): string => {
  const value = readPoseColumn(column, index);
  return typeof value === 'string' && value.trim() ? value.trim() : fallback;
};

const readPoseNumber = (column: PoseColumn | undefined, index: number, fallback: number): number => {
  const value = Number(readPoseColumn(column, index));
  return Number.isFinite(value) ? value : fallback;
};

type AtomicHierarchyLocationLookup = {
  readonly elements: ArrayLike<number>;
  readonly residueIndex: ArrayLike<number>;
  readonly chainIndex: ArrayLike<number>;
};

export function resolveAtomicHierarchyLocation(
  unit: AtomicHierarchyLocationLookup,
  unitIndex: number
): { atomIndex: ElementIndex; residueIndex: number; chainIndex: number } {
  const atomIndex = Number(unit.elements[unitIndex]) as ElementIndex;
  return {
    atomIndex,
    residueIndex: Number(unit.residueIndex[atomIndex]),
    chainIndex: Number(unit.chainIndex[atomIndex]),
  };
}

type AtomicWorldPositionLookup = {
  readonly elements: ArrayLike<number>;
  readonly conformation: {
    position(atomIndex: number, target: Vec3): Vec3;
  };
};

export function resolveAtomicWorldPosition(unit: AtomicWorldPositionLookup, unitIndex: number, target: Vec3): Vec3 {
  return unit.conformation.position(Number(unit.elements[unitIndex]), target);
}

export function createPoseAtoms(structure: Structure, selection: StructureElement.Loci | undefined): MolstarPoseAtom[] {
  const poseLoci =
    selection ??
    StructureElement.Loci(
      structure,
      structure.units.filter(Unit.isAtomic).map((unit) => ({
        unit,
        indices: OrderedSet.ofBounds(0, unit.elements.length) as OrderedSet<StructureElement.UnitIndex>,
      }))
    );
  const atoms: MolstarPoseAtom[] = [];
  const seen = new Set<string>();
  const position = Vec3();

  for (const element of poseLoci.elements) {
    const unit = element.unit;
    if (!Unit.isAtomic(unit)) continue;
    const hierarchy = unit.model.atomicHierarchy;
    const atomColumns = hierarchy.atoms as unknown as Record<string, PoseColumn | undefined>;
    const residueColumns = hierarchy.residues as unknown as Record<string, PoseColumn | undefined>;
    const chainColumns = hierarchy.chains as unknown as Record<string, PoseColumn | undefined>;

    OrderedSet.forEach(element.indices, (index) => {
      const { atomIndex, residueIndex, chainIndex } = resolveAtomicHierarchyLocation(unit, index);
      const atomKey = `${unit.id}:${atomIndex}`;
      if (seen.has(atomKey)) return;
      seen.add(atomKey);

      // Mol* selections expose a unit-local index, while hierarchy segment
      // tables are keyed by the model atom element stored in unit.elements.
      // Using the local index here silently borrowed unrelated protein residue
      // metadata for mmCIF ligands whose atoms occur late in the model.
      resolveAtomicWorldPosition(unit, index, position);
      const residueNumber = readPoseNumber(
        residueColumns.auth_seq_id,
        residueIndex,
        readPoseNumber(residueColumns.label_seq_id, residueIndex, 1)
      );
      const chainId = readPoseString(
        chainColumns.auth_asym_id,
        chainIndex,
        readPoseString(chainColumns.label_asym_id, chainIndex, 'A')
      );
      const operatorIdentity =
        unit.conformation.operator.instanceId || unit.conformation.operator.name || `unit-${unit.id}`;
      const atomicConformation = unit.model.atomicConformation as unknown as Record<string, PoseColumn | undefined>;

      atoms.push({
        recordName: isProteinAtom(unit, atomIndex) ? 'ATOM' : 'HETATM',
        serial: atoms.length + 1,
        atomName: readPoseString(
          atomColumns.auth_atom_id,
          atomIndex,
          readPoseString(atomColumns.label_atom_id, atomIndex, 'X')
        ),
        residueName: readPoseString(
          atomColumns.auth_comp_id,
          atomIndex,
          readPoseString(atomColumns.label_comp_id, atomIndex, 'UNK')
        ),
        chainId,
        // Keep partitioned pieces of one chain together, while separating
        // symmetry/operator instances that reuse the same author chain ID.
        chainKey: JSON.stringify([operatorIdentity, unit.chainGroupId, chainId]),
        residueNumber,
        alternateLocation: readPoseString(atomColumns.label_alt_id, atomIndex, ''),
        insertionCode: readPoseString(residueColumns.pdbx_PDB_ins_code, residueIndex, ''),
        x: position[0],
        y: position[1],
        z: position[2],
        occupancy: readPoseNumber(atomicConformation.occupancy, atomIndex, 1),
        temperatureFactor: readPoseNumber(atomicConformation.B_iso_or_equiv, atomIndex, 0),
        element: readPoseString(atomColumns.type_symbol, atomIndex, ''),
      });
    });
  }

  return atoms;
}

export async function createMolstarStructureEngine(
  host: HTMLElement,
  { mode = 'viewport', onSelectionChange }: MolstarStructureEngineOptions = {}
): Promise<MolstarStructureEngine> {
  // Keep only the UI shell lazy. The core model/representation graph above is
  // static within this viewer chunk, while the broad Viewer application spec
  // (and its unused MP4 encoder) is deliberately excluded from the preview.
  const [{ createPluginUI }, { createRoot }, { DefaultPluginUISpec }] = await Promise.all([
    import('molstar/lib/mol-plugin-ui/index'),
    import('react-dom/client'),
    import('molstar/lib/mol-plugin-ui/spec'),
  ]);
  const spec = mode === 'viewport' ? createMolstarViewportSpec(DefaultPluginUISpec) : DefaultPluginUISpec();
  if (mode === 'thumbnail') configureThumbnailSpec(spec);

  let reactRoot: ReturnType<typeof createRoot> | undefined;
  let disposed = false;
  let dockingBaseStructure: StructureRef | undefined;
  let fixedDockingProteinRef: string | undefined;
  let dockingProteinVisible = true;
  let activeDockingLigandRefs: string[] = [];
  let activeDockingProjectionRef: string | undefined;
  let activeDockingProjectionScope: MolstarDockingProjectionState = 'overview';
  let activeDockingStructure: Structure | undefined;
  let activeDockingDistanceRefs: string[] = [];
  type DockingLigandShapeProviderRefs = {
    atoms: string;
    atomRepresentation: string;
    bonds: string;
    bondRepresentation: string;
  };
  const dockingLigandVisualCache = new Map<string, { refs: DockingLigandShapeProviderRefs; color: Color }>();
  let pocketInteractionStrengthProviderRef: string | undefined;
  let pocketInteractionStrengthRepresentationRef: string | undefined;
  let pocketInteractionStrengthLabelCount = 0;
  let pocketInteractionAnchorProviderRef: string | undefined;
  let electrostaticVolumes: MolstarElectrostaticVolumeHandles = {};
  let dockingProjectionGeneration = 0;
  const latestDockingRefinement = createLatestDockingRefinementQueue();
  const cancelDockingRefinement = () => {
    latestDockingRefinement.cancel();
    dockingProjectionGeneration += 1;
  };
  let selectedDockingLigandResidueName: string | undefined;
  let loadedFormat = '';
  const structureObjectVisibility: Record<StructureObjectKind, boolean> = {
    protein: true,
    ligand: true,
  };
  let structureLigandColor = POCKET_COLORS.ligandCarbon;
  let pocketGraphGeneration = 0;
  const plugin = await createPluginUI({
    target: host,
    render: (element, target) => {
      reactRoot = createRoot(target);
      reactRoot.render(element);
    },
    spec,
  });
  plugin.canvas3d?.setProps({
    camera: { helper: { axes: { name: 'off', params: {} } } },
  });
  plugin.canvas3d?.commit(true);

  const getPocketDistanceRootRefs = (): string[] => {
    const taggedCells = plugin.state.data.select(StateSelection.Generators.root.subtree().withTag(POCKET_DISTANCE_TAG));
    const taggedRefs = new Set(taggedCells.map((cell) => cell.transform.ref));
    return taggedCells.filter((cell) => !taggedRefs.has(cell.transform.parent)).map((cell) => cell.transform.ref);
  };

  const removePocketDistanceRefs = async (refs: readonly string[]) => {
    if (refs.length === 0) return;
    const update = plugin.state.data.build();
    refs.forEach((ref) => update.delete(ref));
    await update.commit();
  };

  const setPocketInteractionRenderVisibility = (strengthLabelCount: number): void => {
    pocketInteractionStrengthLabelCount = strengthLabelCount;
    const { nativeHidden, strengthHidden } = resolvePocketInteractionRenderVisibility(strengthLabelCount);
    const hideStrength = strengthHidden || !dockingProteinVisible;
    const hideNative = nativeHidden || !dockingProteinVisible;
    if (pocketInteractionStrengthProviderRef && plugin.state.data.cells.has(pocketInteractionStrengthProviderRef)) {
      setSubtreeVisibility(plugin.state.data, pocketInteractionStrengthProviderRef, hideStrength);
    }
    for (const tag of POCKET_NATIVE_INTERACTION_TAGS) {
      for (const cell of plugin.state.data.select(StateSelection.Generators.root.subtree().withTag(tag))) {
        setSubtreeVisibility(plugin.state.data, cell.transform.ref, hideNative);
      }
    }
    host.dataset.synonPocketInteractionRenderMode = strengthLabelCount > 0 ? 'native-with-strength-labels' : 'native';
    host.dataset.synonPocketInteractionStrengthCount = String(strengthLabelCount);
    host.dataset.synonPocketInteractionVisible = String(dockingProteinVisible);
  };

  const setPocketInteractionStrengths = async (
    records: readonly MolstarInteractionStrengthRecord[],
    visibility: MolstarPocketInteractionVisibility = {}
  ): Promise<void> => {
    const data = createMolstarInteractionStrengthLabelData(records, visibility);
    const provider = pocketInteractionStrengthProviderRef
      ? plugin.state.data.cells.get(pocketInteractionStrengthProviderRef)?.obj
      : undefined;
    const representation = pocketInteractionStrengthRepresentationRef
      ? plugin.state.data.cells.get(pocketInteractionStrengthRepresentationRef)?.obj
      : undefined;
    if (PluginStateObject.Shape.Provider.is(provider) && PluginStateObject.Shape.Representation3D.is(representation)) {
      if (data.labels.length > 0) {
        provider.data.data = data;
        await representation.data.repr.createOrUpdate({}, data).run();
      }
      setPocketInteractionRenderVisibility(data.labels.length);
      plugin.canvas3d?.requestDraw();
      return;
    }

    const staleProviderRefs = [pocketInteractionStrengthProviderRef].filter((ref): ref is string =>
      Boolean(ref && plugin.state.data.cells.has(ref))
    );
    if (staleProviderRefs.length > 0) {
      const update = plugin.state.data.build();
      staleProviderRefs.forEach((ref) => update.delete(ref));
      await update.commit();
    }
    pocketInteractionStrengthProviderRef = undefined;
    pocketInteractionStrengthRepresentationRef = undefined;
    if (data.labels.length === 0) {
      setPocketInteractionRenderVisibility(0);
      plugin.canvas3d?.requestDraw();
      return;
    }

    const update = plugin.state.data.build();
    const strengthProvider = update
      .toRoot()
      .apply(SynonBiomedInteractionStrengthLabels, { data }, { tags: 'synon-biomed-pocket-interaction-strengths' });
    const strengthRepresentation = strengthProvider.apply(
      StateTransforms.Representation.ShapeRepresentation3D,
      { quality: 'high' },
      { tags: 'synon-biomed-pocket-interaction-strengths-representation' }
    );
    await update.commit();
    pocketInteractionStrengthProviderRef = strengthProvider.ref;
    pocketInteractionStrengthRepresentationRef = strengthRepresentation.ref;
    setPocketInteractionRenderVisibility(data.labels.length);
    plugin.canvas3d?.requestDraw();
  };

  const clearPocketState = async () => {
    plugin.managers.structure.focus.clear();
    await removePocketDistanceRefs(getPocketDistanceRootRefs());
    if (pocketInteractionAnchorProviderRef && plugin.state.data.cells.has(pocketInteractionAnchorProviderRef)) {
      const update = plugin.state.data.build();
      update.delete(pocketInteractionAnchorProviderRef);
      await update.commit();
    }
    pocketInteractionAnchorProviderRef = undefined;
    delete host.dataset.synonPocketLigandAtomCount;
  };

  const applyRepresentationPreset = async (preset: MolstarRepresentationPreset, options?: MolstarSurfaceOptions) => {
    await clearPocketState();
    const structures = [...plugin.managers.structure.hierarchy.current.structures];
    if (structures.length === 0) return;

    if (preset === 'auto') {
      await plugin.managers.structure.component.applyPreset(
        structures,
        PresetStructureRepresentations.auto,
        INITIAL_REPRESENTATION_PRESET_PARAMS
      );
    } else {
      await plugin.managers.structure.component.applyPreset(structures, PresetStructureRepresentations[preset]);
    }

    if (preset === 'molecular-surface' && options) {
      const boundedProbeRadius = Math.max(0.8, Math.min(2.4, options.probeRadius));
      const boundedAlpha = Math.max(0.2, Math.min(1, options.alpha));
      const update = plugin.state.data.build();
      let changed = false;

      for (const structure of plugin.managers.structure.hierarchy.current.structures) {
        for (const component of structure.components) {
          for (const representation of component.representations) {
            if (representation.cell.transform.params.type.name !== 'molecular-surface') continue;
            update.to(representation.cell).update((old) => {
              old.type.params.probeRadius = boundedProbeRadius;
              old.type.params.alpha = boundedAlpha;
            });
            changed = true;
          }
        }
      }

      if (changed) await update.commit();
    }
    applyStructureObjectVisibility();
    await applyStructureLigandColor();
    plugin.handleResize();
  };

  const applyRepresentationStyle = async (representation: MolstarViewRepresentation) => {
    if (representation === 'initial') {
      // Restore the same native preset used when the viewport first loads. This
      // keeps protein cartoons and ligand sticks in Mol*'s normal composition.
      await applyRepresentationPreset('auto');
      return;
    }

    await clearPocketState();

    const structures = [...plugin.managers.structure.hierarchy.current.structures];
    if (structures.length === 0) return;

    // ComponentManager applies the provider and only then synchronizes away
    // obsolete components. This create-before-retire transition keeps at least
    // one representation on canvas throughout every style switch.
    await plugin.managers.structure.component.applyPreset(
      structures,
      createSynonViewRepresentationPreset(representation, electrostaticVolumes)
    );

    applyStructureObjectVisibility();
    await applyStructureLigandColor();
    plugin.handleResize();
  };

  const applyPocketFocus = async (
    options: MolstarPocketOptions,
    targetLigandLoci?: StructureElement.Loci,
    targetStructure?: StructureRef,
    renderLigandVisual = true
  ): Promise<MolstarPocketSummary> => {
    const structures = targetStructure
      ? [targetStructure]
      : [...plugin.managers.structure.hierarchy.current.structures];
    const structureCell = structures[0]?.cell;
    const structure = structureCell?.obj?.data;
    if (!structureCell || !structure) {
      return {
        hasLigand: false,
        ligandCount: 0,
        residueCount: 0,
        distanceCount: 0,
      };
    }

    // Resolve the ligand before changing any Mol* state. A receptor-only file
    // must leave its current protein representation untouched when the user
    // presses the pocket action.
    const ligandSelectionLoci = resolvePocketLigandLoci(structure, targetLigandLoci, (currentStructure) =>
      StructureQuery.loci(StructureSelectionQueries.ligand.query, currentStructure)
    );
    if (StructureElement.Loci.isEmpty(ligandSelectionLoci)) {
      return {
        hasLigand: false,
        ligandCount: 0,
        residueCount: 0,
        distanceCount: 0,
      };
    }

    const previousComponentRefs = new Set(
      structures.flatMap((currentStructure) =>
        currentStructure.components.map((component) => component.cell.transform.ref)
      )
    );
    // A docking replacement is assembled below a hidden candidate trajectory.
    // Keep the visible pose and its distance graph intact until the candidate
    // has every pocket representation ready for the atomic visibility swap.
    if (!targetStructure) await clearPocketState();
    const stagedRepresentationOptions = (tag: string) => ({
      tag,
      initialState: targetStructure ? { isHidden: true } : undefined,
    });

    const ligandLoci = targetLigandLoci ? ligandSelectionLoci : selectPrimaryPocketLigandLoci(ligandSelectionLoci);
    const layerPlan = resolvePocketLayerPlan(options.displayLayers ?? []);
    const ligandSurfaceResidueNames = resolveStructureLociResidueNames(ligandLoci);
    const ligandSurfacePlans = layerPlan.hasLigandSurface
      ? resolveDockingLigandElectrostaticSurfaces(ligandSurfaceResidueNames, electrostaticVolumes)
      : [];
    if ((layerPlan.hasProteinSurface || layerPlan.hasPocketSurface) && !electrostaticVolumes.protein) {
      throw new Error('MOLSTAR_ELECTROSTATIC_PROTEIN_VOLUME_MISSING');
    }
    host.dataset.synonPocketLigandAtomCount = String(StructureElement.Loci.size(ligandLoci));
    {
      const completeInteractionProps = createPocketInteractionProps(
        InteractionsProvider.defaultParams,
        options.interactionVisibility
      );
      await plugin.runTask(
        Task.create('Compute complete pocket interactions', async (runtime) => {
          await InteractionsProvider.attach(
            {
              runtime,
              assetManager: plugin.managers.asset,
              errorContext: plugin.errorContext,
            },
            structure,
            // Mol* 5.5 exposes nested ParamDefinition nodes in this generated type, while attach consumes their values.
            completeInteractionProps as never
          );
        })
      );
    }
    const graphKey = `pocket-${++pocketGraphGeneration}`;
    const ligandExpression = StructureElement.Bundle.toExpression(StructureElement.Bundle.fromLoci(ligandLoci));
    const ligandSelection = StructureSelectionQuery('Pocket ligand', ligandExpression);

    const boundedExpandRadius = Math.max(3, Math.min(8, options.expandRadius));
    const ligandPlusSurroundings = StructureSelectionQuery(
      `Pocket surroundings (${boundedExpandRadius} Å)`,
      MS.struct.modifier.includeSurroundings({
        0: ligandExpression,
        radius: boundedExpandRadius,
        'as-whole-residues': true,
      })
    );
    const pocketProteinExpression = MS.struct.modifier.intersectBy({
      0: ligandPlusSurroundings.expression,
      by: StructureSelectionQueries.protein.expression,
    });
    const ligandSurroundings = StructureSelectionQuery(
      `Pocket protein residues (${boundedExpandRadius} Å)`,
      MS.struct.modifier.exceptBy({
        0: pocketProteinExpression,
        by: ligandExpression,
      })
    );
    const proteinOutsidePocket = StructureSelectionQuery(
      `Protein outside pocket (${boundedExpandRadius} Å)`,
      MS.struct.modifier.exceptBy({
        0: StructureSelectionQueries.protein.expression,
        by: ligandPlusSurroundings.expression,
      })
    );
    const ligandComponent = await plugin.builders.structure.tryCreateComponentFromSelection(
      structureCell,
      ligandSelection,
      `${graphKey}-ligand`,
      { label: 'Pocket ligand' }
    );
    const ligandSurfaceComponents = await Promise.all(
      ligandSurfacePlans.map(async ({ residueName }, index) => {
        const selected = filterStructureLociByResidueName(ligandLoci, residueName);
        if (StructureElement.Loci.isEmpty(selected)) {
          throw new Error(`MOLSTAR_DOCKING_LIGAND_MISSING:${residueName}`);
        }
        const expression = StructureElement.Bundle.toExpression(StructureElement.Bundle.fromLoci(selected));
        const component = await plugin.builders.structure.tryCreateComponentFromSelection(
          structureCell,
          StructureSelectionQuery(`Pocket ligand surface ${residueName}`, expression),
          `${graphKey}-ligand-surface-${index}`,
          { label: `Pocket ligand surface ${residueName}` }
        );
        if (!component) throw new Error(`MOLSTAR_DOCKING_LIGAND_MISSING:${residueName}`);
        return component;
      })
    );
    const pocketInteractionComponent = await plugin.builders.structure.tryCreateComponentFromSelection(
      structureCell,
      ligandPlusSurroundings,
      `${graphKey}-interaction-surroundings`,
      { label: 'Pocket interaction surroundings' }
    );
    const pocketResidueComponent = await plugin.builders.structure.tryCreateComponentFromSelection(
      structureCell,
      ligandSurroundings,
      `${graphKey}-residues`,
      { label: 'Pocket residues' }
    );
    const proteinOutsideComponent =
      dockingBaseStructure && targetStructure
        ? undefined
        : await plugin.builders.structure.tryCreateComponentFromSelection(
            structureCell,
            proteinOutsidePocket,
            `${graphKey}-protein-outside`,
            { label: 'Protein outside pocket' }
          );
    const layeredProteinComponent = layerPlan.hasProteinOverlay
      ? await presetStaticComponent(plugin, structureCell, 'protein', {
          label: 'Layered pocket protein',
        })
      : undefined;

    if (proteinOutsideComponent) {
      await plugin.builders.structure.representation.addRepresentation(
        proteinOutsideComponent,
        {
          type: 'cartoon',
          typeParams: {
            sizeFactor: 0.17,
            helixProfile: 'rounded',
            alpha: 0.38,
          },
          color: 'uniform',
          colorParams: { value: POCKET_COLORS.outsideProtein },
        },
        stagedRepresentationOptions('synon-biomed-pocket-protein')
      );
    }

    if (pocketResidueComponent) {
      await plugin.builders.structure.representation.addRepresentation(
        pocketResidueComponent,
        {
          type: 'ball-and-stick',
          typeParams: {
            sizeFactor: 0.075,
            sizeAspectRatio: 0.42,
            adjustCylinderLength: false,
            linkCap: true,
            ignoreHydrogens: true,
            ignoreHydrogensVariant: 'all',
            multipleBonds: 'off',
            visuals: ['intra-bond', 'inter-bond'],
            colorMode: 'interpolate',
          },
          color: 'element-symbol',
          colorParams: {
            carbonColor: {
              name: 'uniform',
              params: { value: POCKET_COLORS.pocketCarbon },
            },
          },
        },
        stagedRepresentationOptions('synon-biomed-pocket-residues')
      );
    }

    if (ligandComponent && renderLigandVisual) {
      await plugin.builders.structure.representation.addRepresentation(
        ligandComponent,
        {
          type: 'ball-and-stick',
          typeParams: POCKET_LIGAND_STICK_TYPE_PARAMS,
          color: 'element-symbol',
          colorParams: {
            carbonColor: {
              name: 'uniform',
              params: {
                value: options.ligandColor ?? POCKET_COLORS.ligandCarbon,
              },
            },
          },
        },
        stagedRepresentationOptions('synon-biomed-pocket-ligand')
      );
    }

    if (layeredProteinComponent && (layerPlan.hasBallAndStick || layerPlan.hasLine)) {
      await plugin.builders.structure.representation.addRepresentation(
        layeredProteinComponent,
        layerPlan.hasBallAndStick
          ? {
              type: 'ball-and-stick',
              typeParams: {
                sizeFactor: 0.38,
                sizeAspectRatio: 0.72,
                ignoreHydrogens: true,
                ignoreHydrogensVariant: 'all',
              },
              color: 'element-symbol',
            }
          : {
              type: 'line',
              typeParams: {
                sizeFactor: 0.7,
                lineSizeAttenuation: false,
                ignoreHydrogens: true,
                ignoreHydrogensVariant: 'all',
              },
              color: 'element-symbol',
            },
        stagedRepresentationOptions(
          layerPlan.hasBallAndStick ? 'synon-biomed-pocket-layer-ball-and-stick' : 'synon-biomed-pocket-layer-line'
        )
      );
    }

    const addPocketElectrostaticSurface = async (
      component: typeof layeredProteinComponent,
      typeParams: typeof ELECTROSTATIC_SURFACE_TYPE_PARAMS | typeof POCKET_SURFACE_TYPE_PARAMS,
      tag: string,
      volume: MolstarElectrostaticVolume | undefined
    ) => {
      if (!component) return;
      if (!volume) throw new Error('MOLSTAR_ELECTROSTATIC_VOLUME_MISSING');
      await plugin.builders.structure.representation.addRepresentation(
        component,
        {
          type: 'molecular-surface',
          typeParams,
          ...resolveSurfaceColorSettings(component.obj?.data ?? structure, volume),
        },
        stagedRepresentationOptions(tag)
      );
    };

    if (layerPlan.hasProteinSurface) {
      await addPocketElectrostaticSurface(
        layeredProteinComponent,
        ELECTROSTATIC_SURFACE_TYPE_PARAMS,
        'synon-biomed-pocket-layer-protein-surface',
        electrostaticVolumes.protein
      );
    }
    if (layerPlan.hasPocketSurface) {
      await addPocketElectrostaticSurface(
        pocketResidueComponent,
        POCKET_SURFACE_TYPE_PARAMS,
        'synon-biomed-pocket-layer-pocket-surface',
        electrostaticVolumes.protein
      );
    }
    await Promise.all(
      ligandSurfacePlans.map((plan, index) =>
        addPocketElectrostaticSurface(
          ligandSurfaceComponents[index],
          ELECTROSTATIC_SURFACE_TYPE_PARAMS,
          `synon-biomed-pocket-layer-ligand-surface-${index}`,
          plan.volume
        )
      )
    );

    if (ligandComponent) {
      await plugin.builders.structure.representation.addRepresentation(
        ligandComponent,
        {
          type: InteractionsRepresentationProvider,
          typeParams: {
            ...POCKET_INTERACTION_REPRESENTATION_TYPE_PARAMS,
            visuals: [...POCKET_INTERACTION_VISUAL_GROUPS.ligand],
          },
          color: InteractionTypeColorThemeProvider,
        },
        stagedRepresentationOptions('synon-biomed-pocket-interactions')
      );
    }

    if (pocketInteractionComponent) {
      await plugin.builders.structure.representation.addRepresentation(
        pocketInteractionComponent,
        {
          type: InteractionsRepresentationProvider,
          typeParams: {
            ...POCKET_INTERACTION_LINE_TYPE_PARAMS,
            visuals: [...POCKET_INTERACTION_VISUAL_GROUPS.pocketBridges],
          },
          color: InteractionTypeColorThemeProvider,
        },
        stagedRepresentationOptions('synon-biomed-pocket-water-bridges')
      );
    }

    if (!targetStructure) {
      const interactionAnchorData = createMolstarInteractionFeatureAnchorData(structure, ligandLoci);
      if (interactionAnchorData.anchors.length > 0) {
        const update = plugin.state.data.build();
        const anchorProvider = update
          .toRoot()
          .apply(
            SynonBiomedInteractionFeatureAnchors,
            { data: interactionAnchorData },
            { tags: 'synon-biomed-pocket-interaction-feature-anchors' }
          );
        anchorProvider.apply(
          StateTransforms.Representation.ShapeRepresentation3D,
          { quality: 'medium' },
          {
            tags: 'synon-biomed-pocket-interaction-feature-anchors-representation',
          }
        );
        await update.commit();
        pocketInteractionAnchorProviderRef = anchorProvider.ref;
      }
    }

    const distancePairs = getPocketDistancePairs(ligandLoci, structure, boundedExpandRadius);
    const contactResidueLoci = distancePairs.pairs
      .slice(0, POCKET_DISTANCE_MARKER_LIMIT)
      .reduce(
        (loci, pair) =>
          StructureElement.Loci.union(
            loci,
            StructureElement.Loci.firstResidue(singleAtomLoci(structure, pair.partnerUnit, pair.partnerIndex))
          ),
        StructureElement.Loci(structure, [])
      );
    let contactResidueComponent: Awaited<ReturnType<typeof plugin.builders.structure.tryCreateComponentFromSelection>>;
    if (!StructureElement.Loci.isEmpty(contactResidueLoci)) {
      const contactResidueSelection = StructureSelectionQuery(
        'Pocket contact residues',
        StructureElement.Bundle.toExpression(StructureElement.Bundle.fromLoci(contactResidueLoci))
      );
      contactResidueComponent = await plugin.builders.structure.tryCreateComponentFromSelection(
        structureCell,
        contactResidueSelection,
        `${graphKey}-contact-residues`,
        { label: 'Pocket contact residues' }
      );
      if (contactResidueComponent) {
        await plugin.builders.structure.representation.addRepresentation(
          contactResidueComponent,
          {
            type: 'label',
            typeParams: {
              level: 'residue',
              background: true,
              backgroundColor: POCKET_COLORS.labelBackground,
              backgroundOpacity: 0.9,
              borderWidth: 0,
              tether: true,
              tetherLength: 0.5,
              sizeFactor: 0.68,
            },
            color: 'uniform',
            colorParams: { value: POCKET_COLORS.label },
          },
          stagedRepresentationOptions('synon-biomed-pocket-labels')
        );
      }
    }
    const distanceResults = options.showDistances
      ? await Promise.all(
          distancePairs.pairs.slice(0, POCKET_DISTANCE_MARKER_LIMIT).map(async (pair) => {
            const result = await plugin.managers.structure.measurement.addDistance(
              singleAtomLoci(structure, pair.ligandUnit, pair.ligandIndex),
              singleAtomLoci(structure, pair.partnerUnit, pair.partnerIndex),
              {
                customText: `${pair.distance.toFixed(2)} Å`,
                selectionTags: POCKET_DISTANCE_TAG,
                reprTags: POCKET_DISTANCE_TAG,
                visualParams: {
                  visuals: ['lines', 'text'],
                  textSize: 0.58,
                  linesSize: 0.08,
                  lineSizeAttenuation: false,
                  textColor: POCKET_COLORS.distance,
                  background: true,
                  backgroundColor: POCKET_COLORS.labelBackground,
                  backgroundOpacity: 0.92,
                  borderWidth: 0,
                },
              }
            );
            if (targetStructure && result) {
              setSubtreeVisibility(plugin.state.data, result.selection.ref, true);
            }
            return result;
          })
        )
      : [];
    const distanceCount = distanceResults.filter(Boolean).length;

    // Retire the previous graph only after every pocket representation is on
    // canvas. Reused component refs are retained, so repeated pocket focus is
    // idempotent and cannot delete the graph it just updated.
    const retainedComponentRefs = new Set(
      [
        ligandComponent,
        pocketInteractionComponent,
        pocketResidueComponent,
        proteinOutsideComponent,
        layeredProteinComponent,
        contactResidueComponent,
      ]
        .filter(Boolean)
        .map((component) => component!.ref)
    );
    const obsoleteComponentRefs = [...previousComponentRefs].filter((ref) => !retainedComponentRefs.has(ref));
    if (obsoleteComponentRefs.length > 0) {
      const update = plugin.state.data.build();
      obsoleteComponentRefs.forEach((ref) => update.delete(ref));
      await update.commit();
    }

    if (shouldFocusPocketCamera(options)) {
      plugin.managers.camera.focusLoci(ligandLoci, {
        durationMs: 260,
        // Initial pocket entry frames the ligand once. Subsequent pose or
        // color changes rebuild representations with focusCamera=false so the
        // user's receptor viewpoint remains untouched.
        extraRadius: Math.max(1.5, Math.min(2.5, boundedExpandRadius * 0.5)),
        minRadius: Math.max(5.5, boundedExpandRadius * 1.2),
        optimizeDirection: true,
      });
    }
    if (!targetStructure) plugin.handleResize();
    return {
      hasLigand: true,
      ligandCount: StructureElement.Loci.size(ligandLoci),
      residueCount: distancePairs.residueCount,
      distanceCount,
    };
  };

  let pendingClickLoci: StructureElement.Loci | undefined;
  let pendingClickAt: number | undefined;
  let pendingClickTimer: ReturnType<typeof setTimeout> | undefined;
  const clearPendingClick = () => {
    if (pendingClickTimer !== undefined) {
      clearTimeout(pendingClickTimer);
      pendingClickTimer = undefined;
    }
    pendingClickLoci = undefined;
    pendingClickAt = undefined;
  };
  const clickSubscription = plugin.behaviors.interaction.click.subscribe(({ current }) => {
    const shapeResidueNames = resolveDockingLigandShapeResidueNames(current.loci);
    if (shapeResidueNames.length === 1) {
      selectedDockingLigandResidueName = shapeResidueNames[0];
      plugin.managers.interactivity.lociSelects.selectOnly(current, false);
      clearPendingClick();
      onSelectionChange?.(EMPTY_SELECTION_SUMMARY);
      return;
    }

    const clickedLoci = normalizeStructureElementClickLoci(current.loci);
    if (!clickedLoci || StructureElement.Loci.isEmpty(clickedLoci)) {
      selectedDockingLigandResidueName = undefined;
      clearPendingClick();
      onSelectionChange?.(EMPTY_SELECTION_SUMMARY);
      return;
    }
    selectedDockingLigandResidueName = undefined;

    const clickedAt = Date.now();
    if (
      focusStructureLociFromDoubleClick(pendingClickLoci, pendingClickAt, clickedLoci, clickedAt, (loci, options) =>
        plugin.managers.camera.focusLoci(loci, options)
      )
    ) {
      clearPendingClick();
      return;
    }

    clearPendingClick();
    pendingClickLoci = clickedLoci;
    pendingClickAt = clickedAt;
    pendingClickTimer = setTimeout(clearPendingClick, LIGAND_DOUBLE_CLICK_WINDOW_MS);
  });

  const getCurrentStructure = (): Structure | undefined =>
    activeDockingStructure ?? plugin.managers.structure.hierarchy.current.structures[0]?.cell.obj?.data;

  const getStructureComposition = (): MolstarStructureComposition =>
    summarizeStructureComposition(getCurrentStructure(), loadedFormat);

  const createLigandDepictionSource = (
    structure: Structure,
    candidateLigands: StructureElement.Loci
  ): MolstarLigandDepictionSource | undefined => {
    const primaryLigand = selectPrimaryPocketLigandLoci(candidateLigands);
    if (StructureElement.Loci.isEmpty(primaryLigand)) return undefined;

    let residueName = '';
    for (const element of primaryLigand.elements) {
      const unit = element.unit;
      if (!Unit.isAtomic(unit)) continue;
      OrderedSet.forEach(element.indices, (index) => {
        if (residueName) return;
        const atom = unit.elements[index];
        residueName =
          unit.model.atomicHierarchy.atoms.auth_comp_id.value(atom).trim() ||
          unit.model.atomicHierarchy.atoms.label_comp_id.value(atom).trim();
      });
      if (residueName) break;
    }
    residueName = residueName.trim().toUpperCase() || 'Ligand';
    const interactionResidueName = /^[A-Z0-9]{1,3}$/.test(residueName) ? residueName : 'LIG';
    const parsedLigandShape = createDockingLigandShapeData(primaryLigand, structureLigandColor);
    const ligandShape =
      loadedFormat === 'mmcif'
        ? applyDockingLigandBondDefinitions(parsedLigandShape, readMmcifLigandBondDefinitions(structure, residueName))
        : parsedLigandShape;
    const protein = StructureQuery.loci(StructureSelectionQueries.protein.query, structure);
    const hasProtein = !StructureElement.Loci.isEmpty(protein);
    let complexPdb: string | undefined;
    if (hasProtein) {
      const poseAtoms = createPoseAtoms(structure, StructureElement.Loci.union(protein, primaryLigand)).map((atom) =>
        atom.recordName === 'HETATM' ? Object.assign({}, atom, { residueName: interactionResidueName }) : atom
      );
      if (poseAtoms.length > 0) complexPdb = formatMolstarPosePdb(poseAtoms);
    }
    return {
      residueName,
      interactionResidueName,
      molBlock: formatDockingLigandMolBlock(ligandShape, residueName, {
        // Small-molecule formats carry authoritative aromatic topology. PDB,
        // PQR and mmCIF Mol* flags can also contain geometry-derived aromatic
        // accents, which must not be promoted into RDKit chemistry.
        preserveAromaticBonds: loadedFormat === 'mol' || loadedFormat === 'sdf' || loadedFormat === 'mol2',
      }),
      complexPdb,
      atomCount: ligandShape.atoms.length,
      hasProtein,
    };
  };

  const getPrimaryLigandDepictionSource = (): MolstarLigandDepictionSource | undefined => {
    const structure = getCurrentStructure();
    if (!structure) return undefined;
    const composition = getStructureComposition();
    const residueNames = composition.objects
      .filter((object) => object.kind === 'ligand')
      .flatMap((object) => object.residueNames);
    let queriedLigands = StructureQuery.loci(StructureSelectionQueries.ligand.query, structure);
    if (StructureElement.Loci.isEmpty(queriedLigands) && composition.hasLigand && !composition.hasProtein) {
      queriedLigands = StructureQuery.loci(StructureSelectionQueries.all.query, structure);
    }
    const displayLigands =
      residueNames.length > 0 ? filterStructureLociByResidueNames(queriedLigands, residueNames) : queriedLigands;
    return createLigandDepictionSource(structure, displayLigands);
  };

  const getElectrostaticInputSource = (
    ligandResidueNames?: readonly string[]
  ): MolstarElectrostaticInputSource | undefined => {
    const requestedLigands = [
      ...new Set((ligandResidueNames ?? []).map(normalizeElectrostaticLigandKey).filter(Boolean)),
    ];
    const dockingStructure = dockingBaseStructure?.cell.obj?.data;
    const useRequestedLigands = ligandResidueNames !== undefined;
    const structure = useRequestedLigands && dockingStructure ? dockingStructure : getCurrentStructure();
    if (!structure) return undefined;
    const atoms = createPoseAtoms(structure, undefined);
    if (atoms.length === 0) return undefined;
    const ligands: MolstarElectrostaticLigandInputSource[] = [];
    if (useRequestedLigands) {
      const allLigands = StructureQuery.loci(StructureSelectionQueries.ligand.query, structure);
      for (const key of requestedLigands) {
        const ligand = createLigandDepictionSource(structure, filterStructureLociByResidueName(allLigands, key));
        if (!ligand?.molBlock) throw new Error(`MOLSTAR_ELECTROSTATIC_LIGAND_INPUT_MISSING:${key}`);
        ligands.push({ key, molBlock: ligand.molBlock, atomCount: ligand.atomCount });
      }
    } else {
      const ligand = getPrimaryLigandDepictionSource();
      if (ligand?.molBlock) {
        ligands.push({
          key: normalizeElectrostaticLigandKey(ligand.residueName) || 'LIGAND',
          molBlock: ligand.molBlock,
          atomCount: ligand.atomCount,
        });
      }
    }
    return {
      content: formatMolstarPosePdb(atoms),
      ligands,
      atomCount: atoms.length,
    };
  };

  const setElectrostaticPotentials = async (
    sources: MolstarElectrostaticPotentialSources,
    range: readonly [number, number]
  ): Promise<void> => {
    type PotentialEntry = {
      role: MolstarElectrostaticMapRole;
      key?: string;
      source: MolstarElectrostaticPotentialSource;
    };
    const entries: PotentialEntry[] = [];
    if (sources.protein) entries.push({ role: 'protein', source: sources.protein });
    const normalizedLigandKeys = new Set<string>();
    for (const [rawKey, source] of Object.entries(sources.ligands ?? {})) {
      const key = normalizeElectrostaticLigandKey(rawKey);
      if (!key || normalizedLigandKeys.has(key)) throw new Error('MOLSTAR_ELECTROSTATIC_INPUT_INVALID');
      normalizedLigandKeys.add(key);
      entries.push({ role: 'ligand', key, source });
    }
    if (
      entries.length === 0 ||
      entries.some(({ source }) => !source.source.trim() || !source.label.trim()) ||
      !Number.isFinite(range[0]) ||
      !Number.isFinite(range[1]) ||
      range[0] >= range[1]
    ) {
      throw new Error('MOLSTAR_ELECTROSTATIC_INPUT_INVALID');
    }
    const previousVolumes = electrostaticVolumes;
    const nextVolumes: MolstarElectrostaticVolumeHandles = { ligands: {} };
    const stagedSourceRefs: string[] = [];
    try {
      // Mol* state-tree writes are intentionally serialized; concurrent raw
      // data and volume commits can race on the same state snapshot.
      const loaded = await entries.reduce<
        Promise<Array<{ role: MolstarElectrostaticMapRole; key?: string; volume: MolstarElectrostaticVolumeHandle }>>
      >(async (pending, { role, key, source }) => {
        const current = await pending;
        const raw = await plugin.builders.data.rawData({ data: source.source, label: source.label });
        stagedSourceRefs.push(raw.ref);
        const parsed = await DxProvider.parse(plugin, raw);
        current.push({
          role,
          key,
          volume: {
            ref: parsed.volume.ref,
            sourceRef: raw.ref,
            range: [range[0], range[1]],
          },
        });
        return current;
      }, Promise.resolve([]));
      for (const { role, key, volume } of loaded) {
        if (role === 'protein') {
          nextVolumes.protein = volume;
        } else if (key) {
          nextVolumes.ligands![key] = volume;
        }
      }
      const ligandVolumes = Object.values(nextVolumes.ligands ?? {});
      if (ligandVolumes.length === 1) nextVolumes.ligand = ligandVolumes[0];
    } catch (reason) {
      const rollback = plugin.state.data.build();
      let changed = false;
      for (const sourceRef of stagedSourceRefs) {
        if (!plugin.state.data.cells.has(sourceRef)) continue;
        rollback.delete(sourceRef);
        changed = true;
      }
      if (changed) await rollback.commit();
      throw reason;
    }
    electrostaticVolumes = nextVolumes;
    const cleanup = plugin.state.data.build();
    let changed = false;
    const previousVolumeByRef = new Map<string, MolstarElectrostaticVolumeHandle>();
    for (const previous of [
      previousVolumes.protein,
      previousVolumes.ligand,
      ...Object.values(previousVolumes.ligands ?? {}),
    ]) {
      if (previous) previousVolumeByRef.set(previous.sourceRef, previous);
    }
    for (const previous of previousVolumeByRef.values()) {
      if (!plugin.state.data.cells.has(previous.sourceRef)) continue;
      cleanup.delete(previous.sourceRef);
      changed = true;
    }
    if (changed) await cleanup.commit();
  };

  const applyStructureObjectVisibility = (): void => {
    for (const structure of plugin.managers.structure.hierarchy.current.structures) {
      for (const component of structure.components) {
        const componentData = component.cell.obj?.data;
        if (!componentData) continue;
        const composition = summarizeStructureComposition(componentData, loadedFormat);
        const hidden =
          composition.hasProtein && composition.hasLigand
            ? !structureObjectVisibility.protein && !structureObjectVisibility.ligand
            : composition.hasProtein
              ? !structureObjectVisibility.protein
              : composition.hasLigand
                ? !structureObjectVisibility.ligand
                : false;
        setSubtreeVisibility(plugin.state.data, component.cell.transform.ref, hidden);
      }
    }
    plugin.canvas3d?.syncVisibility();
    plugin.canvas3d?.commit(true);
  };

  const setStructureObjectVisible = (kind: StructureObjectKind, visible: boolean): void => {
    structureObjectVisibility[kind] = visible;
    applyStructureObjectVisibility();
  };

  const applyStructureLigandColor = async (): Promise<void> => {
    const ligandComponents = plugin.managers.structure.hierarchy.current.structures.flatMap((structure) =>
      structure.components.filter((component) => {
        const componentData = component.cell.obj?.data;
        if (!componentData) return false;
        const composition = summarizeStructureComposition(componentData, loadedFormat);
        return composition.hasLigand && !composition.hasProtein;
      })
    );
    if (ligandComponents.length === 0) return;
    await plugin.managers.structure.component.updateRepresentationsTheme(
      ligandComponents,
      (_component, representation) => {
        const representationType = representation.cell.transform.params.type.name;
        if (!isStructureLigandCarbonColorRepresentation(representationType)) return {};
        return {
          color: 'element-symbol' as const,
          colorParams: {
            carbonColor: {
              name: 'uniform' as const,
              params: {
                value: structureLigandColor,
                saturation: 0,
                lightness: 0,
              },
            },
            saturation: 0,
            lightness: 0.2,
            colors: { name: 'default' as const, params: {} },
          },
        };
      }
    );
    plugin.canvas3d?.requestDraw();
  };

  const setStructureLigandColor = async (color: Color): Promise<void> => {
    structureLigandColor = color;
    await applyStructureLigandColor();
  };

  const getTrajectoryModelState = (): MolstarTrajectoryModelState => {
    const state = plugin.state.data;
    const models = state.selectQ((query) => query.ofTransformer(StateTransforms.Model.ModelFromTrajectory));
    for (const model of models) {
      if (!model.sourceRef) continue;
      const trajectory = state.cells.get(model.sourceRef)?.obj;
      const count = trajectory?.data.frameCount ?? 0;
      if (count > 1) {
        const index = Math.max(0, Math.min(count - 1, Math.round(model.transform.params.modelIndex)));
        return { index, count };
      }
    }
    return { index: 0, count: 1 };
  };

  const advanceTrajectoryModel = async (by: number): Promise<MolstarTrajectoryModelState> => {
    if (!Number.isSafeInteger(by) || by === 0) return getTrajectoryModelState();
    await PluginCommands.State.ApplyAction(plugin, {
      state: plugin.state.data,
      action: UpdateTrajectory.create({ action: 'advance', by }),
    });
    plugin.handleResize();
    return getTrajectoryModelState();
  };

  const getCurrentSelectionLoci = (): StructureElement.Loci | undefined => {
    const structure = getCurrentStructure();
    if (!structure) return undefined;
    const loci = plugin.managers.structure.selection.getLoci(structure);
    return StructureElement.Loci.is(loci) ? loci : undefined;
  };

  const getSelectionSummary = (): MolstarSelectionSummary => summarizeMolstarSelection(getCurrentSelectionLoci());
  const getSelectedLigandResidueName = (): string | undefined => {
    const selection = getCurrentSelectionLoci();
    const structure = getCurrentStructure();
    if (!selection || !structure || StructureElement.Loci.isEmpty(selection)) return selectedDockingLigandResidueName;
    const ligandLoci = StructureQuery.loci(StructureSelectionQueries.ligand.query, structure);
    const selectedLigand = StructureElement.Loci.intersect(selection, ligandLoci);
    if (
      StructureElement.Loci.isEmpty(selectedLigand) ||
      StructureElement.Loci.size(selectedLigand) !== StructureElement.Loci.size(selection)
    )
      return undefined;
    const residueNames = new Set<string>();
    for (const element of selectedLigand.elements) {
      if (!Unit.isAtomic(element.unit)) return undefined;
      OrderedSet.forEach(element.indices, (index) => {
        const atomIndex = element.unit.elements[index];
        const hierarchy = element.unit.model.atomicHierarchy;
        const residueName =
          hierarchy.atoms.auth_comp_id.value(atomIndex).trim() || hierarchy.atoms.label_comp_id.value(atomIndex).trim();
        if (residueName) residueNames.add(residueName.toUpperCase());
      });
    }
    return residueNames.size === 1 ? [...residueNames][0] : undefined;
  };
  const notifySelectionChange = () => onSelectionChange?.(getSelectionSummary());
  const selectionSubscription = plugin.managers.structure.selection.events.changed.subscribe(notifySelectionChange);

  const resolvePickedLoci = (point: MolstarScreenPoint): StructureElement.Loci | undefined => {
    const canvas = plugin.canvas3d;
    const structure = getCurrentStructure();
    if (!canvas || !structure) return undefined;

    const canvasElement = host.querySelector('canvas');
    const bounds = canvasElement?.getBoundingClientRect() ?? host.getBoundingClientRect();
    const x = Math.round(point.x - bounds.left);
    const y = Math.round(point.y - bounds.top);
    if (x < 0 || y < 0 || x > bounds.width || y > bounds.height) return undefined;

    const pickData = canvas.identify(Vec2.create(x, y));
    const pickedLoci = normalizeStructureElementClickLoci(canvas.getLoci(pickData?.id));
    if (!pickedLoci || StructureElement.Loci.isEmpty(pickedLoci)) return undefined;
    return pickedLoci.structure === structure ? pickedLoci : StructureElement.Loci.remap(pickedLoci, structure);
  };

  const applySelectionLoci = (loci: StructureElement.Loci | undefined, additive: boolean): MolstarSelectionSummary => {
    if (!loci || StructureElement.Loci.isEmpty(loci)) {
      if (!additive) plugin.managers.interactivity.lociSelects.deselectAll();
      const summary = getSelectionSummary();
      onSelectionChange?.(summary);
      return summary;
    }

    const representationLoci = { loci };
    if (additive) {
      plugin.managers.interactivity.lociSelects.selectJoin(representationLoci, false);
    } else {
      plugin.managers.interactivity.lociSelects.selectOnly(representationLoci, false);
    }
    const summary = getSelectionSummary();
    onSelectionChange?.(summary);
    return summary;
  };

  const selectScreenPoint = (point: MolstarScreenPoint, additive = false): MolstarSelectionSummary => {
    return applySelectionLoci(resolvePickedLoci(point), additive);
  };

  const selectScreenRectangle = (rectangle: MolstarScreenRectangle, additive = false): MolstarSelectionSummary => {
    const structure = getCurrentStructure();
    if (!structure) return EMPTY_SELECTION_SUMMARY;

    const canvas = plugin.canvas3d;
    const canvasElement = host.querySelector('canvas');
    if (!canvas || !canvasElement) return EMPTY_SELECTION_SUMMARY;

    const bounds = canvasElement.getBoundingClientRect();
    const pixelRatioX = canvasElement.width > 0 && bounds.width > 0 ? canvasElement.width / bounds.width : 1;
    const pixelRatioY = canvasElement.height > 0 && bounds.height > 0 ? canvasElement.height / bounds.height : 1;
    const left = Math.min(rectangle.left, rectangle.left + rectangle.width);
    const right = Math.max(rectangle.left, rectangle.left + rectangle.width);
    const top = Math.min(rectangle.top, rectangle.top + rectangle.height);
    const bottom = Math.max(rectangle.top, rectangle.top + rectangle.height);
    const projected = Vec4();
    const position = Vec3();
    const selectedElements: Array<{
      unit: Unit.Atomic;
      indices: OrderedSet<StructureElement.UnitIndex>;
    }> = [];

    for (const unit of structure.units) {
      if (!Unit.isAtomic(unit)) continue;
      const selectedIndices: StructureElement.UnitIndex[] = [];
      for (let index = 0; index < unit.elements.length; index += 1) {
        resolveAtomicWorldPosition(unit, index, position);
        canvas.camera.project(projected, position);

        const screenX = bounds.left + projected[0] / pixelRatioX;
        const screenY = bounds.top + (canvasElement.height - projected[1]) / pixelRatioY;
        if (screenX >= left && screenX <= right && screenY >= top && screenY <= bottom) {
          selectedIndices.push(index as StructureElement.UnitIndex);
        }
      }

      if (selectedIndices.length > 0) {
        selectedElements.push({
          unit,
          indices: OrderedSet.ofSortedArray(selectedIndices),
        });
      }
    }

    return applySelectionLoci(StructureElement.Loci(structure, selectedElements), additive);
  };

  const clearSelection = (): MolstarSelectionSummary => {
    selectedDockingLigandResidueName = undefined;
    plugin.managers.interactivity.lociSelects.deselectAll();
    const summary = getSelectionSummary();
    onSelectionChange?.(summary);
    return summary;
  };

  const clearPocketFocus = async () => {
    await applyRepresentationPreset('auto');
  };

  const removeDockingProjectionRef = async (ref: string | undefined) => {
    if (!ref || !plugin.state.data.cells.has(ref)) return;
    const update = plugin.state.data.build();
    update.delete(ref);
    await update.commit();
  };

  const setDockingRefHidden = (ref: string, hidden: boolean) => {
    const cell = plugin.state.data.cells.get(ref);
    if (!cell || cell.state.isHidden === hidden) return;
    setSubtreeVisibility(plugin.state.data, ref, hidden);
  };

  const applyDockingProteinVisibility = (projectionScope = activeDockingProjectionScope): void => {
    const refs = new Set<string>();
    if (fixedDockingProteinRef) refs.add(fixedDockingProteinRef);
    DOCKING_PROTEIN_VISIBILITY_TAGS.forEach((tag) => {
      plugin.state.data
        .select(StateSelection.Generators.root.subtree().withTag(tag))
        .forEach((cell) => refs.add(cell.transform.ref));
    });
    refs.forEach((ref) =>
      setDockingRefHidden(
        ref,
        ref === fixedDockingProteinRef
          ? isDockingFixedProteinHidden(dockingProteinVisible, projectionScope)
          : !dockingProteinVisible
      )
    );
    setPocketInteractionRenderVisibility(pocketInteractionStrengthLabelCount);
    host.dataset.synonDockingProteinVisible = String(dockingProteinVisible);
    host.dataset.synonPocketInteractionVisible = String(dockingProteinVisible);
  };

  const setDockingProteinVisible = (visible: boolean): void => {
    dockingProteinVisible = visible;
    applyDockingProteinVisibility();
    plugin.canvas3d?.syncVisibility();
    plugin.canvas3d?.commit(true);
    plugin.canvas3d?.requestDraw();
  };

  const waitForDockingSwapFrame = () =>
    new Promise<void>((resolve) => {
      const requestFrame = host.ownerDocument.defaultView?.requestAnimationFrame.bind(host.ownerDocument.defaultView);
      if (!requestFrame) {
        resolve();
        return;
      }
      requestFrame(() => requestFrame(() => resolve()));
    });

  const createDockingLigandLoci = (residueNames: readonly string[]): StructureElement.Loci => {
    const baseStructure = dockingBaseStructure?.cell.obj?.data;
    if (!baseStructure) throw new Error('MOLSTAR_DOCKING_BASE_MISSING');

    const allLigands = StructureQuery.loci(StructureSelectionQueries.ligand.query, baseStructure);
    const selectedLigands = filterStructureLociByResidueNames(allLigands, residueNames);
    if (StructureElement.Loci.isEmpty(selectedLigands)) {
      throw new Error(`MOLSTAR_DOCKING_LIGAND_MISSING:${residueNames.join(',')}`);
    }
    return selectedLigands;
  };

  const focusDockingLigands = (residueNames: readonly string[], durationMs: number): void => {
    const visibleStructure = activeDockingStructure;
    const visibleLigands = visibleStructure
      ? filterStructureLociByResidueNames(
          StructureQuery.loci(StructureSelectionQueries.ligand.query, visibleStructure),
          residueNames
        )
      : undefined;
    const ligandLoci =
      visibleLigands && !StructureElement.Loci.isEmpty(visibleLigands)
        ? visibleLigands
        : createDockingLigandLoci(residueNames);
    const ligandSphere = StructureElement.Loci.getBoundingSphere(ligandLoci);
    if (ligandSphere) {
      plugin.managers.camera.focusSphere(ligandSphere, {
        durationMs,
        extraRadius: 2.25,
        minRadius: 5.5,
      });
      return;
    }
    plugin.managers.camera.focusLoci(ligandLoci, {
      durationMs,
      extraRadius: 2.25,
      minRadius: 5.5,
    });
  };

  const createDockingProjectionLoci = (
    residueNames: readonly string[],
    expandRadius: number,
    scope: 'pocket' | 'structure'
  ): StructureElement.Loci => {
    const baseStructure = dockingBaseStructure?.cell.obj?.data;
    if (!baseStructure) throw new Error('MOLSTAR_DOCKING_BASE_MISSING');
    const selectedLigands = createDockingLigandLoci(residueNames);
    if (scope === 'structure') {
      const protein = StructureQuery.loci(StructureSelectionQueries.protein.query, baseStructure);
      return StructureElement.Loci.union(protein, selectedLigands);
    }

    const ligandExpression = StructureElement.Bundle.toExpression(StructureElement.Bundle.fromLoci(selectedLigands));
    const boundedExpandRadius = Math.max(3, Math.min(8, expandRadius));
    const nearbyProteinQuery = StructureSelectionQuery(
      'Docking pocket protein',
      MS.struct.modifier.intersectBy({
        0: MS.struct.modifier.includeSurroundings({
          0: ligandExpression,
          radius: boundedExpandRadius,
          'as-whole-residues': true,
        }),
        by: StructureSelectionQueries.protein.expression,
      })
    );
    const nearbyProtein = StructureQuery.loci(nearbyProteinQuery.query, baseStructure);
    return StructureElement.Loci.union(nearbyProtein, selectedLigands);
  };

  const hasDockingLigandVisual = (
    refs: DockingLigandShapeProviderRefs | undefined
  ): refs is DockingLigandShapeProviderRefs =>
    Boolean(
      refs &&
      plugin.state.data.cells.has(refs.atoms) &&
      plugin.state.data.cells.has(refs.atomRepresentation) &&
      plugin.state.data.cells.has(refs.bonds) &&
      plugin.state.data.cells.has(refs.bondRepresentation)
    );

  // Each ligand owns one persistent custom-shape graph. Selection changes only
  // reveal or hide those cached graphs; they never rebuild every selected
  // ligand into one combined geometry on the click path.
  const prepareDockingLigandVisual = async (
    activeLigandLoci: StructureElement.Loci,
    color: Color,
    reusableRefs?: DockingLigandShapeProviderRefs
  ): Promise<DockingLigandShapeProviderRefs> => {
    const data = createDockingLigandShapeData(activeLigandLoci, color);
    if (hasDockingLigandVisual(reusableRefs)) {
      const atomProvider = plugin.state.data.cells.get(reusableRefs.atoms)?.obj;
      const atomRepresentation = plugin.state.data.cells.get(reusableRefs.atomRepresentation)?.obj;
      const bondProvider = plugin.state.data.cells.get(reusableRefs.bonds)?.obj;
      const bondRepresentation = plugin.state.data.cells.get(reusableRefs.bondRepresentation)?.obj;
      if (
        PluginStateObject.Shape.Provider.is(atomProvider) &&
        PluginStateObject.Shape.Representation3D.is(atomRepresentation) &&
        PluginStateObject.Shape.Provider.is(bondProvider) &&
        PluginStateObject.Shape.Representation3D.is(bondRepresentation)
      ) {
        atomProvider.data.data = data;
        bondProvider.data.data = data;
        await Promise.all([
          atomRepresentation.data.repr.createOrUpdate({}, data).run(),
          bondRepresentation.data.repr.createOrUpdate({}, data).run(),
        ]);
        return reusableRefs;
      }
    }
    if (reusableRefs) throw new Error('MOLSTAR_DOCKING_LIGAND_SHAPE_STATE_MISSING');

    const update = plugin.state.data.build();
    const atomProvider = update
      .toRoot()
      .apply(SynonBiomedDockingLigandAtoms, { data }, { tags: 'synon-biomed-docking-ligand-atoms' });
    const atomRepresentation = atomProvider.apply(
      StateTransforms.Representation.ShapeRepresentation3D,
      { quality: 'medium' },
      { tags: 'synon-biomed-docking-ligand-atoms-representation' }
    );
    const bondProvider = update
      .toRoot()
      .apply(SynonBiomedDockingLigandBonds, { data }, { tags: 'synon-biomed-docking-ligand-bonds' });
    const bondRepresentation = bondProvider.apply(
      StateTransforms.Representation.ShapeRepresentation3D,
      { quality: 'medium' },
      { tags: 'synon-biomed-docking-ligand-bonds-representation' }
    );
    await update.commit();
    const refs = {
      atoms: atomProvider.ref,
      atomRepresentation: atomRepresentation.ref,
      bonds: bondProvider.ref,
      bondRepresentation: bondRepresentation.ref,
    };
    setSubtreeVisibility(plugin.state.data, atomProvider.ref, true);
    setSubtreeVisibility(plugin.state.data, bondProvider.ref, true);
    return refs;
  };

  type PreparedDockingLigand = {
    componentRefs: string[];
    ligandLoci: StructureElement.Loci;
    residueNames: string[];
  };

  const prepareDockingLigandUpdate = async (
    residueNames: readonly string[],
    color: Color,
    colors: readonly Color[] = []
  ): Promise<PreparedDockingLigand> => {
    const clock = host.ownerDocument.defaultView?.performance;
    const startedAt = clock?.now() ?? 0;
    let cacheHits = 0;
    let graphUpdates = 0;
    const uniqueResidueNames = [...new Set(residueNames)];
    const ligandLoci = createDockingLigandLoci(uniqueResidueNames);
    const providerRefs = await Promise.all(
      uniqueResidueNames.map(async (residueName, index) => {
        const key = residueName.toUpperCase();
        const nextColor = colors[index] ?? color;
        const cached = dockingLigandVisualCache.get(key);
        const cachedGraphAvailable = Boolean(cached && hasDockingLigandVisual(cached.refs));
        if (shouldReuseDockingLigandVisual(cached?.color, nextColor, cachedGraphAvailable)) {
          cacheHits += 1;
          return cached!.refs;
        }
        graphUpdates += 1;
        const residueLoci = createDockingLigandLoci([residueName]);
        const refs = await prepareDockingLigandVisual(
          residueLoci,
          nextColor,
          cached && hasDockingLigandVisual(cached.refs) ? cached.refs : undefined
        );
        dockingLigandVisualCache.set(key, { refs, color: nextColor });
        return refs;
      })
    );
    const preparedAt = clock?.now() ?? startedAt;
    host.dataset.synonDockingLigandPrepareProfile = JSON.stringify({
      requested: uniqueResidueNames.length,
      cacheHits,
      graphUpdates,
      totalMs: preparedAt - startedAt,
    });
    return {
      componentRefs: providerRefs.flatMap((refs) => [refs.atoms, refs.bonds]),
      ligandLoci,
      residueNames: uniqueResidueNames,
    };
  };

  const commitDockingLigandUpdate = (prepared: PreparedDockingLigand, visible: boolean): MolstarPocketSummary => {
    selectedDockingLigandResidueName = undefined;
    plugin.managers.interactivity.lociSelects.deselectAll();
    onSelectionChange?.(getSelectionSummary());
    const previousLigandRefs = activeDockingLigandRefs;
    activeDockingLigandRefs = prepared.componentRefs;
    const nextLigandRefs = new Set(activeDockingLigandRefs);
    applyDockingVisibilitySwap(
      previousLigandRefs.filter((ref) => !nextLigandRefs.has(ref)),
      visible ? activeDockingLigandRefs : [],
      setDockingRefHidden
    );
    if (!visible) activeDockingLigandRefs.forEach((ref) => setDockingRefHidden(ref, true));

    activeDockingStructure = StructureElement.Loci.toStructure(prepared.ligandLoci);
    host.dataset.synonDockingLigandCount = String(prepared.residueNames.length);
    host.dataset.synonDockingActiveResidues = prepared.residueNames.join(',');
    return {
      hasLigand: !StructureElement.Loci.isEmpty(prepared.ligandLoci),
      ligandCount: StructureElement.Loci.size(prepared.ligandLoci),
      residueCount: 0,
      distanceCount: 0,
    };
  };

  const updateActiveDockingLigand = async (
    residueNames: readonly string[],
    color = POCKET_COLORS.ligandCarbon
  ): Promise<MolstarPocketSummary> => {
    const clock = host.ownerDocument.defaultView?.performance;
    const startedAt = clock?.now() ?? 0;
    const baseStructure = dockingBaseStructure;
    if (!baseStructure) throw new Error('MOLSTAR_DOCKING_BASE_MISSING');
    const prepared = await prepareDockingLigandUpdate(residueNames, color);
    const preparedAt = clock?.now() ?? startedAt;
    const summary = commitDockingLigandUpdate(prepared, true);
    const visibilityAt = clock?.now() ?? preparedAt;
    plugin.canvas3d?.syncVisibility();
    const syncedAt = clock?.now() ?? visibilityAt;
    plugin.canvas3d?.requestDraw();
    const committedAt = clock?.now() ?? syncedAt;

    host.dataset.synonDockingSwitchProfile = JSON.stringify({
      prepareMs: preparedAt - startedAt,
      visibilityMs: visibilityAt - preparedAt,
      syncMs: syncedAt - visibilityAt,
      commitMs: committedAt - syncedAt,
      totalMs: committedAt - startedAt,
    });
    return summary;
  };

  const activateDockingOverview = (residueNames: readonly string[], focusCamera: boolean): void => {
    const previousProjectionRef = activeDockingProjectionRef;
    const previousDistanceRefs = [...activeDockingDistanceRefs];
    activeDockingProjectionScope = 'overview';
    applyDockingVisibilitySwap(
      [...previousDistanceRefs, ...(previousProjectionRef ? [previousProjectionRef] : [])],
      [
        ...(dockingProteinVisible && fixedDockingProteinRef ? [fixedDockingProteinRef] : []),
        ...activeDockingLigandRefs,
      ],
      setDockingRefHidden
    );
    applyDockingProteinVisibility();
    host.dataset.synonDockingProjectionScope = 'overview';
    host.dataset.synonDockingRefinement = 'manual';
    plugin.canvas3d?.syncVisibility();
    plugin.canvas3d?.requestDraw();

    if (focusCamera) {
      focusDockingLigands(residueNames, 180);
    }
    // Keep one hidden refined graph as a reusable retirement target. The next
    // explicit Pocket action replaces and deletes it in the existing atomic
    // swap. Deleting it during an ordinary pose switch caused another visible
    // main-thread pause even though the user had already left pocket mode.
  };

  const replaceDockingProjection = async (
    residueNames: readonly string[],
    expandRadius: number,
    scope: 'pocket' | 'structure',
    project: (structure: StructureRef) => Promise<MolstarPocketSummary>,
    keepPersistentLigandVisible: boolean
  ): Promise<MolstarPocketSummary> => {
    const baseStructure = dockingBaseStructure;
    if (!baseStructure) throw new Error('MOLSTAR_DOCKING_BASE_MISSING');

    const projectionLoci = createDockingProjectionLoci(residueNames, expandRadius, scope);
    const projectionQuery = StructureSelectionQuery(
      scope === 'pocket' ? 'Active docking pocket' : 'Active docking view',
      StructureElement.Bundle.toExpression(StructureElement.Bundle.fromLoci(projectionLoci))
    );
    const projectionGeneration = ++dockingProjectionGeneration;
    const projection = await plugin.builders.structure.tryCreateComponentFromSelection(
      baseStructure.cell,
      projectionQuery,
      `docking-projection-${projectionGeneration}`,
      {
        label: scope === 'pocket' ? 'Active docking pocket' : 'Active docking view',
      }
    );
    if (!projection?.cell || !projection.obj) {
      throw new Error('MOLSTAR_DOCKING_PROJECTION_MISSING');
    }

    const projectionRef = projection.ref;
    setSubtreeVisibility(plugin.state.data, projectionRef, true);
    const previousDistanceRefs = [...activeDockingDistanceRefs];
    const previousDistanceRefSet = new Set(previousDistanceRefs);
    const targetStructure = {
      kind: 'structure',
      cell: projection.cell,
      version: projection.cell.transform.version,
      components: [],
    } as unknown as StructureRef;

    try {
      const summary = await project(targetStructure);
      const nextDistanceRefs = getPocketDistanceRootRefs().filter((ref) => !previousDistanceRefSet.has(ref));
      if (projectionGeneration !== dockingProjectionGeneration) {
        await removePocketDistanceRefs(nextDistanceRefs);
        await removeDockingProjectionRef(projectionRef);
        return summary;
      }
      applyDockingVisibilitySwap(
        [
          ...(activeDockingProjectionRef ? [activeDockingProjectionRef] : []),
          ...previousDistanceRefs,
          ...(scope === 'structure' && fixedDockingProteinRef ? [fixedDockingProteinRef] : []),
          ...(!keepPersistentLigandVisible ? activeDockingLigandRefs : []),
        ],
        [
          projectionRef,
          ...nextDistanceRefs,
          ...(scope === 'pocket' && fixedDockingProteinRef ? [fixedDockingProteinRef] : []),
          ...(keepPersistentLigandVisible ? activeDockingLigandRefs : []),
        ],
        setDockingRefHidden
      );
      applyDockingProteinVisibility(scope);
      plugin.canvas3d?.syncVisibility();
      plugin.canvas3d?.commit(true);
      plugin.canvas3d?.requestDraw();

      const previousProjectionRef = activeDockingProjectionRef;
      activeDockingProjectionRef = projectionRef;
      activeDockingProjectionScope = scope;
      activeDockingDistanceRefs = nextDistanceRefs;
      activeDockingStructure = projection.obj.data;
      host.dataset.synonDockingLigandCount = String(new Set(residueNames).size);
      host.dataset.synonDockingActiveResidues = [...new Set(residueNames)].join(',');
      host.dataset.synonDockingProjectionScope = scope;
      host.dataset.synonDockingRefinement = 'ready';

      await waitForDockingSwapFrame();
      await removePocketDistanceRefs(previousDistanceRefs);
      await removeDockingProjectionRef(previousProjectionRef);
      return summary;
    } catch (reason) {
      const failedDistanceRefs = getPocketDistanceRootRefs().filter((ref) => !previousDistanceRefSet.has(ref));
      await removePocketDistanceRefs(failedDistanceRefs);
      await removeDockingProjectionRef(projectionRef);
      throw reason;
    }
  };

  const applyDockingLayers = async (
    structureRef: StructureRef,
    requestedLayers: readonly MolstarViewLayer[],
    ligandResidueNames: readonly string[]
  ): Promise<MolstarPocketSummary> => {
    const structure = structureRef.cell.obj?.data;
    if (!structure) throw new Error('MOLSTAR_DOCKING_VIEW_MISSING');

    const allLigandLoci = StructureQuery.loci(StructureSelectionQueries.ligand.query, structure);
    const ligandLoci = ligandResidueNames.length
      ? filterStructureLociByResidueNames(allLigandLoci, ligandResidueNames)
      : allLigandLoci;
    if (ligandResidueNames.length > 0 && StructureElement.Loci.isEmpty(ligandLoci)) {
      throw new Error(`MOLSTAR_DOCKING_LIGAND_MISSING:${ligandResidueNames.join(',')}`);
    }

    const layers = [...new Set(requestedLayers)];
    if (layers.length === 0) {
      const protein = await presetStaticComponent(plugin, structureRef.cell, 'protein', {
        label: 'Initial protein',
      });
      if (protein) {
        await plugin.builders.structure.representation.addRepresentation(
          protein,
          {
            type: 'cartoon',
            typeParams: {
              sizeFactor: 0.2,
              helixProfile: 'rounded',
              alpha: 0.62,
            },
            color: 'chain-id',
          },
          { tag: 'synon-biomed-layer-initial-protein' }
        );
      }
      return {
        hasLigand: !StructureElement.Loci.isEmpty(ligandLoci),
        ligandCount: StructureElement.Loci.size(ligandLoci),
        residueCount: 0,
        distanceCount: 0,
      };
    }

    const hasBallAndStick = layers.includes('ball-and-stick');
    const hasLine = layers.includes('line');
    const hasProteinSurface = layers.includes('surface');
    const hasPocketSurface = layers.includes('pocket-surface');
    const hasLigandSurface = layers.includes('ligand-surface');
    const hasAtomLayer = hasBallAndStick || hasLine;
    const ligandExpression = StructureElement.Bundle.toExpression(StructureElement.Bundle.fromLoci(ligandLoci));
    const structureCell = structureRef.cell;
    const ligandSurfacePlans = hasLigandSurface
      ? resolveDockingLigandElectrostaticSurfaces(ligandResidueNames, electrostaticVolumes)
      : [];
    if ((hasProteinSurface || hasPocketSurface) && !electrostaticVolumes.protein) {
      throw new Error('MOLSTAR_ELECTROSTATIC_PROTEIN_VOLUME_MISSING');
    }

    const all = hasAtomLayer
      ? await presetStaticComponent(plugin, structureCell, 'protein', {
          label: 'Layered protein atoms',
        })
      : undefined;
    const protein =
      hasProteinSurface || (hasLigandSurface && !hasAtomLayer)
        ? await presetStaticComponent(plugin, structureCell, 'protein', {
            label: 'Layered protein',
          })
        : undefined;
    const pocket = hasPocketSurface
      ? await plugin.builders.structure.tryCreateComponentFromSelection(
          structureCell,
          StructureSelectionQuery(
            'Layered pocket surface',
            MS.struct.modifier.intersectBy({
              0: MS.struct.modifier.includeSurroundings({
                0: ligandExpression,
                radius: DEFAULT_POCKET_EXPAND_RADIUS,
                'as-whole-residues': true,
              }),
              by: StructureSelectionQueries.protein.expression,
            })
          ),
          'synon-biomed-layered-pocket-surface',
          { label: 'Layered pocket surface' }
        )
      : undefined;
    const ligandSurfaceComponents = await Promise.all(
      ligandSurfacePlans.map(async ({ residueName }, index) => {
        const selected = filterStructureLociByResidueName(ligandLoci, residueName);
        if (StructureElement.Loci.isEmpty(selected)) {
          throw new Error(`MOLSTAR_DOCKING_LIGAND_MISSING:${residueName}`);
        }
        const expression = StructureElement.Bundle.toExpression(StructureElement.Bundle.fromLoci(selected));
        const component = await plugin.builders.structure.tryCreateComponentFromSelection(
          structureCell,
          StructureSelectionQuery(`Layered ligand surface ${residueName}`, expression),
          `synon-biomed-layered-ligand-surface-${index}`,
          { label: `Layered ligand surface ${residueName}` }
        );
        if (!component) throw new Error(`MOLSTAR_DOCKING_LIGAND_MISSING:${residueName}`);
        return component;
      })
    );

    if (hasBallAndStick && all) {
      await plugin.builders.structure.representation.addRepresentation(
        all,
        {
          type: 'ball-and-stick',
          typeParams: {
            sizeFactor: 0.38,
            sizeAspectRatio: 0.72,
            ignoreHydrogens: true,
            ignoreHydrogensVariant: 'all',
          },
          color: 'element-symbol',
        },
        { tag: 'synon-biomed-layer-ball-and-stick' }
      );
    }
    if (hasLine && all) {
      await plugin.builders.structure.representation.addRepresentation(
        all,
        {
          type: 'line',
          typeParams: {
            sizeFactor: 0.7,
            lineSizeAttenuation: false,
            ignoreHydrogens: true,
            ignoreHydrogensVariant: 'all',
          },
          color: 'element-symbol',
        },
        { tag: 'synon-biomed-layer-line' }
      );
    }

    const addElectrostaticSurface = async (
      component: typeof protein,
      tag: string,
      volume: MolstarElectrostaticVolume | undefined
    ) => {
      if (!component) return;
      if (!volume) throw new Error('MOLSTAR_ELECTROSTATIC_VOLUME_MISSING');
      await plugin.builders.structure.representation.addRepresentation(
        component,
        {
          type: 'molecular-surface',
          typeParams: ELECTROSTATIC_SURFACE_TYPE_PARAMS,
          ...resolveSurfaceColorSettings(component.obj?.data ?? structure, volume),
        },
        { tag }
      );
    };

    if (hasProteinSurface) {
      await addElectrostaticSurface(protein, 'synon-biomed-layer-protein-surface', electrostaticVolumes.protein);
    }
    if (hasPocketSurface) {
      await addElectrostaticSurface(pocket, 'synon-biomed-layer-pocket-surface', electrostaticVolumes.protein);
    }
    await Promise.all(
      ligandSurfacePlans.map((plan, index) =>
        addElectrostaticSurface(
          ligandSurfaceComponents[index],
          `synon-biomed-layer-ligand-surface-${index}`,
          plan.volume
        )
      )
    );

    if (!hasAtomLayer && hasLigandSurface && !hasProteinSurface && !hasPocketSurface && protein) {
      await plugin.builders.structure.representation.addRepresentation(
        protein,
        {
          type: 'cartoon',
          typeParams: { alpha: 0.24, sizeFactor: 0.16 },
          color: 'uniform',
          colorParams: { value: POCKET_COLORS.outsideProtein },
        },
        { tag: 'synon-biomed-layer-protein-context' }
      );
    }

    return {
      hasLigand: !StructureElement.Loci.isEmpty(ligandLoci),
      ligandCount: StructureElement.Loci.size(ligandLoci),
      residueCount: 0,
      distanceCount: 0,
    };
  };

  const applyDockingPocket = async (
    ligandResidueName: string,
    options: MolstarPocketOptions
  ): Promise<MolstarPocketSummary> => {
    const preparedLigand = await prepareDockingLigandUpdate(
      [ligandResidueName],
      options.ligandColor ?? POCKET_COLORS.ligandCarbon
    );
    const summary = commitDockingLigandUpdate(preparedLigand, true);
    host.dataset.synonDockingSwitchComplete = ligandResidueName;
    activateDockingOverview([ligandResidueName], false);
    if (shouldFocusPocketCamera(options)) focusDockingLigands([ligandResidueName], 120);
    const projectionScope = resolveDockingProjectionScope(options.displayLayers ?? []);
    const refinedSummary = await latestDockingRefinement.queue(
      () =>
        replaceDockingProjection(
          [ligandResidueName],
          options.expandRadius,
          projectionScope,
          (structure) => applyPocketFocus({ ...options, focusCamera: false }, undefined, structure, false),
          true
        ),
      (state, reason) => {
        host.dataset.synonDockingRefinement = state;
        if (state === 'failed') {
          plugin.log.error(`Failed to refine the active docking pocket: ${String(reason)}`);
        }
      }
    );
    return refinedSummary ?? summary;
  };

  const clearDockingPocket = async (ligandResidueNames: readonly string[]): Promise<MolstarPocketSummary> => {
    cancelDockingRefinement();
    const ligandLoci = createDockingLigandLoci(ligandResidueNames);
    activateDockingOverview(ligandResidueNames, false);
    return {
      hasLigand: !StructureElement.Loci.isEmpty(ligandLoci),
      ligandCount: StructureElement.Loci.size(ligandLoci),
      residueCount: 0,
      distanceCount: 0,
    };
  };

  const replaceDockingComparison = async (
    primaryResidueName: string,
    secondaryResidueName: string,
    options: MolstarLigandComparisonOptions
  ): Promise<MolstarPocketSummary> => {
    const residueNames = [primaryResidueName, secondaryResidueName];
    const colors = [options.primaryColor, options.secondaryColor];
    const preparedLigand = await prepareDockingLigandUpdate(residueNames, options.primaryColor, colors);
    const summary = commitDockingLigandUpdate(preparedLigand, true);
    host.dataset.synonDockingSwitchComplete = residueNames.join(',');
    activateDockingOverview(residueNames, false);
    if (shouldFocusPocketCamera(options)) focusDockingLigands(residueNames, 120);
    const projectionScope = resolveDockingProjectionScope(options.displayLayers ?? []);
    const refinedSummary = await latestDockingRefinement.queue(
      () =>
        replaceDockingProjection(
          residueNames,
          options.expandRadius,
          projectionScope,
          (structure) => {
            const selectedLoci = StructureElement.Loci.remap(
              createDockingLigandLoci(residueNames),
              structure.cell.obj!.data
            );
            return applyPocketFocus({ ...options, focusCamera: false }, selectedLoci, structure, false);
          },
          true
        ),
      (state, reason) => {
        host.dataset.synonDockingRefinement = state;
        if (state === 'failed') {
          plugin.log.error(`Failed to refine the docking comparison: ${String(reason)}`);
        }
      }
    );
    return refinedSummary ?? summary;
  };

  const replaceDockingSelection = async (
    ligandResidueNames: readonly string[],
    options: MolstarPocketOptions
  ): Promise<MolstarPocketSummary> => {
    const uniqueResidueNames = [...new Set(ligandResidueNames)];
    if (uniqueResidueNames.length === 0) {
      return {
        hasLigand: false,
        ligandCount: 0,
        residueCount: 0,
        distanceCount: 0,
      };
    }
    const preparedLigand = await prepareDockingLigandUpdate(
      uniqueResidueNames,
      options.ligandColor ?? POCKET_COLORS.ligandCarbon,
      options.ligandColors
    );
    const summary = commitDockingLigandUpdate(preparedLigand, true);
    host.dataset.synonDockingSwitchComplete = uniqueResidueNames.join(',');
    activateDockingOverview(uniqueResidueNames, false);
    if (shouldFocusPocketCamera(options)) focusDockingLigands(uniqueResidueNames, 120);
    const projectionScope = resolveDockingProjectionScope(options.displayLayers ?? []);
    const refinedSummary = await latestDockingRefinement.queue(
      () =>
        replaceDockingProjection(
          uniqueResidueNames,
          options.expandRadius,
          projectionScope,
          async (structure) => {
            const selectedLoci = StructureElement.Loci.remap(
              createDockingLigandLoci(uniqueResidueNames),
              structure.cell.obj!.data
            );
            return applyPocketFocus({ ...options, focusCamera: false }, selectedLoci, structure, false);
          },
          true
        ),
      (state, reason) => {
        host.dataset.synonDockingRefinement = state;
        if (state === 'failed') {
          plugin.log.error(`Failed to refine the docking selection: ${String(reason)}`);
        }
      }
    );
    return refinedSummary ?? summary;
  };

  const clearDockingSelection = async (): Promise<MolstarPocketSummary> => {
    cancelDockingRefinement();
    applyDockingVisibilitySwap(
      [
        ...(activeDockingProjectionRef ? [activeDockingProjectionRef] : []),
        ...activeDockingDistanceRefs,
        ...activeDockingLigandRefs,
      ],
      dockingProteinVisible && fixedDockingProteinRef ? [fixedDockingProteinRef] : [],
      setDockingRefHidden
    );
    activeDockingProjectionRef = undefined;
    activeDockingProjectionScope = 'overview';
    activeDockingDistanceRefs = [];
    activeDockingStructure = dockingBaseStructure?.cell.obj?.data;
    host.dataset.synonDockingLigandCount = '0';
    host.dataset.synonDockingActiveResidues = '';
    host.dataset.synonDockingProjectionScope = 'overview';
    host.dataset.synonDockingRefinement = 'manual';
    applyDockingProteinVisibility();
    plugin.canvas3d?.syncVisibility();
    plugin.canvas3d?.commit(true);
    plugin.canvas3d?.requestDraw();
    return {
      hasLigand: false,
      ligandCount: 0,
      residueCount: 0,
      distanceCount: 0,
    };
  };

  const replaceDockingLayers = async (
    ligandResidueNames: readonly string[],
    layers: readonly MolstarViewLayer[],
    ligandColors: readonly Color[] = []
  ): Promise<MolstarPocketSummary> => {
    const preparedLigand = await prepareDockingLigandUpdate(
      ligandResidueNames,
      ligandColors[0] ?? POCKET_COLORS.ligandCarbon,
      ligandColors
    );
    const summary = commitDockingLigandUpdate(preparedLigand, true);
    activateDockingOverview(ligandResidueNames, false);
    host.dataset.synonDockingSwitchComplete = [...new Set(ligandResidueNames)].join(',');
    const refinedSummary = await latestDockingRefinement.queue(
      () =>
        replaceDockingProjection(
          ligandResidueNames,
          DEFAULT_POCKET_EXPAND_RADIUS,
          'structure',
          (structure) => applyDockingLayers(structure, layers, ligandResidueNames),
          true
        ),
      (state, reason) => {
        host.dataset.synonDockingRefinement = state;
        if (state === 'failed') {
          plugin.log.error(`Failed to refine the active docking layers: ${String(reason)}`);
        }
      }
    );
    return refinedSummary ?? summary;
  };

  const loadDockingEnsemble = async (
    source: string,
    filename: string,
    format: string,
    initialLigandResidueName: string,
    options: MolstarPocketOptions
  ): Promise<MolstarPocketSummary> => {
    loadedFormat = format;
    cancelDockingRefinement();
    await plugin.clear();
    electrostaticVolumes = {};
    delete host.dataset.synonDockingLigandCount;
    delete host.dataset.synonDockingProjectionScope;
    delete host.dataset.synonDockingActiveResidues;
    delete host.dataset.synonDockingRefinement;
    delete host.dataset.synonDockingSwitchComplete;
    delete host.dataset.synonDockingProteinVisible;
    delete host.dataset.synonPocketInteractionRenderMode;
    delete host.dataset.synonPocketInteractionStrengthCount;
    delete host.dataset.synonPocketInteractionVisible;
    dockingBaseStructure = undefined;
    fixedDockingProteinRef = undefined;
    dockingProteinVisible = true;
    activeDockingLigandRefs = [];
    dockingLigandVisualCache.clear();
    pocketInteractionStrengthProviderRef = undefined;
    pocketInteractionStrengthRepresentationRef = undefined;
    pocketInteractionStrengthLabelCount = 0;
    pocketInteractionAnchorProviderRef = undefined;
    activeDockingProjectionRef = undefined;
    activeDockingProjectionScope = 'overview';
    activeDockingStructure = undefined;
    activeDockingDistanceRefs = [];

    const raw = await plugin.builders.data.rawData({
      data: source,
      label: filename,
    });
    const trajectory = await plugin.builders.structure.parseTrajectory(raw, format as MolstarTrajectoryFormat);
    const model = await plugin.builders.structure.createModel(trajectory);
    const modelProperties = await plugin.builders.structure.insertModelProperties(model);
    const structureSelector = await plugin.builders.structure.createStructure(modelProperties || model, {
      name: 'model',
      params: {},
    });
    await plugin.builders.structure.insertStructureProperties(structureSelector);
    dockingBaseStructure = plugin.managers.structure.hierarchy.current.structures.find(
      (structure) => structure.cell.transform.ref === structureSelector.cell?.transform.ref
    );
    if (!dockingBaseStructure) {
      throw new Error('MOLSTAR_DOCKING_BASE_MISSING');
    }

    const fixedProtein = await plugin.builders.structure.tryCreateComponentFromSelection(
      dockingBaseStructure.cell,
      StructureSelectionQueries.protein,
      'docking-fixed-protein',
      { label: 'Fixed receptor' }
    );
    fixedDockingProteinRef = fixedProtein?.ref;
    if (fixedProtein) {
      await plugin.builders.structure.representation.addRepresentation(
        fixedProtein,
        {
          type: 'cartoon',
          typeParams: {
            sizeFactor: 0.17,
            helixProfile: 'rounded',
            alpha: 0.38,
          },
          color: 'uniform',
          colorParams: { value: POCKET_COLORS.outsideProtein },
        },
        { tag: 'synon-biomed-docking-fixed-protein' }
      );
    }

    const ligandColor = options.ligandColor ?? POCKET_COLORS.ligandCarbon;
    await updateActiveDockingLigand([initialLigandResidueName], ligandColor);
    // A docking ensemble represents binding modes, so its first committed
    // frame is the real pocket projection rather than an overview whose
    // camera merely happens to be centered on the ligand. The loading veil
    // remains active until this complete projection is ready.
    const summary = await replaceDockingProjection(
      [initialLigandResidueName],
      options.expandRadius,
      resolveDockingProjectionScope(options.displayLayers ?? []),
      (structure) => applyPocketFocus({ ...options, focusCamera: false }, undefined, structure, false),
      true
    );
    if (shouldFocusPocketCamera(options)) focusDockingLigands([initialLigandResidueName], 260);
    host.dataset.synonDockingSwitchComplete = initialLigandResidueName;
    plugin.handleResize();
    return summary;
  };

  plugin.selectionMode = true;
  return {
    plugin,
    load: async (source, filename, format) => {
      loadedFormat = format;
      structureObjectVisibility.protein = true;
      structureObjectVisibility.ligand = true;
      structureLigandColor = POCKET_COLORS.ligandCarbon;
      cancelDockingRefinement();
      await plugin.clear();
      electrostaticVolumes = {};
      delete host.dataset.synonDockingLigandCount;
      delete host.dataset.synonDockingProjectionScope;
      delete host.dataset.synonDockingActiveResidues;
      delete host.dataset.synonDockingRefinement;
      delete host.dataset.synonDockingSwitchComplete;
      delete host.dataset.synonDockingProteinVisible;
      delete host.dataset.synonPocketInteractionRenderMode;
      delete host.dataset.synonPocketInteractionStrengthCount;
      delete host.dataset.synonPocketInteractionVisible;
      dockingBaseStructure = undefined;
      fixedDockingProteinRef = undefined;
      activeDockingLigandRefs = [];
      dockingLigandVisualCache.clear();
      pocketInteractionStrengthProviderRef = undefined;
      pocketInteractionStrengthRepresentationRef = undefined;
      pocketInteractionStrengthLabelCount = 0;
      pocketInteractionAnchorProviderRef = undefined;
      activeDockingProjectionRef = undefined;
      activeDockingProjectionScope = 'overview';
      activeDockingStructure = undefined;
      activeDockingDistanceRefs = [];
      if (format === 'cube') {
        if (typeof source !== 'string') throw new Error('MOLSTAR_CUBE_REQUIRES_TEXT');
        const raw = await plugin.builders.data.rawData({
          data: source,
          label: filename,
        });
        const parsed = await CubeProvider.parse(plugin, raw);
        await CubeProvider.visuals(plugin, parsed);
      } else {
        const raw = await plugin.builders.data.rawData({
          data: source,
          label: filename,
        });
        const trajectory = await plugin.builders.structure.parseTrajectory(raw, format as MolstarTrajectoryFormat);
        const preset = await plugin.builders.structure.hierarchy.applyPreset(trajectory, 'default', {
          structure: { name: 'model', params: {} },
          showUnitcell: false,
          representationPreset: 'auto',
          representationPresetParams: INITIAL_REPRESENTATION_PRESET_PARAMS,
        });
        if (!preset || plugin.managers.structure.hierarchy.current.structures.length === 0) {
          throw new Error('MOLSTAR_STRUCTURE_MISSING');
        }
      }
      applyStructureObjectVisibility();
      await applyStructureLigandColor();
      plugin.handleResize();
      return getStructureComposition();
    },
    add: async (source, filename, format) => {
      if (format === 'cube') throw new Error('MOLSTAR_STRUCTURE_SCENE_CUBE_UNSUPPORTED');
      const raw = await plugin.builders.data.rawData({
        data: source,
        label: filename,
      });
      const trajectory = await plugin.builders.structure.parseTrajectory(raw, format as MolstarTrajectoryFormat);
      const preset = await plugin.builders.structure.hierarchy.applyPreset(trajectory, 'default', {
        structure: { name: 'model', params: {} },
        showUnitcell: false,
        representationPreset: 'auto',
        representationPresetParams: INITIAL_REPRESENTATION_PRESET_PARAMS,
      });
      if (!preset || plugin.managers.structure.hierarchy.current.structures.length === 0) {
        throw new Error('MOLSTAR_STRUCTURE_SCENE_LAYER_MISSING');
      }
      applyStructureObjectVisibility();
      await applyStructureLigandColor();
      plugin.handleResize();
      return getStructureComposition();
    },
    loadDockingEnsemble,
    resize: () => plugin.handleResize(),
    resetCamera: () => {
      plugin.canvas3d?.requestCameraReset({ durationMs: 180 });
      plugin.canvas3d?.commit(true);
    },
    clickNativeControl: (controlId) => {
      const button = host.querySelector<HTMLButtonElement>(`button[data-synon-molstar-control="${controlId}"]`);
      button?.click();
    },
    isNativeControlActive: (controlId) => {
      const button = host.querySelector<HTMLButtonElement>(`button[data-synon-molstar-control="${controlId}"]`);
      return button?.classList.contains('msp-btn-link-toggle-on') ?? false;
    },
    applyRepresentationPreset,
    applyRepresentationStyle,
    applyPocketFocus,
    setPocketInteractionStrengths,
    applyDockingPocket,
    clearDockingPocket,
    replaceDockingComparison,
    replaceDockingSelection,
    clearDockingSelection,
    cancelDockingRefinement,
    replaceDockingLayers,
    setDockingProteinVisible,
    clearPocketFocus,
    setSelectionMode: (enabled) => {
      plugin.selectionMode = enabled;
      if (!enabled) clearPendingClick();
    },
    selectScreenPoint,
    selectScreenRectangle,
    clearSelection,
    getSelectionSummary,
    getSelectedLigandResidueName,
    getTrajectoryModelState,
    getStructureComposition,
    getPrimaryLigandDepictionSource,
    getElectrostaticInputSource,
    setElectrostaticPotentials,
    setStructureObjectVisible,
    setStructureLigandColor,
    advanceTrajectoryModel,
    exportCurrentPose: ({ selectionOnly = false } = {}) => {
      const structure = getCurrentStructure();
      if (!structure) throw new Error('MOLSTAR_STRUCTURE_MISSING');
      const selection = getCurrentSelectionLoci();
      const useSelection = selectionOnly && Boolean(selection && !StructureElement.Loci.isEmpty(selection));
      const atoms = createPoseAtoms(structure, useSelection ? selection : undefined);
      if (atoms.length === 0) throw new Error('MOLSTAR_POSE_EMPTY');
      return {
        content: formatMolstarPosePdb(atoms),
        atomCount: atoms.length,
        selectionOnly: useSelection,
      };
    },
    setBackgroundColor: (color) => {
      plugin.canvas3d?.setProps({
        renderer: { backgroundColor: Color(color) },
      });
      plugin.canvas3d?.commit(true);
    },
    captureImage: async ({ width = 304, height = 184, backgroundColor = 0xf7f9fc } = {}) => {
      const screenshot = plugin.helpers.viewportScreenshot;
      const canvas = plugin.canvas3d;
      if (!screenshot || !canvas) throw new Error('MOLSTAR_SCREENSHOT_UNAVAILABLE');
      const boundedWidth = Math.max(128, Math.min(2048, Math.floor(width)));
      const boundedHeight = Math.max(128, Math.min(2048, Math.floor(height)));
      canvas.setProps({
        renderer: { backgroundColor: Color(backgroundColor) },
      });
      canvas.requestCameraReset({ durationMs: 0 });
      canvas.commit(true);
      screenshot.behaviors.values.next({
        ...screenshot.values,
        resolution: {
          name: 'custom',
          params: { width: boundedWidth, height: boundedHeight },
        },
        format: { name: 'png', params: {} },
        transparent: false,
        axes: { name: 'off', params: {} },
      });
      screenshot.behaviors.cropParams.next({
        auto: false,
        relativePadding: 0.1,
      });
      screenshot.resetCrop();
      return screenshot.getImageDataUri();
    },
    dispose: () => {
      if (disposed) return;
      disposed = true;
      clickSubscription.unsubscribe();
      selectionSubscription.unsubscribe();
      clearPendingClick();
      cancelDockingRefinement();
      plugin.selectionMode = false;
      const ownedReactRoot = reactRoot;
      reactRoot = undefined;
      disposeMolstarUiResources(plugin, ownedReactRoot);
    },
  };
}

function configureViewportSpec(spec: PluginUISpec): void {
  spec.config = [
    ...(spec.config ?? []).filter(([key]) => key !== PluginConfig.General.PixelScale),
    [PluginConfig.General.PixelScale, MOLSTAR_VIEWPORT_PIXEL_SCALE],
  ];
  spec.layout = {
    initial: {
      isExpanded: false,
      showControls: false,
      regionState: {
        left: 'hidden',
        right: 'hidden',
        top: 'hidden',
        bottom: 'hidden',
      },
    },
  };
  spec.components = {
    ...spec.components,
    controls: { top: 'none', right: 'none', left: 'none', bottom: 'none' },
    remoteState: 'none',
    sequenceViewer: { view: () => null },
  };
}

function configureThumbnailSpec(spec: PluginUISpec): void {
  spec.config = [
    ...(spec.config ?? []).filter(([key]) => key !== PluginConfig.General.PixelScale),
    [PluginConfig.General.PixelScale, MOLSTAR_THUMBNAIL_PIXEL_SCALE],
  ];
  spec.layout = {
    initial: {
      isExpanded: false,
      showControls: false,
      regionState: {
        left: 'hidden',
        right: 'hidden',
        top: 'hidden',
        bottom: 'hidden',
      },
    },
  };
  spec.components = {
    ...spec.components,
    controls: { top: 'none', right: 'none', left: 'none', bottom: 'none' },
    remoteState: 'none',
    viewport: { controls: () => null },
    sequenceViewer: { view: () => null },
  };
}
