/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { Empty, Spin } from '@arco-design/web-react';
import { Camera, Down, Left, Lightning, MoreOne, PreviewOpen, Right, Up } from '@icon-park/react';
import { Color } from 'molstar/lib/mol-util/color/index';
import { createPortal } from 'react-dom';
import React, { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  logScientificPreviewError,
  resolveScientificPreviewError,
  type ScientificPreviewError,
  scientificPreviewErrorKey,
} from './scientificPreviewError';
import './SynonBiomedStructureViewer.css';
import { isSynonBiomedHttpError, requestSynonBiomedJson } from '@/renderer/services/synonBiomedHttp';
import {
  createMolstarStructureEngine,
  isElectrostaticSurfaceRepresentation,
  type MolstarPocketInteractionName,
  type MolstarPocketInteractionVisibility,
  type MolstarLigandDepictionSource,
  type MolstarPocketOptions,
  type MolstarElectrostaticPotentialSources,
  type MolstarTrajectoryModelState,
  type MolstarViewLayer,
  type MolstarViewRepresentation,
  type MolstarStructureEngine,
} from './molstarStructureEngine';
import { ELECTROSTATIC_COLOR_STOPS } from './molstarElectrostaticTheme';
import { loadStructureContent, resolveStructureFormat } from './structureSource';
import { annotateMolstarControls } from './molstarControlsHelp';
import {
  createAnimationFrameCoalescer,
  createMolstarViewportResizeScheduler,
  type MolstarViewportResizeScheduler,
} from './molstarViewportPerformance';
import {
  mergeDockingEnsembleLigandCoordinates,
  parseDockingEnsemble,
  selectDockingEnsembleEntries,
  selectDockingEnsembleEntry,
  type DockingEnsemble,
  type DockingEnsembleEntry,
} from './dockingEnsemble';
import { loadCurrentInteractionDiagramCandidateSmiles } from './interactionDiagramLigandSource';
import type { MolstarInteractionStrengthRecord } from './molstarInteractionStrengthLabels';
import {
  decodeStructureElectrostaticMaps,
  isStructureElectrostaticMapResponse,
  MAX_STRUCTURE_ELECTROSTATIC_BATCH_LIGANDS,
  type StructureElectrostaticMapReport,
  type StructureElectrostaticMapResponse,
} from './structureElectrostaticMap';
import { renderMoleculeSvg, validateAndRenderMolBlock } from '@/renderer/services/rdkitBrowser';
import {
  StructureInteractionReportCache,
  type StructureInteractionDiagramResponse,
  type StructureInteractionReportResponse,
} from './structureInteractionReportCache';
import { StructureObjectList } from './StructureObjectList';
import type { MolstarStructureComposition, StructureObjectKind } from './structureComposition';

export { resolveStructureFormat } from './structureSource';

type SynonBiomedStructureViewerProps = {
  contentUrl?: string;
  content?: string;
  filename: string;
  rootFrameId?: string;
  conversationId?: string;
  companionArtifactUrls?: Readonly<Record<string, string>>;
};

type StructureMinimizationResponse = {
  ok: boolean;
  status: string;
  message?: string;
  content?: string;
  filename?: string;
  format?: string;
  atom_count?: number;
  ligand_atom_count?: number;
  fixed_atom_count?: number;
  protein_present?: boolean;
};

type MolstarControlTooltip = {
  text: string;
  left: number;
  top: number;
};

type QuickPanel = 'more' | null;
type CanvasBackground = 'white' | 'dark';
type DisplayRepresentation = Exclude<MolstarViewRepresentation, 'electrostatic'>;
type DisplayLayer = MolstarViewLayer;
type StructureToolbarDensity = 'full' | 'compact' | 'minimal';
type DockingLigandDepiction = {
  svg: string | null;
  unavailable: boolean;
};
type StructureLigandDepiction = DockingLigandDepiction &
  MolstarLigandDepictionSource & {
    smiles: string | null;
  };
type PocketInteractionStrengthContext =
  | {
      kind: 'docking';
      indices: readonly number[];
      ensemble: DockingEnsemble;
    }
  | {
      kind: 'structure';
      depiction: StructureLigandDepiction;
    };
type CachedElectrostaticMap = {
  content: string;
  ligandSignature: string;
  potentials: MolstarElectrostaticPotentialSources;
  report: StructureElectrostaticMapReport;
};

class StructureElectrostaticUserError extends Error {
  readonly userMessage: string;

  constructor(userMessage: string, options?: ErrorOptions) {
    super('STRUCTURE_ELECTROSTATIC_UNAVAILABLE', options);
    this.userMessage = userMessage;
  }
}
const SINGLE_TRAJECTORY_MODEL: MolstarTrajectoryModelState = {
  index: 0,
  count: 1,
};

export const resolveStructureToolbarDensity = (availableHeight: number): StructureToolbarDensity => {
  if (availableHeight <= 430) return 'minimal';
  if (availableHeight <= 600) return 'compact';
  return 'full';
};

const BACKGROUND_SEQUENCE: CanvasBackground[] = ['dark', 'white'];
const PDB_WATER_RESIDUES = new Set(['DOD', 'H2O', 'HOH', 'WAT']);

const DISPLAY_LAYER_COMPETITORS: ReadonlyArray<ReadonlySet<DisplayLayer>> = [
  new Set<DisplayLayer>(['ball-and-stick', 'line']),
  new Set<DisplayLayer>(['surface', 'pocket-surface']),
];

export const toggleStructureDisplayLayer = (
  currentLayers: readonly DisplayLayer[],
  targetLayer: DisplayLayer
): DisplayLayer[] => {
  if (currentLayers.includes(targetLayer)) return currentLayers.filter((layer) => layer !== targetLayer);
  const competitionGroup = DISPLAY_LAYER_COMPETITORS.find((group) => group.has(targetLayer));
  return [...currentLayers.filter((layer) => !competitionGroup?.has(layer)), targetLayer];
};

const CANVAS_BACKGROUNDS: Record<CanvasBackground, number> = {
  white: 0xffffff,
  dark: 0x172033,
};

const RIGHT_PANEL_DEFAULT_WIDTH = 180;
const RIGHT_PANEL_COLLAPSED_WIDTH = 28;
const RIGHT_PANEL_MIN_EXPANDED_WIDTH = 120;

const structureInteractionReportKey = (index: number, entry: DockingEnsembleEntry): string =>
  `${index}:${entry.candidateId}:${entry.poseRank}:${entry.residueName}`;

const primaryLigandInteractionReportKey = (depiction: StructureLigandDepiction): string =>
  `structure:${depiction.residueName}:${depiction.interactionResidueName}:${depiction.atomCount}`;

const DOCKING_COLOR_OPTIONS = [
  { key: 'teal', value: Color(0x0f766e) },
  { key: 'orange', value: Color(0xea580c) },
  { key: 'violet', value: Color(0x7c3aed) },
  { key: 'blue', value: Color(0x2563eb) },
] as const;

const POCKET_INTERACTION_LEGEND = [
  { key: 'hydrogenBond', color: '#2b83ba', provider: 'hydrogen-bonds' },
  {
    key: 'weakHydrogenBond',
    color: '#c5ddec',
    provider: 'weak-hydrogen-bonds',
  },
  { key: 'ionic', color: '#f0c814', provider: 'ionic' },
  { key: 'piStacking', color: '#8cb366', provider: 'pi-stacking' },
  { key: 'cationPi', color: '#ff8000', provider: 'cation-pi' },
  { key: 'hydrophobic', color: '#808080', provider: 'hydrophobic' },
  { key: 'halogenBond', color: '#40ffbf', provider: 'halogen-bonds' },
  {
    key: 'metalCoordination',
    color: '#8c4099',
    provider: 'metal-coordination',
  },
  { key: 'waterBridge', color: '#00ccee', provider: 'water-bridges' },
] as const;

const DEFAULT_POCKET_INTERACTION_VISIBILITY: MolstarPocketInteractionVisibility = {
  ionic: false,
  'pi-stacking': true,
  'cation-pi': false,
  'halogen-bonds': false,
  'hydrogen-bonds': true,
  'weak-hydrogen-bonds': false,
  hydrophobic: false,
  'metal-coordination': false,
  'water-bridges': false,
};

const resolveCandidateSmilesCompanionUrl = (companionArtifactUrls?: Readonly<Record<string, string>>): string | null =>
  Object.entries(companionArtifactUrls ?? {}).find(([name]) =>
    /(?:final_candidates|designed_ligands).*\.smi$/i.test(name)
  )?.[1] ?? null;

export const prepareLigandDepictionSvg = (svg: string, background: CanvasBackground): string => {
  const transparent = svg.replace(
    /<rect\b[^>]*(?:fill\s*:\s*#(?:fff|ffffff)|fill=["']#(?:fff|ffffff)["'])[^>]*\/?>(?:<\/rect>)?/gi,
    ''
  );
  const padded = transparent.replace(
    /viewBox\s*=\s*(["'])(-?\d+(?:\.\d+)?)\s+(-?\d+(?:\.\d+)?)\s+(\d+(?:\.\d+)?)\s+(\d+(?:\.\d+)?)\1/i,
    (_match, quote: string, xText: string, yText: string, widthText: string, heightText: string) => {
      const x = Number(xText);
      const y = Number(yText);
      const width = Number(widthText);
      const height = Number(heightText);
      const horizontalPadding = width * 0.06;
      const verticalPadding = height * 0.06;
      return `viewBox=${quote}${(x - horizontalPadding).toFixed(3)} ${(y - verticalPadding).toFixed(
        3
      )} ${(width + horizontalPadding * 2).toFixed(3)} ${(height + verticalPadding * 2).toFixed(3)}${quote}`;
    }
  );
  if (background !== 'dark') return padded;
  return padded.replace(/#000000\b/gi, '#FFFFFF');
};

type LigandDepictionBounds = Pick<DOMRect, 'x' | 'y' | 'width' | 'height'>;

export const resolveLigandDepictionViewBox = (bounds: LigandDepictionBounds): string | null => {
  if (
    !Number.isFinite(bounds.x) ||
    !Number.isFinite(bounds.y) ||
    !Number.isFinite(bounds.width) ||
    !Number.isFinite(bounds.height) ||
    bounds.width <= 0 ||
    bounds.height <= 0
  ) {
    return null;
  }
  const horizontalPadding = Math.max(4, bounds.width * 0.08);
  const verticalPadding = Math.max(4, bounds.height * 0.12);
  return [
    bounds.x - horizontalPadding,
    bounds.y - verticalPadding,
    bounds.width + horizontalPadding * 2,
    bounds.height + verticalPadding * 2,
  ]
    .map((value) => value.toFixed(3))
    .join(' ');
};

export const resolvePdbLigandResidueName = (content: string): string | undefined => {
  const counts = new Map<string, number>();
  content.split(/\r?\n/).forEach((line) => {
    if (!line.startsWith('HETATM') || line.length < 20) return;
    const residueName = line.slice(17, 20).trim().toUpperCase();
    if (!residueName || PDB_WATER_RESIDUES.has(residueName)) return;
    counts.set(residueName, (counts.get(residueName) ?? 0) + 1);
  });
  const candidates = [...counts.entries()].filter(([, count]) => count >= 2).map(([name]) => name);
  return candidates.length === 1 ? candidates[0] : undefined;
};

/** Hosts the native Mol* viewport within the Synon Biomed single-viewer boundary. */
const SynonBiomedStructureViewer: React.FC<SynonBiomedStructureViewerProps> = ({
  contentUrl,
  content,
  filename,
  rootFrameId,
  conversationId,
  companionArtifactUrls,
}) => {
  const { t } = useTranslation();
  const hostRef = useRef<HTMLDivElement>(null);
  const stageRef = useRef<HTMLDivElement>(null);
  const controlDockRef = useRef<HTMLDivElement>(null);
  const quickActionsRef = useRef<HTMLDivElement>(null);
  const engineRef = useRef<MolstarStructureEngine | null>(null);
  const busyActionRef = useRef<string | null>(null);
  const busyIndicatorTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const dockingSelectionQueueRef = useRef<Promise<void>>(Promise.resolve());
  const dockingSelectionRevisionRef = useRef(0);
  const ligandDepictionSvgRef = useRef<HTMLDivElement>(null);
  const interactionDiagramRequestRef = useRef(0);
  const interactionDiagramOpenRef = useRef(false);
  const pocketStrengthRequestRef = useRef(0);
  const pocketStrengthContextRef = useRef<PocketInteractionStrengthContext | null>(null);
  const pocketStrengthRenderQueueRef = useRef<Promise<void>>(Promise.resolve());
  const electrostaticMapRequestRef = useRef(0);
  const electrostaticMapCacheRef = useRef<CachedElectrostaticMap | null>(null);
  const pocketStrengthDebounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const interactionReportCacheRef = useRef(new StructureInteractionReportCache());
  const pocketStrengthRecordsRef = useRef<MolstarInteractionStrengthRecord[]>([]);
  const sourceRef = useRef<string | ArrayBuffer | null>(null);
  const loadedFilenameRef = useRef(filename);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<ScientificPreviewError | null>(null);
  const [tooltip, setTooltip] = useState<MolstarControlTooltip | null>(null);
  const [activePanel, setActivePanel] = useState<QuickPanel>(null);
  const [pocketVisible, setPocketVisible] = useState(false);
  const [pocketInteractionVisibility, setPocketInteractionVisibility] = useState<MolstarPocketInteractionVisibility>(
    DEFAULT_POCKET_INTERACTION_VISIBILITY
  );
  const pocketInteractionVisibilityRef = useRef<MolstarPocketInteractionVisibility>(
    DEFAULT_POCKET_INTERACTION_VISIBILITY
  );
  const [viewLayers, setViewLayers] = useState<DisplayLayer[]>([]);
  const pocketSettings: MolstarPocketOptions = {
    expandRadius: 4.5,
    showDistances: false,
    displayLayers: viewLayers,
    interactionVisibility: pocketInteractionVisibility,
  };
  const [selectedLigandResidueName, setSelectedLigandResidueName] = useState<string | undefined>();
  const [canvasBackground, setCanvasBackground] = useState<CanvasBackground>('white');
  const [busyAction, setBusyAction] = useState<string | null>(null);
  const [actionFeedback, setActionFeedback] = useState<string | null>(null);
  const [trajectoryModel, setTrajectoryModel] = useState<MolstarTrajectoryModelState>(SINGLE_TRAJECTORY_MODEL);
  const [dockingEnsemble, setDockingEnsemble] = useState<DockingEnsemble | null>(null);
  const [dockingEntryIndex, setDockingEntryIndex] = useState(0);
  const [selectedDockingIndices, setSelectedDockingIndices] = useState<number[]>([]);
  const [dockingColorIndices, setDockingColorIndices] = useState<Record<number, number>>({});
  const [expandedDockingColorIndex, setExpandedDockingColorIndex] = useState<number | null>(null);
  const [dockingProteinVisible, setDockingProteinVisible] = useState(true);
  const [structureComposition, setStructureComposition] = useState<MolstarStructureComposition | null>(null);
  const [structureObjectVisibility, setStructureObjectVisibility] = useState<Record<StructureObjectKind, boolean>>({
    protein: true,
    ligand: true,
  });
  const [structureLigandColorIndex, setStructureLigandColorIndex] = useState(0);
  const [structureLigandColorExpanded, setStructureLigandColorExpanded] = useState(false);
  const [dockingDepictionIndex, setDockingDepictionIndex] = useState<number | null>(null);
  const [dockingDepictions, setDockingDepictions] = useState<Record<number, DockingLigandDepiction>>({});
  const [dockingDepictionLoading, setDockingDepictionLoading] = useState(false);
  const [structureLigandDepiction, setStructureLigandDepiction] = useState<StructureLigandDepiction | null>(null);
  const [structureLigandDepictionLoading, setStructureLigandDepictionLoading] = useState(false);
  const [leftToolbarExpanded, setLeftToolbarExpanded] = useState(false);
  const [ligandDepictionExpanded, setLigandDepictionExpanded] = useState(false);
  const [interactionLegendExpanded, setInteractionLegendExpanded] = useState(false);
  const [electrostaticLegendExpanded, setElectrostaticLegendExpanded] = useState(false);
  const [electrostaticMapReport, setElectrostaticMapReport] = useState<StructureElectrostaticMapReport | null>(null);
  const [rightPanelExpanded, setRightPanelExpanded] = useState(false);
  const [comparisonEnabled, setComparisonEnabled] = useState(false);
  const [comparisonPrimaryIndex, setComparisonPrimaryIndex] = useState(0);
  const [dockingPanelWidth, setDockingPanelWidth] = useState(RIGHT_PANEL_DEFAULT_WIDTH);
  const [dockingCanvasInset, setDockingCanvasInset] = useState(RIGHT_PANEL_DEFAULT_WIDTH);
  const [dockingPanelHeight, setDockingPanelHeight] = useState<number | null>(null);
  const [toolbarDensity, setToolbarDensity] = useState<StructureToolbarDensity>('full');
  const [interactionDiagramOpen, setInteractionDiagramOpen] = useState(false);
  const [interactionDiagramLoading, setInteractionDiagramLoading] = useState(false);
  const [interactionDiagram, setInteractionDiagram] = useState<StructureInteractionDiagramResponse | null>(null);
  const [interactionDiagramError, setInteractionDiagramError] = useState<string | null>(null);
  const [interactionDiagramPendingPoseLabel, setInteractionDiagramPendingPoseLabel] = useState<string | null>(null);
  const [interactionDiagramExpanded, setInteractionDiagramExpanded] = useState(false);
  const [pocketStrengthStatus, setPocketStrengthStatus] = useState<'idle' | 'loading' | 'ready' | 'unavailable'>(
    'idle'
  );
  const format = resolveStructureFormat(filename);
  // Previews opened from a conversation may come from a legacy or local-file
  // path that did not persist rootFrameId in its metadata. The conversation
  // id is the same frame-scoped identity used by the unified backend contract
  // and is a safe fallback within the conversation preview boundary.
  const operationFrameId = rootFrameId?.trim() || conversationId?.trim();
  const canInteract = !loading && !error && Boolean(engineRef.current) && !busyAction;
  const dockingEntry = dockingEnsemble?.entries[dockingEntryIndex];
  const hasProteinContent = Boolean(
    dockingEnsemble ? dockingEnsemble.proteinLines.length > 0 : structureComposition?.hasProtein
  );
  const hasLigandContent = Boolean(
    dockingEnsemble ? selectedDockingIndices.length > 0 : structureComposition?.hasLigand
  );
  const isRepresentationAvailable = (representation: DisplayRepresentation): boolean => {
    if (representation === 'surface') return hasProteinContent;
    if (representation === 'pocket-surface') return hasProteinContent && hasLigandContent;
    if (representation === 'ligand-surface') return hasLigandContent;
    return true;
  };
  const smilesCompanionUrl = resolveCandidateSmilesCompanionUrl(companionArtifactUrls);
  const hasInteractionDiagramContext = Boolean(
    dockingEnsemble
      ? dockingEntry && operationFrameId && smilesCompanionUrl
      : structureLigandDepiction?.complexPdb && structureLigandDepiction.smiles && operationFrameId
  );
  const dockingPoseCount = (entry: DockingEnsembleEntry): number =>
    dockingEnsemble?.entries.filter(
      (candidate) => candidate.kind === 'candidate' && candidate.candidateId === entry.candidateId
    ).length ?? 0;
  const dockingColorValue = (index: number, colors: Readonly<Record<number, number>> = dockingColorIndices) =>
    DOCKING_COLOR_OPTIONS[colors[index] ?? 0]?.value ?? DOCKING_COLOR_OPTIONS[0].value;

  useEffect(() => {
    setDockingDepictions({});
    setDockingDepictionIndex(null);
  }, [dockingEnsemble?.source]);

  useEffect(() => {
    setRightPanelExpanded(false);
  }, [content, contentUrl, filename]);

  useEffect(() => {
    if (!pocketVisible) setInteractionLegendExpanded(false);
  }, [pocketVisible]);

  const hasElectrostaticSurface = viewLayers.some((layer) => isElectrostaticSurfaceRepresentation(layer));

  useEffect(() => {
    if (!hasElectrostaticSurface) setElectrostaticLegendExpanded(false);
  }, [hasElectrostaticSurface]);

  useEffect(() => {
    if (dockingEnsemble || !structureComposition?.hasLigand) {
      setStructureLigandDepiction(null);
      setStructureLigandDepictionLoading(false);
      return;
    }
    const engine = engineRef.current;
    if (!engine) return;
    let active = true;
    setStructureLigandDepiction(null);
    setStructureLigandDepictionLoading(true);
    void (async () => {
      try {
        const source = engine.getPrimaryLigandDepictionSource();
        const rendered = source ? await validateAndRenderMolBlock(source.molBlock, 276, 189) : null;
        if (!active) return;
        setStructureLigandDepiction(
          source
            ? {
                ...source,
                svg: rendered?.svg ?? null,
                smiles: rendered?.smiles ?? null,
                unavailable: !rendered?.svg,
              }
            : null
        );
      } catch (reason) {
        if (!active) return;
        logScientificPreviewError(
          '[SynonBiomedStructureViewer] Failed to render the primary ligand depiction',
          reason,
          'parse-failed'
        );
        setStructureLigandDepiction(null);
      } finally {
        if (active) setStructureLigandDepictionLoading(false);
      }
    })();
    return () => {
      active = false;
    };
  }, [dockingEnsemble, structureComposition]);

  useEffect(() => {
    setDockingDepictionIndex((current) =>
      current !== null && selectedDockingIndices.includes(current) ? current : (selectedDockingIndices[0] ?? null)
    );
  }, [selectedDockingIndices]);

  useEffect(() => {
    const index = dockingDepictionIndex;
    const entry = index === null ? undefined : dockingEnsemble?.entries[index];
    if (!entry || dockingDepictions[index] !== undefined) return;
    let active = true;
    setDockingDepictionLoading(true);
    void (async () => {
      try {
        const smiles = smilesCompanionUrl
          ? await loadCurrentInteractionDiagramCandidateSmiles(smilesCompanionUrl, entry.candidateId)
          : null;
        const svg = smiles ? await renderMoleculeSvg(smiles, 276, 189) : null;
        if (!active) return;
        setDockingDepictions((current) => ({
          ...current,
          [index]: { svg, unavailable: !svg },
        }));
      } catch (reason) {
        if (!active) return;
        logScientificPreviewError(
          '[SynonBiomedStructureViewer] Failed to render docking ligand depiction',
          reason,
          'parse-failed'
        );
        setDockingDepictions((current) => ({
          ...current,
          [index]: { svg: null, unavailable: true },
        }));
      } finally {
        if (active) setDockingDepictionLoading(false);
      }
    })();
    return () => {
      active = false;
    };
  }, [dockingDepictionIndex, dockingDepictions, dockingEnsemble, smilesCompanionUrl]);

  useLayoutEffect(() => {
    if (!ligandDepictionExpanded) return;
    const svg = ligandDepictionSvgRef.current?.querySelector('svg');
    if (!svg || typeof svg.getBBox !== 'function') return;
    try {
      const fittedViewBox = resolveLigandDepictionViewBox(svg.getBBox());
      if (!fittedViewBox) return;
      svg.setAttribute('viewBox', fittedViewBox);
      svg.setAttribute('preserveAspectRatio', 'xMidYMid meet');
    } catch (reason) {
      logScientificPreviewError(
        '[SynonBiomedStructureViewer] Failed to fit docking ligand depiction',
        reason,
        'parse-failed'
      );
    }
  }, [canvasBackground, dockingDepictionIndex, dockingDepictions, ligandDepictionExpanded, structureLigandDepiction]);

  const stepDockingDepiction = (direction: -1 | 1) => {
    if (selectedDockingIndices.length < 2) return;
    const currentPosition = selectedDockingIndices.indexOf(dockingDepictionIndex ?? selectedDockingIndices[0]);
    const nextPosition =
      (Math.max(0, currentPosition) + direction + selectedDockingIndices.length) % selectedDockingIndices.length;
    setDockingDepictionIndex(selectedDockingIndices[nextPosition]);
  };

  const runEngineAction = async (
    actionId: string,
    operation: (engine: MolstarStructureEngine) => Promise<void> | void
  ): Promise<boolean> => {
    const engine = engineRef.current;
    if (!engine || loading || error || busyActionRef.current) return false;

    busyActionRef.current = actionId;
    if (busyIndicatorTimerRef.current) clearTimeout(busyIndicatorTimerRef.current);
    if (actionId !== 'docking-entry') {
      busyIndicatorTimerRef.current = setTimeout(() => {
        busyIndicatorTimerRef.current = null;
        if (busyActionRef.current === actionId) setBusyAction(actionId);
      }, 180);
    }
    setActionFeedback(null);
    try {
      const result = operation(engine);
      if (result && typeof result.then === 'function') await result;
      return true;
    } catch (reason) {
      logScientificPreviewError(
        `[SynonBiomedStructureViewer] Failed to run ${actionId} action`,
        reason,
        'parse-failed'
      );
      setActionFeedback(
        reason instanceof StructureElectrostaticUserError
          ? reason.userMessage
          : t('preview.scientific.structure.quickActions.actionFailed')
      );
      return false;
    } finally {
      if (busyIndicatorTimerRef.current) {
        clearTimeout(busyIndicatorTimerRef.current);
        busyIndicatorTimerRef.current = null;
      }
      if (busyActionRef.current === actionId) busyActionRef.current = null;
      setBusyAction(null);
    }
  };

  const ensureElectrostaticPotential = async (
    engine: MolstarStructureEngine,
    ligandResidueNames?: readonly string[]
  ): Promise<void> => {
    const source = engine.getElectrostaticInputSource(ligandResidueNames);
    if (!operationFrameId || !source) {
      throw new StructureElectrostaticUserError(
        t('preview.scientific.structure.quickActions.electrostaticMapUnavailable')
      );
    }
    if (source.ligands.length > MAX_STRUCTURE_ELECTROSTATIC_BATCH_LIGANDS) {
      throw new StructureElectrostaticUserError(
        t('preview.scientific.structure.quickActions.electrostaticMapUnavailable')
      );
    }
    const ligandKeys = new Set<string>();
    for (const ligand of source.ligands) {
      const key = ligand.key.trim();
      const normalizedKey = key.toUpperCase();
      if (
        key !== ligand.key ||
        !/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(key) ||
        !ligand.molBlock.trim() ||
        ligandKeys.has(normalizedKey)
      ) {
        throw new StructureElectrostaticUserError(
          t('preview.scientific.structure.quickActions.electrostaticMapUnavailable')
        );
      }
      ligandKeys.add(normalizedKey);
    }
    const ligandSignature = JSON.stringify(
      source.ligands.map(({ key, molBlock, atomCount }) => [key, molBlock, atomCount])
    );
    const cached = electrostaticMapCacheRef.current;
    if (cached?.content === source.content && cached.ligandSignature === ligandSignature) return;

    const requestId = ++electrostaticMapRequestRef.current;
    try {
      const labelStem = (loadedFilenameRef.current || filename).replace(/\.[^.]+$/, '') || 'structure';
      const response = await requestSynonBiomedJson<StructureElectrostaticMapResponse>(
        `/api/frames/${encodeURIComponent(operationFrameId)}/structure-electrostatic-map`,
        {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            content: source.content,
            filename: loadedFilenameRef.current || filename,
            ...(source.ligands.length === 1 ? { ligand_mol_block: source.ligands[0].molBlock } : {}),
            ...(source.ligands.length > 1
              ? {
                  ligand_mol_blocks: source.ligands.map(({ key, molBlock }) => ({
                    key,
                    mol_block: molBlock,
                  })),
                }
              : {}),
            ph: 7.4,
            ionic_strength_molar: 0.15,
          }),
        },
        { timeoutMs: 990000 }
      );
      if (!isStructureElectrostaticMapResponse(response)) {
        throw new Error('STRUCTURE_ELECTROSTATIC_RESPONSE_INVALID');
      }
      const maps = await decodeStructureElectrostaticMaps(response);
      if (requestId !== electrostaticMapRequestRef.current || engineRef.current !== engine) {
        throw new Error('STRUCTURE_ELECTROSTATIC_REQUEST_RETIRED');
      }
      const potentials: MolstarElectrostaticPotentialSources = {};
      const ligandPotentials: Record<string, { source: string; label: string }> = {};
      if (maps.protein) {
        potentials.protein = { source: maps.protein, label: `${labelStem}-protein-apbs.dx` };
      }
      for (const ligand of source.ligands) {
        const map = source.ligands.length === 1 ? maps.ligand : maps.ligands?.[ligand.key];
        if (!map) throw new Error(`STRUCTURE_ELECTROSTATIC_LIGAND_MAP_MISSING:${ligand.key}`);
        const safeKey = ligand.key.replace(/[^A-Za-z0-9_-]+/g, '-').slice(0, 64) || 'ligand';
        ligandPotentials[ligand.key] = {
          source: map,
          label: `${labelStem}-${safeKey}-ligand-apbs.dx`,
        };
      }
      if (Object.keys(ligandPotentials).length > 0) potentials.ligands = ligandPotentials;
      await engine.setElectrostaticPotentials(potentials, response.report.color_range);
      electrostaticMapCacheRef.current = {
        content: source.content,
        ligandSignature,
        potentials,
        report: response.report,
      };
      setElectrostaticMapReport(response.report);
    } catch (reason) {
      logScientificPreviewError(
        '[SynonBiomedStructureViewer] Failed to load the APBS electrostatic potential',
        reason,
        'parse-failed'
      );
      const message =
        isSynonBiomedHttpError(reason) && reason.backendMessage
          ? reason.backendMessage
          : t('preview.scientific.structure.quickActions.electrostaticMapUnavailable');
      throw new StructureElectrostaticUserError(message, { cause: reason });
    }
  };

  const handleCapture = async () => {
    await runEngineAction('snapshot', async (engine) => {
      const bounds = hostRef.current?.getBoundingClientRect();
      const width = Math.max(512, Math.min(2048, Math.round((bounds?.width ?? 640) * 2)));
      const height = Math.max(512, Math.min(2048, Math.round((bounds?.height ?? 480) * 2)));
      const dataUri = await engine.captureImage({
        width,
        height,
        backgroundColor: CANVAS_BACKGROUNDS[canvasBackground],
      });
      const anchor = document.createElement('a');
      anchor.href = dataUri;
      anchor.download = `${filename.replace(/\.[^.]+$/, '') || 'structure'}-snapshot.png`;
      anchor.rel = 'noopener';
      document.body.appendChild(anchor);
      anchor.click();
      anchor.remove();
      setActionFeedback(t('preview.scientific.structure.quickActions.snapshotSaved'));
    });
  };

  const interactionPoseLabel = (entry: DockingEnsembleEntry): string =>
    `${t('preview.scientific.structure.quickActions.poseRank')} ${entry.poseRank}${
      entry.affinityKcalMol === undefined ? '' : ` · ${entry.affinityKcalMol.toFixed(3)} kcal/mol`
    }`;

  const requestInteractionReport = async (
    index: number,
    ensemble: DockingEnsemble,
    reportOnly: boolean
  ): Promise<StructureInteractionReportResponse> => {
    const entry = ensemble.entries[index];
    if (!entry || !operationFrameId || !smilesCompanionUrl) {
      throw new Error('STRUCTURE_INTERACTION_CONTEXT_MISSING');
    }
    const candidateSmiles = await loadCurrentInteractionDiagramCandidateSmiles(smilesCompanionUrl, entry.candidateId);
    if (!candidateSmiles) throw new Error('STRUCTURE_INTERACTION_SMILES_MISSING');
    return requestSynonBiomedJson<StructureInteractionReportResponse>(
      `/api/frames/${encodeURIComponent(operationFrameId)}/structure-interaction-diagram`,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          content: selectDockingEnsembleEntry(ensemble, index),
          filename: loadedFilenameRef.current || filename,
          ligand_residue_name: entry.residueName,
          smiles: candidateSmiles,
          ligand_label: entry.candidateId,
          pose_label: interactionPoseLabel(entry),
          ...(reportOnly ? { report_only: true } : {}),
        }),
      },
      { timeoutMs: 630000 }
    );
  };

  const loadInteractionDiagram = (
    index: number,
    ensemble: DockingEnsemble
  ): Promise<StructureInteractionDiagramResponse> => {
    const entry = ensemble.entries[index];
    if (!entry) return Promise.reject(new Error('STRUCTURE_INTERACTION_CONTEXT_MISSING'));
    return interactionReportCacheRef.current.loadDiagram(structureInteractionReportKey(index, entry), async () => {
      const response = await requestInteractionReport(index, ensemble, false);
      if (!response.ok || !response.svg || !response.png_base64 || !response.report) {
        throw new Error('STRUCTURE_INTERACTION_RESPONSE_INVALID');
      }
      return response as StructureInteractionDiagramResponse;
    });
  };

  const requestPrimaryLigandInteractionReport = async (
    depiction: StructureLigandDepiction,
    reportOnly: boolean
  ): Promise<StructureInteractionReportResponse> => {
    if (!operationFrameId || !depiction.complexPdb || !depiction.smiles) {
      throw new Error('STRUCTURE_INTERACTION_CONTEXT_MISSING');
    }
    const basename = (loadedFilenameRef.current || filename).replace(/\.[^.]+$/, '') || 'structure';
    return requestSynonBiomedJson<StructureInteractionReportResponse>(
      `/api/frames/${encodeURIComponent(operationFrameId)}/structure-interaction-diagram`,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          content: depiction.complexPdb,
          filename: `${basename}-interaction.pdb`,
          ligand_residue_name: depiction.interactionResidueName,
          smiles: depiction.smiles,
          ligand_label: depiction.residueName,
          pose_label: t('preview.scientific.structure.quickActions.primaryLigandPose'),
          report_only: reportOnly,
        }),
      },
      { timeoutMs: 630000 }
    );
  };

  const loadPrimaryLigandInteractionDiagram = (
    depiction: StructureLigandDepiction
  ): Promise<StructureInteractionDiagramResponse> =>
    interactionReportCacheRef.current.loadDiagram(primaryLigandInteractionReportKey(depiction), async () => {
      const response = await requestPrimaryLigandInteractionReport(depiction, false);
      if (!response.ok || !response.svg || !response.png_base64 || !response.report) {
        throw new Error('STRUCTURE_INTERACTION_RESPONSE_INVALID');
      }
      return response as StructureInteractionDiagramResponse;
    });

  const loadInteractionStrengthReport = (
    index: number,
    ensemble: DockingEnsemble
  ): Promise<StructureInteractionReportResponse> => {
    const entry = ensemble.entries[index];
    if (!entry) return Promise.reject(new Error('STRUCTURE_INTERACTION_CONTEXT_MISSING'));
    return interactionReportCacheRef.current.loadReport(structureInteractionReportKey(index, entry), async () => {
      const response = await requestInteractionReport(index, ensemble, true);
      if (!response.ok || !response.report || !Array.isArray(response.report.interactions)) {
        throw new Error('STRUCTURE_INTERACTION_STRENGTH_RESPONSE_INVALID');
      }
      return response;
    });
  };

  const loadPrimaryLigandInteractionStrengthReport = (
    depiction: StructureLigandDepiction
  ): Promise<StructureInteractionReportResponse> =>
    interactionReportCacheRef.current.loadReport(primaryLigandInteractionReportKey(depiction), async () => {
      const response = await requestPrimaryLigandInteractionReport(depiction, true);
      if (!response.ok || !response.report || !Array.isArray(response.report.interactions)) {
        throw new Error('STRUCTURE_INTERACTION_STRENGTH_RESPONSE_INVALID');
      }
      return response;
    });

  const refreshInteractionDiagram = async (
    index: number = dockingEntryIndex,
    ensemble: DockingEnsemble | null = dockingEnsemble
  ) => {
    if (!engineRef.current) return;
    const requestId = ++interactionDiagramRequestRef.current;
    const entry = ensemble?.entries[index];
    const poseLabel = entry
      ? interactionPoseLabel(entry)
      : structureLigandDepiction
        ? t('preview.scientific.structure.quickActions.primaryLigandPose')
        : null;
    setInteractionDiagramLoading(true);
    setInteractionDiagramPendingPoseLabel(poseLabel);
    setInteractionDiagramError(null);
    try {
      let response: StructureInteractionDiagramResponse;
      if (ensemble && entry) {
        response = await loadInteractionDiagram(index, ensemble);
      } else if (structureLigandDepiction) {
        response = await loadPrimaryLigandInteractionDiagram(structureLigandDepiction);
      } else {
        throw new Error('STRUCTURE_INTERACTION_CONTEXT_MISSING');
      }
      if (requestId !== interactionDiagramRequestRef.current || !interactionDiagramOpenRef.current) return;
      setInteractionDiagram(response);
      setInteractionDiagramPendingPoseLabel(null);
    } catch (reason) {
      if (requestId !== interactionDiagramRequestRef.current || !interactionDiagramOpenRef.current) return;
      logScientificPreviewError(
        '[SynonBiomedStructureViewer] Failed to derive the active interaction diagram',
        reason,
        'parse-failed'
      );
      setInteractionDiagram(null);
      setInteractionDiagramPendingPoseLabel(null);
      setInteractionDiagramError(
        isSynonBiomedHttpError(reason) && reason.backendMessage
          ? reason.backendMessage
          : t('preview.scientific.structure.quickActions.interactionDiagramUnavailable')
      );
    } finally {
      if (requestId === interactionDiagramRequestRef.current) setInteractionDiagramLoading(false);
    }
  };

  const queuePocketInteractionStrengthRender = (
    engine: MolstarStructureEngine,
    records: readonly MolstarInteractionStrengthRecord[],
    visibility: MolstarPocketInteractionVisibility,
    isCurrent: () => boolean = () => true
  ): Promise<void> => {
    const render = pocketStrengthRenderQueueRef.current.then(async () => {
      if (!isCurrent() || engineRef.current !== engine) return;
      await engine.setPocketInteractionStrengths(records, visibility);
    });
    pocketStrengthRenderQueueRef.current = render.catch((): void => {});
    return render;
  };

  const clearPocketInteractionStrengthRender = (): void => {
    const engine = engineRef.current;
    if (!engine) return;
    void queuePocketInteractionStrengthRender(engine, [], pocketInteractionVisibilityRef.current).catch((reason) => {
      logScientificPreviewError(
        '[SynonBiomedStructureViewer] Failed to clear 3D interaction strengths',
        reason,
        'parse-failed'
      );
    });
  };

  const refreshPocketInteractionStrengths = async (
    context: PocketInteractionStrengthContext,
    visibility: MolstarPocketInteractionVisibility = pocketInteractionVisibilityRef.current
  ): Promise<void> => {
    pocketStrengthContextRef.current = context;
    const requestId = ++pocketStrengthRequestRef.current;
    const engine = engineRef.current;
    pocketStrengthRecordsRef.current = [];
    if (!engine) return;
    const isCurrent = () => requestId === pocketStrengthRequestRef.current && engineRef.current === engine;
    try {
      await queuePocketInteractionStrengthRender(engine, [], visibility, isCurrent);
    } catch (reason) {
      logScientificPreviewError(
        '[SynonBiomedStructureViewer] Failed to reset 3D interaction strengths',
        reason,
        'parse-failed'
      );
      if (isCurrent()) setPocketStrengthStatus('unavailable');
      return;
    }
    if (!isCurrent()) return;
    if (context.kind === 'docking' && context.indices.length === 0) {
      setPocketStrengthStatus('idle');
      return;
    }
    if (
      !operationFrameId ||
      (context.kind === 'docking' ? !smilesCompanionUrl : !context.depiction.complexPdb || !context.depiction.smiles)
    ) {
      setPocketStrengthStatus('unavailable');
      return;
    }

    setPocketStrengthStatus('loading');
    const records: MolstarInteractionStrengthRecord[] = [];
    let renderFailed = false;
    const loaders: Array<() => Promise<MolstarInteractionStrengthRecord[]>> =
      context.kind === 'docking'
        ? context.indices.map((index) => async () => {
            const entry = context.ensemble.entries[index];
            if (!entry) return [];
            const response = await loadInteractionStrengthReport(index, context.ensemble);
            if (!response.ok || !Array.isArray(response.report?.interactions)) {
              throw new Error('STRUCTURE_INTERACTION_STRENGTH_RESPONSE_INVALID');
            }
            return response.report.interactions.map((record) =>
              Object.assign({}, record, { ligand_label: entry.candidateId })
            );
          })
        : [
            async () => {
              const response = await loadPrimaryLigandInteractionStrengthReport(context.depiction);
              return (response.report.interactions ?? []).map((record) =>
                Object.assign({}, record, { ligand_label: context.depiction.residueName })
              );
            },
          ];

    const loadBatch = async (offset: number): Promise<void> => {
      if (offset >= loaders.length) return;
      const batch = loaders.slice(offset, offset + 2);
      const results = await Promise.all(
        batch.map(async (loadRecords) => {
          try {
            return await loadRecords();
          } catch (reason) {
            logScientificPreviewError(
              '[SynonBiomedStructureViewer] Failed to derive 3D interaction strengths',
              reason,
              'parse-failed'
            );
            return [];
          }
        })
      );
      if (!isCurrent()) return;
      records.push(...results.flat());
      pocketStrengthRecordsRef.current = records;
      try {
        await queuePocketInteractionStrengthRender(engine, records, visibility, isCurrent);
      } catch (reason) {
        logScientificPreviewError(
          '[SynonBiomedStructureViewer] Failed to render 3D interaction strengths',
          reason,
          'parse-failed'
        );
        renderFailed = true;
        if (isCurrent()) setPocketStrengthStatus('unavailable');
        return;
      }
      if (!isCurrent()) return;
      await loadBatch(offset + 2);
    };

    await loadBatch(0);

    if (!isCurrent()) return;
    if (renderFailed) return;
    setPocketStrengthStatus(records.length > 0 ? 'ready' : 'unavailable');
  };

  const schedulePocketInteractionStrengthRefresh = (
    context: PocketInteractionStrengthContext,
    visibility: MolstarPocketInteractionVisibility = pocketInteractionVisibilityRef.current
  ) => {
    pocketStrengthContextRef.current = context;
    if (pocketStrengthDebounceRef.current) clearTimeout(pocketStrengthDebounceRef.current);
    pocketStrengthDebounceRef.current = setTimeout(() => {
      pocketStrengthDebounceRef.current = null;
      void refreshPocketInteractionStrengths(context, visibility);
    }, 160);
  };

  const cancelScheduledPocketInteractionStrengthRefresh = () => {
    if (!pocketStrengthDebounceRef.current) return;
    clearTimeout(pocketStrengthDebounceRef.current);
    pocketStrengthDebounceRef.current = null;
  };

  useEffect(() => {
    if (!pocketVisible || dockingEnsemble || !structureLigandDepiction) return;
    schedulePocketInteractionStrengthRefresh({
      kind: 'structure',
      depiction: structureLigandDepiction,
    });
  }, [dockingEnsemble, pocketVisible, structureLigandDepiction]);

  const invalidateInteractionDiagramSource = () => {
    interactionReportCacheRef.current.clear();
    interactionDiagramRequestRef.current += 1;
    interactionDiagramOpenRef.current = false;
    setInteractionDiagramOpen(false);
    setInteractionDiagram(null);
    setInteractionDiagramLoading(false);
    setInteractionDiagramPendingPoseLabel(null);
    setInteractionDiagramExpanded(false);
    setInteractionDiagramError(null);
  };

  const handleInteractionDiagramToggle = () => {
    const nextOpen = !interactionDiagramOpenRef.current;
    interactionDiagramOpenRef.current = nextOpen;
    setInteractionDiagramOpen(nextOpen);
    if (nextOpen) {
      void refreshInteractionDiagram();
    } else {
      interactionDiagramRequestRef.current += 1;
      setInteractionDiagramLoading(false);
      setInteractionDiagramPendingPoseLabel(null);
      setInteractionDiagramExpanded(false);
      setInteractionDiagramError(null);
    }
  };

  const handleInteractionDiagramDownload = (outputFormat: 'svg' | 'png') => {
    if (!interactionDiagram) return;
    const blob =
      outputFormat === 'svg'
        ? new Blob([interactionDiagram.svg], {
            type: 'image/svg+xml;charset=utf-8',
          })
        : new Blob([Uint8Array.from(atob(interactionDiagram.png_base64), (character) => character.charCodeAt(0))], {
            type: 'image/png',
          });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement('a');
    const basename = filename.replace(/\.[^.]+$/, '') || 'structure';
    anchor.href = url;
    anchor.download = `${basename}-${interactionDiagram.report.ligand_label}-interactions.${outputFormat}`
      .replace(/[^A-Za-z0-9._-]+/g, '-')
      .replace(/-+/g, '-');
    anchor.rel = 'noopener';
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();
    URL.revokeObjectURL(url);
  };

  const handlePocketAction = async () => {
    if (dockingEnsemble && dockingEntry) {
      const activeIndices = selectedDockingIndices;
      if (activeIndices.length === 0) return;
      const residueNames = activeIndices.map((index) => dockingEnsemble.entries[index].residueName);
      let hasLigand = false;
      const succeeded = await runEngineAction('pocket', async (engine) => {
        if (pocketVisible) {
          hasLigand = (await engine.clearDockingPocket(residueNames)).hasLigand;
        } else if (activeIndices.length === 1) {
          hasLigand = (
            await engine.applyDockingPocket(residueNames[0], {
              ...pocketSettings,
              focusCamera: true,
              ligandColor: dockingColorValue(activeIndices[0]),
            })
          ).hasLigand;
        } else if (activeIndices.length === 2) {
          hasLigand = (
            await engine.replaceDockingComparison(residueNames[0], residueNames[1], {
              ...pocketSettings,
              focusCamera: true,
              primaryColor: dockingColorValue(activeIndices[0]),
              secondaryColor: dockingColorValue(activeIndices[1]),
            })
          ).hasLigand;
        } else {
          hasLigand = (
            await engine.replaceDockingSelection(residueNames, {
              ...pocketSettings,
              focusCamera: true,
              ligandColor: dockingColorValue(activeIndices[0]),
              ligandColors: activeIndices.map((index) => dockingColorValue(index)),
            })
          ).hasLigand;
        }
      });
      if (succeeded) {
        const nextPocketVisible = !pocketVisible && hasLigand;
        setPocketVisible(nextPocketVisible);
        if (!nextPocketVisible) setViewLayers([]);
        if (nextPocketVisible) {
          schedulePocketInteractionStrengthRefresh({
            kind: 'docking',
            indices: activeIndices,
            ensemble: dockingEnsemble,
          });
        } else {
          cancelScheduledPocketInteractionStrengthRefresh();
          pocketStrengthRequestRef.current += 1;
          pocketStrengthContextRef.current = null;
          pocketStrengthRecordsRef.current = [];
          setPocketStrengthStatus('idle');
          clearPocketInteractionStrengthRender();
          engineRef.current?.resetCamera();
        }
        setActivePanel(null);
      }
      return;
    }

    if (pocketVisible) {
      const succeeded = await runEngineAction('pocket-reset', (engine) => {
        return engine.clearPocketFocus();
      });
      if (succeeded) {
        setPocketVisible(false);
        setViewLayers([]);
        cancelScheduledPocketInteractionStrengthRefresh();
        pocketStrengthRequestRef.current += 1;
        pocketStrengthContextRef.current = null;
        pocketStrengthRecordsRef.current = [];
        setPocketStrengthStatus('idle');
        clearPocketInteractionStrengthRender();
        engineRef.current?.resetCamera();
        setActivePanel(null);
      }
      return;
    }

    let hasLigand = false;
    const succeeded = await runEngineAction('pocket', async (engine) => {
      hasLigand = (await engine.applyPocketFocus(pocketSettings)).hasLigand;
    });
    if (succeeded) {
      setPocketVisible(hasLigand);
      if (hasLigand && structureLigandDepiction) {
        schedulePocketInteractionStrengthRefresh({
          kind: 'structure',
          depiction: structureLigandDepiction,
        });
      }
    }
  };

  const handlePocketInteractionVisibilityChange = async (provider: MolstarPocketInteractionName) => {
    const previousVisibility = pocketInteractionVisibility;
    const nextVisibility = {
      ...previousVisibility,
      [provider]: previousVisibility[provider] === false,
    };
    if (!pocketVisible) {
      pocketInteractionVisibilityRef.current = nextVisibility;
      setPocketInteractionVisibility(nextVisibility);
      return;
    }
    const strengthContext = pocketStrengthContextRef.current;
    cancelScheduledPocketInteractionStrengthRefresh();
    pocketStrengthRequestRef.current += 1;
    const applyVisibility = async (engine: MolstarStructureEngine, visibility: MolstarPocketInteractionVisibility) => {
      const settings = {
        ...pocketSettings,
        focusCamera: false,
        interactionVisibility: visibility,
      };
      if (!dockingEnsemble || selectedDockingIndices.length === 0) {
        await engine.applyPocketFocus(settings);
        await queuePocketInteractionStrengthRender(engine, pocketStrengthRecordsRef.current, visibility);
        return;
      }
      const residueNames = selectedDockingIndices.map((index) => dockingEnsemble.entries[index].residueName);
      if (residueNames.length === 1) {
        await engine.applyDockingPocket(residueNames[0], {
          ...settings,
          ligandColor: dockingColorValue(selectedDockingIndices[0]),
        });
      } else if (residueNames.length === 2) {
        await engine.replaceDockingComparison(residueNames[0], residueNames[1], {
          ...settings,
          primaryColor: dockingColorValue(selectedDockingIndices[0]),
          secondaryColor: dockingColorValue(selectedDockingIndices[1]),
        });
      } else {
        await engine.replaceDockingSelection(residueNames, {
          ...settings,
          ligandColor: dockingColorValue(selectedDockingIndices[0]),
          ligandColors: selectedDockingIndices.map((index) => dockingColorValue(index)),
        });
      }
      await queuePocketInteractionStrengthRender(engine, pocketStrengthRecordsRef.current, visibility);
    };
    const succeeded = await runEngineAction(`interaction-${provider}`, async (engine) => {
      await pocketStrengthRenderQueueRef.current;
      try {
        await applyVisibility(engine, nextVisibility);
      } catch (reason) {
        try {
          await applyVisibility(engine, previousVisibility);
        } catch (rollbackReason) {
          logScientificPreviewError(
            '[SynonBiomedStructureViewer] Failed to restore the previous pocket interaction visibility',
            rollbackReason,
            'parse-failed'
          );
        }
        throw reason;
      }
    });
    if (succeeded) {
      pocketInteractionVisibilityRef.current = nextVisibility;
      setPocketInteractionVisibility(nextVisibility);
      if (strengthContext) schedulePocketInteractionStrengthRefresh(strengthContext, nextVisibility);
    } else if (strengthContext) {
      schedulePocketInteractionStrengthRefresh(strengthContext, previousVisibility);
    }
  };

  const handleViewRepresentation = async (representation: DisplayRepresentation) => {
    const preservePocket = pocketVisible && representation !== 'initial';
    const nextLayers =
      representation === 'initial'
        ? []
        : dockingEntry || preservePocket
          ? toggleStructureDisplayLayer(viewLayers, representation)
          : [representation];
    const dockingResidueIndices =
      dockingEntry && dockingEnsemble
        ? selectedDockingIndices.length > 0
          ? selectedDockingIndices
          : [dockingEntryIndex]
        : [];
    const dockingResidueNames = dockingResidueIndices.map((index) => dockingEnsemble!.entries[index].residueName);
    const succeeded = await runEngineAction(`view-${representation}`, async (engine) => {
      if (nextLayers.some((layer) => isElectrostaticSurfaceRepresentation(layer))) {
        await ensureElectrostaticPotential(
          engine,
          nextLayers.includes('ligand-surface') ? (dockingEntry ? dockingResidueNames : undefined) : []
        );
      }
      if (!dockingEntry) {
        if (preservePocket) {
          await engine.applyPocketFocus({
            ...pocketSettings,
            focusCamera: false,
            displayLayers: nextLayers,
          });
        } else {
          await engine.applyRepresentationStyle(nextLayers[0] ?? 'initial');
        }
        if (representation === 'initial') engine.resetCamera();
        return;
      }
      const residueNames = dockingResidueNames;
      const residueIndices = dockingResidueIndices;
      if (preservePocket && residueNames.length === 1) {
        await engine.applyDockingPocket(residueNames[0], {
          ...pocketSettings,
          focusCamera: false,
          displayLayers: nextLayers,
          ligandColor: dockingColorValue(residueIndices[0]),
        });
      } else if (preservePocket && residueNames.length === 2) {
        await engine.replaceDockingComparison(residueNames[0], residueNames[1], {
          ...pocketSettings,
          focusCamera: false,
          displayLayers: nextLayers,
          primaryColor: dockingColorValue(residueIndices[0]),
          secondaryColor: dockingColorValue(residueIndices[1]),
        });
      } else if (preservePocket) {
        await engine.replaceDockingSelection(residueNames, {
          ...pocketSettings,
          focusCamera: false,
          displayLayers: nextLayers,
          ligandColor: dockingColorValue(residueIndices[0]),
          ligandColors: residueIndices.map((index) => dockingColorValue(index)),
        });
      } else {
        await engine.replaceDockingLayers(
          residueNames,
          nextLayers,
          residueIndices.map((index) => dockingColorValue(index))
        );
      }
      if (representation === 'initial') engine.resetCamera();
    });
    if (succeeded) {
      setViewLayers(nextLayers);
      if (preservePocket) return;
      setPocketVisible(false);
      cancelScheduledPocketInteractionStrengthRefresh();
      pocketStrengthRequestRef.current += 1;
      pocketStrengthContextRef.current = null;
      pocketStrengthRecordsRef.current = [];
      setPocketStrengthStatus('idle');
      clearPocketInteractionStrengthRender();
    }
  };

  const handleTrajectoryModelChange = async (by: number) => {
    await runEngineAction('trajectory-model', async (engine) => {
      setTrajectoryModel(await engine.advanceTrajectoryModel(by));
      setStructureComposition(engine.getStructureComposition());
    });
    electrostaticMapRequestRef.current += 1;
    electrostaticMapCacheRef.current = null;
    setElectrostaticMapReport(null);
    setViewLayers([]);
    if (interactionDiagramOpenRef.current) await refreshInteractionDiagram();
  };

  const applyDockingMultiSelection = async (
    indices: readonly number[],
    colors: Readonly<Record<number, number>> = dockingColorIndices
  ) => {
    const ensemble = dockingEnsemble;
    if (!ensemble) return;
    if (busyActionRef.current && busyActionRef.current !== 'docking-entry') return;
    engineRef.current?.cancelDockingRefinement();
    const nonElectrostaticLayers = viewLayers.filter((layer) => !isElectrostaticSurfaceRepresentation(layer));
    const selectionChanged =
      indices.length !== selectedDockingIndices.length ||
      indices.some((index, position) => index !== selectedDockingIndices[position]);
    const refreshElectrostaticMap = selectionChanged && nonElectrostaticLayers.length !== viewLayers.length;
    if (refreshElectrostaticMap) setViewLayers(nonElectrostaticLayers);
    const revision = ++dockingSelectionRevisionRef.current;
    const nextIndices = [...new Set(indices)].filter((index) => index >= 0 && index < ensemble.entries.length);
    const previousState = {
      source: sourceRef.current,
      selectedIndices: selectedDockingIndices,
      primaryIndex: comparisonPrimaryIndex,
      entryIndex: dockingEntryIndex,
      comparison: comparisonEnabled,
      depictionIndex: dockingDepictionIndex,
      colors: dockingColorIndices,
      electrostaticCache: electrostaticMapCacheRef.current,
      electrostaticReport: electrostaticMapReport,
    };
    const applySelectionLayers = async (
      engine: MolstarStructureEngine,
      targetIndices: readonly number[],
      layers: readonly DisplayLayer[],
      targetColors: Readonly<Record<number, number>>
    ) => {
      if (targetIndices.length === 0) {
        await engine.clearDockingSelection();
        return;
      }
      const residueNames = targetIndices.map((index) => ensemble.entries[index].residueName);
      if (!pocketVisible) {
        await engine.replaceDockingLayers(
          residueNames,
          layers,
          targetIndices.map((index) => dockingColorValue(index, targetColors))
        );
      } else if (targetIndices.length === 1) {
        await engine.applyDockingPocket(residueNames[0], {
          ...pocketSettings,
          focusCamera: false,
          displayLayers: layers,
          ligandColor: dockingColorValue(targetIndices[0], targetColors),
        });
      } else if (targetIndices.length === 2) {
        await engine.replaceDockingComparison(residueNames[0], residueNames[1], {
          ...pocketSettings,
          focusCamera: false,
          displayLayers: layers,
          primaryColor: dockingColorValue(targetIndices[0], targetColors),
          secondaryColor: dockingColorValue(targetIndices[1], targetColors),
        });
      } else {
        await engine.replaceDockingSelection(residueNames, {
          ...pocketSettings,
          focusCamera: false,
          displayLayers: layers,
          ligandColor: dockingColorValue(targetIndices[0], targetColors),
          ligandColors: targetIndices.map((index) => dockingColorValue(index, targetColors)),
        });
      }
    };
    const activeIndex = nextIndices.at(-1);
    sourceRef.current =
      nextIndices.length === 1
        ? selectDockingEnsembleEntry(ensemble, nextIndices[0])
        : selectDockingEnsembleEntries(ensemble, nextIndices);
    setSelectedDockingIndices(nextIndices);
    setComparisonPrimaryIndex(nextIndices[0] ?? 0);
    setDockingEntryIndex(activeIndex ?? 0);
    setComparisonEnabled(nextIndices.length > 1);
    setTrajectoryModel(SINGLE_TRAJECTORY_MODEL);
    if (pocketVisible) {
      cancelScheduledPocketInteractionStrengthRefresh();
      pocketStrengthRequestRef.current += 1;
      pocketStrengthContextRef.current = null;
      pocketStrengthRecordsRef.current = [];
      setPocketStrengthStatus(nextIndices.length > 0 ? 'loading' : 'idle');
      clearPocketInteractionStrengthRender();
    }

    const selectionOperation = dockingSelectionQueueRef.current.then(async () => {
      if (revision !== dockingSelectionRevisionRef.current) return true;
      return runEngineAction('docking-entry', async (engine) => {
        const residueNames = nextIndices.map((index) => ensemble.entries[index].residueName);
        if (pocketVisible) await pocketStrengthRenderQueueRef.current;
        await applySelectionLayers(
          engine,
          nextIndices,
          refreshElectrostaticMap ? nonElectrostaticLayers : viewLayers,
          colors
        );
        if (refreshElectrostaticMap && nextIndices.length > 0) {
          electrostaticMapRequestRef.current += 1;
          electrostaticMapCacheRef.current = null;
          setElectrostaticMapReport(null);
          await ensureElectrostaticPotential(engine, viewLayers.includes('ligand-surface') ? residueNames : []);
          await applySelectionLayers(engine, nextIndices, viewLayers, colors);
          setViewLayers(viewLayers);
        }
      });
    });
    dockingSelectionQueueRef.current = selectionOperation.then((): void => {}).catch((): void => {});
    const succeeded = await selectionOperation;
    if (revision !== dockingSelectionRevisionRef.current) return;
    if (!succeeded) {
      electrostaticMapRequestRef.current += 1;
      const rollbackOperation = (async () => {
        const engine = engineRef.current;
        if (!engine) return false;
        try {
          if (
            previousState.electrostaticCache &&
            electrostaticMapCacheRef.current !== previousState.electrostaticCache &&
            viewLayers.some((layer) => isElectrostaticSurfaceRepresentation(layer))
          ) {
            await engine.setElectrostaticPotentials(
              previousState.electrostaticCache.potentials,
              previousState.electrostaticCache.report.color_range
            );
          }
          await applySelectionLayers(engine, previousState.selectedIndices, viewLayers, previousState.colors);
          return true;
        } catch (reason) {
          logScientificPreviewError(
            '[SynonBiomedStructureViewer] Failed to restore the previous docking selection',
            reason,
            'parse-failed'
          );
          return false;
        }
      })();
      dockingSelectionQueueRef.current = rollbackOperation.then((): void => {}).catch((): void => {});
      const engineRollbackSucceeded = await rollbackOperation;
      if (revision !== dockingSelectionRevisionRef.current) return;
      if (!engineRollbackSucceeded) {
        // Keep the optimistic selection and the already-committed Mol* graph
        // aligned if even the rollback graph cannot be restored. The failed
        // APBS layer remains explicitly retired.
        setViewLayers(nonElectrostaticLayers);
        electrostaticMapCacheRef.current = null;
        setElectrostaticMapReport(null);
        return;
      }
      sourceRef.current = previousState.source;
      setSelectedDockingIndices(previousState.selectedIndices);
      setComparisonPrimaryIndex(previousState.primaryIndex);
      setDockingEntryIndex(previousState.entryIndex);
      setComparisonEnabled(previousState.comparison);
      setDockingDepictionIndex(previousState.depictionIndex);
      setViewLayers(viewLayers);
      electrostaticMapCacheRef.current = previousState.electrostaticCache;
      setElectrostaticMapReport(previousState.electrostaticReport);
      if (pocketVisible && previousState.selectedIndices.length > 0) {
        schedulePocketInteractionStrengthRefresh({
          kind: 'docking',
          indices: previousState.selectedIndices,
          ensemble,
        });
      }
      return;
    }

    if (interactionDiagramOpenRef.current && activeIndex !== undefined) {
      void refreshInteractionDiagram(activeIndex, ensemble);
    }
    if (pocketVisible && nextIndices.length > 0) {
      schedulePocketInteractionStrengthRefresh({ kind: 'docking', indices: nextIndices, ensemble });
    }
  };

  const handleCompoundVisibilityToggle = async (index: number) => {
    const selected = selectedDockingIndices.includes(index);
    const nextIndices = selected
      ? selectedDockingIndices.filter((current) => current !== index)
      : [...selectedDockingIndices, index];
    if (!selected) setDockingDepictionIndex(index);
    await applyDockingMultiSelection(nextIndices);
  };

  const handleDockingColorSelection = async (entryIndex: number, colorIndex: number) => {
    const nextColors = { ...dockingColorIndices, [entryIndex]: colorIndex };
    setDockingColorIndices(nextColors);
    setExpandedDockingColorIndex(null);
    if (selectedDockingIndices.includes(entryIndex)) {
      await applyDockingMultiSelection(selectedDockingIndices, nextColors);
    }
  };

  const handleDockingProteinToggle = () => {
    const nextVisible = !dockingProteinVisible;
    engineRef.current?.setDockingProteinVisible(nextVisible);
    setDockingProteinVisible(nextVisible);
  };

  const handleStructureObjectToggle = (kind: StructureObjectKind) => {
    setStructureObjectVisibility((current) => {
      const nextVisible = !current[kind];
      engineRef.current?.setStructureObjectVisible(kind, nextVisible);
      return { ...current, [kind]: nextVisible };
    });
  };

  const setStructureLigandVisible = (visible: boolean) => {
    engineRef.current?.setStructureObjectVisible('ligand', visible);
    setStructureObjectVisibility((current) => ({
      ...current,
      ligand: visible,
    }));
  };

  const handleStructureLigandColorSelection = async (colorIndex: number) => {
    const option = DOCKING_COLOR_OPTIONS[colorIndex];
    if (!option) return;
    const succeeded = await runEngineAction('structure-ligand-color', (engine) =>
      engine.setStructureLigandColor(option.value)
    );
    if (!succeeded) return;
    setStructureLigandColorIndex(colorIndex);
    setStructureLigandColorExpanded(false);
  };

  const startDockingPanelResize = (axis: 'horizontal' | 'vertical', event: React.PointerEvent<HTMLDivElement>) => {
    event.preventDefault();
    const startX = event.clientX;
    const startY = event.clientY;
    const startWidth = dockingPanelWidth;
    const stageRect = stageRef.current?.getBoundingClientRect();
    const startHeight = (dockingPanelHeight ?? stageRect?.height) || 720;
    const maxWidth = Math.max(220, (stageRect?.width ?? 640) - 116);
    const maxHeight = Math.max(280, stageRect?.height ?? 720);
    let pendingWidth = startWidth;
    const handlePointerMove = (pointerEvent: PointerEvent) => {
      if (axis === 'horizontal') {
        pendingWidth = Math.max(
          RIGHT_PANEL_MIN_EXPANDED_WIDTH,
          Math.min(maxWidth, startWidth + startX - pointerEvent.clientX)
        );
        setDockingPanelWidth(pendingWidth);
      } else {
        setDockingPanelHeight(Math.max(280, Math.min(maxHeight, startHeight + pointerEvent.clientY - startY)));
      }
    };
    const stopResize = () => {
      if (axis === 'horizontal') {
        setDockingPanelWidth(pendingWidth);
        setDockingCanvasInset(pendingWidth);
      }
      window.removeEventListener('pointermove', handlePointerMove);
      window.removeEventListener('pointerup', stopResize);
      window.removeEventListener('pointercancel', stopResize);
    };
    window.addEventListener('pointermove', handlePointerMove);
    window.addEventListener('pointerup', stopResize);
    window.addEventListener('pointercancel', stopResize);
  };

  const handleDockingPanelResizeKey = (axis: 'horizontal' | 'vertical', event: React.KeyboardEvent) => {
    const delta = event.key === 'ArrowLeft' || event.key === 'ArrowUp' ? 16 : 16;
    if (axis === 'horizontal' && (event.key === 'ArrowLeft' || event.key === 'ArrowRight')) {
      event.preventDefault();
      setDockingPanelWidth((current) => {
        const nextWidth = Math.max(RIGHT_PANEL_MIN_EXPANDED_WIDTH, current + (event.key === 'ArrowLeft' ? 16 : -16));
        setDockingCanvasInset(nextWidth);
        return nextWidth;
      });
    }
    if (axis === 'vertical' && (event.key === 'ArrowUp' || event.key === 'ArrowDown')) {
      event.preventDefault();
      setDockingPanelHeight((current) => {
        const currentHeight = (current ?? stageRef.current?.getBoundingClientRect().height) || 720;
        return Math.max(280, currentHeight + (event.key === 'ArrowDown' ? delta : -delta));
      });
    }
  };

  const handleSavePose = async () => {
    await runEngineAction('save-pose', (engine) => {
      const pose = engine.exportCurrentPose();
      const blob = new Blob([pose.content], { type: 'chemical/x-pdb' });
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement('a');
      const basename = filename.replace(/\.[^.]+$/, '') || 'structure';
      anchor.href = url;
      anchor.download = `${basename}-fixed-pose.pdb`;
      anchor.rel = 'noopener';
      document.body.appendChild(anchor);
      anchor.click();
      anchor.remove();
      URL.revokeObjectURL(url);
      setActionFeedback(
        t('preview.scientific.structure.quickActions.poseSaved', {
          count: pose.atomCount,
          scope: t('preview.scientific.structure.quickActions.poseWholeScope'),
        })
      );
    });
  };

  const handleMinimize = async () => {
    await runEngineAction('minimize', async (engine) => {
      if (!operationFrameId) throw new Error('STRUCTURE_MINIMIZATION_CONTEXT_MISSING');
      const ligandResidueName = engine.getSelectedLigandResidueName();
      if (!ligandResidueName) throw new Error('STRUCTURE_MINIMIZATION_LIGAND_NOT_SELECTED');
      const authoritativePdb = format === 'pdb' && typeof sourceRef.current === 'string' ? sourceRef.current : null;
      const currentPose = authoritativePdb
        ? { content: authoritativePdb, atomCount: 0, selectionOnly: false }
        : engine.exportCurrentPose();
      const basename = (loadedFilenameRef.current || filename).replace(/\.[^.]+$/, '') || 'ligand';

      const response = await requestSynonBiomedJson<StructureMinimizationResponse>(
        `/api/frames/${encodeURIComponent(operationFrameId)}/structure-minimization`,
        {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            content: currentPose.content,
            filename: `${basename}-current-pose.pdb`,
            force_field: 'uff',
            scope: 'ligand',
            protein_environment: 'fixed',
            ligand_residue_name: ligandResidueName,
            max_iterations: 200,
            tolerance: 1e-4,
          }),
        },
        { timeoutMs: 360000 }
      );
      if (!response.ok || !response.content || !response.filename || !response.format) {
        throw new Error(response.message || 'STRUCTURE_MINIMIZATION_FAILED');
      }

      cancelScheduledPocketInteractionStrengthRefresh();
      pocketStrengthRequestRef.current += 1;
      pocketStrengthContextRef.current = null;
      pocketStrengthRecordsRef.current = [];
      await pocketStrengthRenderQueueRef.current;

      if (dockingEnsemble) {
        const nextEnsemble = mergeDockingEnsembleLigandCoordinates(
          dockingEnsemble,
          ligandResidueName,
          response.content
        );
        const minimizedIndex = nextEnsemble.entries.findIndex(
          (entry) => entry.residueName.toUpperCase() === ligandResidueName.toUpperCase()
        );
        if (minimizedIndex < 0) throw new Error('MINIMIZED_DOCKING_LIGAND_MISSING');
        const nextSelectedIndices = selectedDockingIndices.includes(minimizedIndex)
          ? selectedDockingIndices
          : [minimizedIndex];
        const selectedResidueNames = nextSelectedIndices.map((index) => nextEnsemble.entries[index].residueName);
        const nonElectrostaticLayers = viewLayers.filter((layer) => !isElectrostaticSurfaceRepresentation(layer));
        const refreshElectrostatic = nonElectrostaticLayers.length !== viewLayers.length;
        electrostaticMapRequestRef.current += 1;
        electrostaticMapCacheRef.current = null;
        setElectrostaticMapReport(null);

        await engine.loadDockingEnsemble(nextEnsemble.source, filename, 'pdb', ligandResidueName, {
          ...pocketSettings,
          displayLayers: nonElectrostaticLayers,
          focusCamera: false,
          ligandColor: dockingColorValue(minimizedIndex),
        });
        let restoredLayers = viewLayers;
        if (refreshElectrostatic) {
          try {
            await ensureElectrostaticPotential(
              engine,
              viewLayers.includes('ligand-surface') ? selectedResidueNames : []
            );
          } catch {
            // The minimized coordinates remain authoritative even if the
            // replacement APBS map is unavailable. Retire every electrostatic
            // surface rather than applying a categorical fallback under stale
            // APBS UI state.
            restoredLayers = nonElectrostaticLayers;
            setViewLayers(nonElectrostaticLayers);
          }
        }
        if (!pocketVisible) {
          await engine.replaceDockingLayers(
            selectedResidueNames,
            restoredLayers,
            nextSelectedIndices.map((index) => dockingColorValue(index))
          );
        } else if (selectedResidueNames.length === 1) {
          await engine.applyDockingPocket(selectedResidueNames[0], {
            ...pocketSettings,
            displayLayers: restoredLayers,
            focusCamera: false,
            ligandColor: dockingColorValue(nextSelectedIndices[0]),
          });
        } else if (selectedResidueNames.length === 2) {
          await engine.replaceDockingComparison(selectedResidueNames[0], selectedResidueNames[1], {
            ...pocketSettings,
            displayLayers: restoredLayers,
            focusCamera: false,
            primaryColor: dockingColorValue(nextSelectedIndices[0]),
            secondaryColor: dockingColorValue(nextSelectedIndices[1]),
          });
        } else {
          await engine.replaceDockingSelection(selectedResidueNames, {
            ...pocketSettings,
            displayLayers: restoredLayers,
            focusCamera: false,
            ligandColor: dockingColorValue(nextSelectedIndices[0]),
            ligandColors: nextSelectedIndices.map((index) => dockingColorValue(index)),
          });
        }
        engine.setDockingProteinVisible(dockingProteinVisible);
        setDockingEnsemble(nextEnsemble);
        invalidateInteractionDiagramSource();
        setDockingEntryIndex(minimizedIndex);
        setSelectedDockingIndices(nextSelectedIndices);
        setComparisonPrimaryIndex(nextSelectedIndices[0]);
        setComparisonEnabled(nextSelectedIndices.length > 1);
        sourceRef.current = selectDockingEnsembleEntries(nextEnsemble, nextSelectedIndices);
        loadedFilenameRef.current = filename;
        if (pocketVisible) {
          schedulePocketInteractionStrengthRefresh({
            kind: 'docking',
            indices: nextSelectedIndices,
            ensemble: nextEnsemble,
          });
        }
      } else {
        const nextComposition = await engine.load(response.content, response.filename, response.format);
        // Replacing coordinates invalidates both completed and in-flight 2D
        // interaction reports, even when the minimized ligand retains the same
        // residue name and atom count used by the cache key.
        invalidateInteractionDiagramSource();
        electrostaticMapRequestRef.current += 1;
        electrostaticMapCacheRef.current = null;
        setElectrostaticMapReport(null);
        setViewLayers([]);
        setStructureComposition(nextComposition);
        setStructureObjectVisibility({ protein: true, ligand: true });
        sourceRef.current = response.content;
        loadedFilenameRef.current = response.filename;
      }
      setTrajectoryModel(engine.getTrajectoryModelState());
      setSelectedLigandResidueName(undefined);
      setActionFeedback(
        t('preview.scientific.structure.quickActions.minimizationComplete', {
          count: response.ligand_atom_count ?? response.atom_count ?? 0,
        })
      );
    });
  };

  const handleBackground = (background: CanvasBackground) => {
    setCanvasBackground(background);
    engineRef.current?.setBackgroundColor(CANVAS_BACKGROUNDS[background]);
  };

  const handleBackgroundSelection = (nextBackground: CanvasBackground) => {
    if (!canInteract) return;
    handleBackground(nextBackground);
    const nextLabel =
      nextBackground === 'white'
        ? t('preview.scientific.structure.quickActions.backgroundWhite')
        : t('preview.scientific.structure.quickActions.backgroundDark');
    setActionFeedback(
      t('preview.scientific.structure.quickActions.backgroundChanged', {
        background: nextLabel,
      })
    );
  };

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;

    const annotate = () => {
      annotateMolstarControls(host, (controlId) => t(`preview.scientific.structure.controls.${controlId}` as const));
    };
    const annotateFrame = createAnimationFrameCoalescer(window, annotate);
    const observer = new MutationObserver((records) => {
      const hasAddedControls = records.some((record) =>
        [...record.addedNodes].some(
          (node) =>
            node instanceof Element &&
            (node.matches('.msp-plugin button') || Boolean(node.querySelector('.msp-plugin button')))
        )
      );
      if (hasAddedControls) annotateFrame.schedule();
    });
    annotateFrame.schedule();
    observer.observe(host, { childList: true, subtree: true });

    return () => {
      interactionDiagramRequestRef.current += 1;
      interactionDiagramOpenRef.current = false;
      observer.disconnect();
      annotateFrame.cancel();
    };
  }, [t]);

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;

    const findHelpButton = (target: EventTarget | null): HTMLButtonElement | null => {
      if (!(target instanceof Element)) return null;
      const button = target.closest<HTMLButtonElement>('button[data-synon-molstar-help]');
      return button && host.contains(button) ? button : null;
    };
    let tooltipButton: HTMLButtonElement | null = null;
    const hideTooltip = () => {
      tooltipButton = null;
      setTooltip(null);
    };
    const showTooltip = (button: HTMLButtonElement) => {
      const text = button.dataset.synonMolstarHelp?.trim();
      if (!text) return;

      const rect = button.getBoundingClientRect();
      const viewportWidth = window.innerWidth > 0 ? window.innerWidth : 1024;
      const viewportHeight = window.innerHeight > 0 ? window.innerHeight : 768;
      if (rect.bottom < 0 || rect.top > viewportHeight || rect.right < 0 || rect.left > viewportWidth) {
        hideTooltip();
        return;
      }
      const tooltipWidth = Math.min(300, viewportWidth - 16);
      const tooltipHeight = 88;
      const leftSpace = rect.left - tooltipWidth - 8;
      const rightSpace = rect.right + 8;
      const fitsRight = rightSpace + tooltipWidth <= viewportWidth - 8;
      const left = fitsRight ? rightSpace : Math.max(8, leftSpace);
      const top = Math.max(
        8,
        Math.min(viewportHeight - tooltipHeight - 8, rect.top + rect.height / 2 - tooltipHeight / 2)
      );
      tooltipButton = button;
      setTooltip({ text, left, top });
    };
    const repositionTooltip = () => {
      if (!tooltipButton || !tooltipButton.isConnected || !host.contains(tooltipButton)) {
        hideTooltip();
        return;
      }
      showTooltip(tooltipButton);
    };
    const handlePointerOver = (event: PointerEvent) => {
      const button = findHelpButton(event.target);
      const previousButton = findHelpButton(event.relatedTarget);
      if (button && button !== previousButton) showTooltip(button);
    };
    const handlePointerOut = (event: PointerEvent) => {
      if (!findHelpButton(event.relatedTarget)) hideTooltip();
    };
    const handleFocusIn = (event: FocusEvent) => {
      const button = findHelpButton(event.target);
      if (button) showTooltip(button);
    };
    const handleFocusOut = (event: FocusEvent) => {
      if (!findHelpButton(event.relatedTarget)) hideTooltip();
    };

    host.addEventListener('pointerover', handlePointerOver);
    host.addEventListener('pointerout', handlePointerOut);
    host.addEventListener('focusin', handleFocusIn);
    host.addEventListener('focusout', handleFocusOut);
    window.addEventListener('resize', repositionTooltip);
    window.addEventListener('scroll', repositionTooltip, true);

    return () => {
      host.removeEventListener('pointerover', handlePointerOver);
      host.removeEventListener('pointerout', handlePointerOut);
      host.removeEventListener('focusin', handleFocusIn);
      host.removeEventListener('focusout', handleFocusOut);
      window.removeEventListener('resize', repositionTooltip);
      window.removeEventListener('scroll', repositionTooltip, true);
    };
  }, []);

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;

    const controller = new AbortController();
    let active = true;
    let engine: MolstarStructureEngine | undefined;
    let resizeObserver: ResizeObserver | undefined;
    let resizeScheduler: MolstarViewportResizeScheduler | undefined;
    setLoading(true);
    setError(null);
    setActivePanel(null);
    setPocketVisible(false);
    setViewLayers([]);
    setSelectedLigandResidueName(undefined);
    setActionFeedback(null);
    setTrajectoryModel(SINGLE_TRAJECTORY_MODEL);
    setDockingEnsemble(null);
    cancelScheduledPocketInteractionStrengthRefresh();
    pocketStrengthRequestRef.current += 1;
    pocketStrengthContextRef.current = null;
    interactionReportCacheRef.current.clear();
    pocketStrengthRecordsRef.current = [];
    setPocketStrengthStatus('idle');
    setDockingEntryIndex(0);
    setSelectedDockingIndices([]);
    setDockingColorIndices({});
    setExpandedDockingColorIndex(null);
    setDockingProteinVisible(true);
    setStructureComposition(null);
    setStructureObjectVisibility({ protein: true, ligand: true });
    setStructureLigandColorIndex(0);
    setStructureLigandColorExpanded(false);
    setStructureLigandDepiction(null);
    setStructureLigandDepictionLoading(false);
    setLeftToolbarExpanded(false);
    setLigandDepictionExpanded(false);
    setInteractionLegendExpanded(false);
    electrostaticMapRequestRef.current += 1;
    electrostaticMapCacheRef.current = null;
    setElectrostaticMapReport(null);
    setComparisonEnabled(false);
    setComparisonPrimaryIndex(0);

    void (async () => {
      engine = await createMolstarStructureEngine(host, {
        mode: 'viewport',
        onSelectionChange: () => {
          if (active) setSelectedLigandResidueName(engine?.getSelectedLigandResidueName());
        },
      });
      if (!active) {
        engine.dispose();
        return;
      }

      engineRef.current = engine;
      setSelectedLigandResidueName(engine.getSelectedLigandResidueName());
      engine.setBackgroundColor(CANVAS_BACKGROUNDS[canvasBackground]);
      resizeScheduler = createMolstarViewportResizeScheduler(window, () => engine?.resize());
      resizeObserver = new ResizeObserver((entries) => {
        const rect = entries[0]?.contentRect;
        if (rect) resizeScheduler?.notify(rect.width, rect.height);
      });
      resizeObserver.observe(host);

      const source = await loadStructureContent({
        contentUrl,
        content,
        filename,
        format,
        signal: controller.signal,
      });
      if (!active) return;

      const ensemble = format === 'pdb' && typeof source === 'string' ? parseDockingEnsemble(source) : null;
      const initialDockingIndex = ensemble
        ? Math.max(
            0,
            ensemble.entries.findIndex((entry) => entry.rank === 1)
          )
        : 0;
      const displayedSource = ensemble ? selectDockingEnsembleEntry(ensemble, initialDockingIndex) : source;

      sourceRef.current = displayedSource;
      loadedFilenameRef.current = filename;
      let initialDockingSummary: Awaited<ReturnType<MolstarStructureEngine['loadDockingEnsemble']>> | undefined;
      if (ensemble) {
        initialDockingSummary = await engine.loadDockingEnsemble(
          ensemble.source,
          filename,
          format,
          ensemble.entries[initialDockingIndex].residueName,
          {
            ...pocketSettings,
            focusCamera: true,
            ligandColor: DOCKING_COLOR_OPTIONS[0].value,
          }
        );
      }
      const composition = !ensemble ? await engine.load(displayedSource, filename, format) : null;
      if (!active) return;
      const initialStructurePocketSummary =
        composition?.hasProtein && composition.hasLigand
          ? await engine.applyPocketFocus({
              ...pocketSettings,
              focusCamera: true,
            })
          : null;
      if (active) {
        if (ensemble) {
          if (!active) return;
          setDockingEnsemble(ensemble);
          setDockingEntryIndex(initialDockingIndex);
          setSelectedDockingIndices([initialDockingIndex]);
          setDockingColorIndices(
            Object.fromEntries(
              ensemble.entries.map((entry, index) => [index, entry.kind === 'reference' ? 1 : (index - 1) % 4])
            )
          );
          const hasInitialPocket = initialDockingSummary?.hasLigand ?? false;
          setPocketVisible(hasInitialPocket);
          setViewLayers([]);
          if (hasInitialPocket) {
            schedulePocketInteractionStrengthRefresh({
              kind: 'docking',
              indices: [initialDockingIndex],
              ensemble,
            });
          }
        }
        if (composition) {
          setStructureComposition(composition);
          if (initialStructurePocketSummary?.hasLigand) {
            setPocketVisible(true);
            setViewLayers([]);
          }
        }
        setTrajectoryModel(engine.getTrajectoryModelState());
        setLoading(false);
      }
    })().catch((reason) => {
      if (!active || controller.signal.aborted) return;
      logScientificPreviewError(
        '[SynonBiomedStructureViewer] Failed to load structure in Mol*',
        reason,
        'parse-failed'
      );
      setError(resolveScientificPreviewError(reason, 'parse-failed'));
      setLoading(false);
    });

    return () => {
      active = false;
      cancelScheduledPocketInteractionStrengthRefresh();
      pocketStrengthRequestRef.current += 1;
      pocketStrengthContextRef.current = null;
      electrostaticMapRequestRef.current += 1;
      controller.abort();
      resizeObserver?.disconnect();
      resizeScheduler?.dispose();
      if (engineRef.current === engine) engineRef.current = null;
      if (engineRef.current === null) sourceRef.current = null;
      engine?.dispose();
    };
  }, [content, contentUrl, filename, format]);

  useEffect(() => {
    const stage = stageRef.current;
    if (!stage || typeof ResizeObserver === 'undefined') return;

    const observer = new ResizeObserver((entries) => {
      const height = entries[0]?.contentRect.height;
      if (height && Number.isFinite(height)) {
        setToolbarDensity(resolveStructureToolbarDensity(height));
      }
    });
    observer.observe(stage);
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    if (!activePanel) return;

    const handlePointerDown = (event: PointerEvent) => {
      if (event.target instanceof Node && !controlDockRef.current?.contains(event.target)) setActivePanel(null);
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setActivePanel(null);
    };

    document.addEventListener('pointerdown', handlePointerDown);
    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('pointerdown', handlePointerDown);
      document.removeEventListener('keydown', handleKeyDown);
    };
  }, [activePanel]);

  const renderQuickPanel = (): React.ReactNode => {
    if (!activePanel) return null;

    const panelLabel = t('preview.scientific.structure.quickActions.moreActions');

    return (
      <div
        className='synon-biomed-molstar__quick-panel'
        role='dialog'
        aria-label={panelLabel}
        data-testid={`synon-biomed-molstar-panel-${activePanel}`}
      >
        {activePanel === 'more' && (
          <div className='synon-biomed-molstar__quick-panel-content'>
            <span className='synon-biomed-molstar__quick-section-label'>
              {t('preview.scientific.structure.quickActions.background')}
            </span>
            <div
              className='synon-biomed-molstar__quick-choice-strip'
              role='group'
              aria-label={t('preview.scientific.structure.quickActions.background')}
            >
              {BACKGROUND_SEQUENCE.map((background) => {
                const label =
                  background === 'white'
                    ? t('preview.scientific.structure.quickActions.backgroundWhite')
                    : t('preview.scientific.structure.quickActions.backgroundDark');
                return (
                  <button
                    key={background}
                    type='button'
                    className='synon-biomed-molstar__quick-chip'
                    data-active={canvasBackground === background ? 'true' : undefined}
                    aria-pressed={canvasBackground === background}
                    onClick={() => handleBackgroundSelection(background)}
                    disabled={!canInteract}
                  >
                    {label}
                  </button>
                );
              })}
            </div>
            <span className='synon-biomed-molstar__quick-section-label'>
              {t('preview.scientific.structure.quickActions.structureActionsTitle')}
            </span>
            <div
              className='synon-biomed-molstar__quick-choice-grid synon-biomed-molstar__quick-choice-grid--structure-actions'
              role='group'
              aria-label={t('preview.scientific.structure.quickActions.structureActionsTitle')}
            >
              {toolbarDensity !== 'full' && (
                <button
                  type='button'
                  className='synon-biomed-molstar__style-button synon-biomed-molstar__quick-choice--overflow'
                  data-testid='synon-biomed-molstar-overflow-minimize'
                  aria-label={t('preview.scientific.structure.quickActions.minimization')}
                  onClick={() => void handleMinimize()}
                  disabled={!canInteract || !selectedLigandResidueName}
                  title={t('preview.scientific.structure.quickActions.minimizationDescription')}
                >
                  <Lightning size={14} />
                  <span>{t('preview.scientific.structure.quickActions.minimization')}</span>
                </button>
              )}
              <button
                type='button'
                className='synon-biomed-molstar__quick-choice'
                data-testid='synon-biomed-molstar-quick-snapshot'
                onClick={() => void handleCapture()}
                disabled={!canInteract}
                title={t('preview.scientific.structure.quickActions.snapshotDescription')}
              >
                <Camera size={15} />
                <span>{t('preview.scientific.structure.quickActions.snapshot')}</span>
              </button>
              <button
                type='button'
                className='synon-biomed-molstar__quick-choice'
                data-testid='synon-biomed-molstar-save-pose'
                onClick={() => void handleSavePose()}
                disabled={!canInteract}
                title={t('preview.scientific.structure.quickActions.poseDescription')}
              >
                <PreviewOpen size={15} />
                <span>{t('preview.scientific.structure.quickActions.savePose')}</span>
              </button>
            </div>
          </div>
        )}
      </div>
    );
  };

  const dockingOptionLabel = (entry: DockingEnsembleEntry): string => {
    if (entry.kind === 'reference') {
      return `${entry.candidateId} · ${t('preview.scientific.structure.quickActions.referenceLigand')}`;
    }
    const affinity = entry.affinityKcalMol === undefined ? '' : ` · ${entry.affinityKcalMol.toFixed(3)} kcal/mol`;
    return `#${entry.rank} ${entry.candidateId} · ${t(
      'preview.scientific.structure.quickActions.poseRank'
    )} ${entry.poseRank}/${dockingPoseCount(entry)}${affinity}`;
  };

  const renderStructureObjectList = (): React.ReactNode => {
    if (!structureComposition?.objects.length) return null;
    return (
      <StructureObjectList
        objects={structureComposition.objects}
        visibility={structureObjectVisibility}
        disabled={!canInteract}
        ligandColorOptions={DOCKING_COLOR_OPTIONS.map((option) => option.value)}
        ligandColorIndex={structureLigandColorIndex}
        ligandColorExpanded={structureLigandColorExpanded}
        onToggle={handleStructureObjectToggle}
        onToggleLigandColor={() => setStructureLigandColorExpanded((expanded) => !expanded)}
        onSelectLigandColor={(index) => void handleStructureLigandColorSelection(index)}
        onSelectAll={() => setStructureLigandVisible(true)}
        onClearAll={() => setStructureLigandVisible(false)}
        labels={{
          title: t('preview.scientific.structure.quickActions.compoundList'),
          protein: t('preview.scientific.structure.quickActions.proteinObject'),
          ligand: t('preview.scientific.structure.quickActions.ligandObject'),
          toggleProtein: t('preview.scientific.structure.quickActions.toggleProteinObject'),
          toggleLigand: t('preview.scientific.structure.quickActions.toggleLigandObject'),
          atoms: (count) => t('preview.scientific.structure.quickActions.atomCount', { count }),
          ligandColor: t('preview.scientific.structure.quickActions.compoundCarbonColor', {
            compound: t('preview.scientific.structure.quickActions.ligandObject'),
          }),
          colorOptions: DOCKING_COLOR_OPTIONS.map((option) =>
            t('preview.scientific.structure.quickActions.selectCompoundCarbonColor', {
              compound: t('preview.scientific.structure.quickActions.ligandObject'),
              color: t(`preview.scientific.structure.quickActions.color.${option.key}` as const),
            })
          ),
          selectAll: t('preview.scientific.structure.quickActions.selectAllCompounds'),
          clearAll: t('preview.scientific.structure.quickActions.clearAllCompounds'),
        }}
      />
    );
  };

  const renderCompoundList = (): React.ReactNode => {
    if (!dockingEnsemble) return null;
    const orderedEntries = dockingEnsemble.entries
      .map((entry, index) => ({ entry, index }))
      .toSorted((left, right) => {
        if (left.entry.kind !== right.entry.kind) return left.entry.kind === 'reference' ? -1 : 1;
        if (left.entry.kind === 'candidate' && right.entry.kind === 'candidate') {
          return left.entry.rank - right.entry.rank || left.entry.poseRank - right.entry.poseRank;
        }
        return left.index - right.index;
      });
    return (
      <section
        className='synon-biomed-molstar__compound-browser'
        aria-label={t('preview.scientific.structure.quickActions.compoundList')}
        data-testid='synon-biomed-docking-compound-list'
      >
        <header>
          <strong>{t('preview.scientific.structure.quickActions.compoundList')}</strong>
          <span>{dockingEnsemble.entries.length + 1}</span>
        </header>
        <div className='synon-biomed-molstar__compound-list'>
          <div
            className='synon-biomed-molstar__compound-row synon-biomed-molstar__compound-row--protein'
            data-active={dockingProteinVisible ? 'true' : undefined}
          >
            <button
              type='button'
              className='synon-biomed-molstar__compound-main'
              aria-label={t('preview.scientific.structure.quickActions.toggleProteinObject')}
              aria-pressed={dockingProteinVisible}
              disabled={!canInteract}
              onClick={handleDockingProteinToggle}
            >
              <span className='synon-biomed-molstar__compound-marker' aria-hidden='true' />
              <span className='synon-biomed-molstar__compound-copy'>
                <strong>{t('preview.scientific.structure.quickActions.proteinObject')}</strong>
              </span>
              <span className='synon-biomed-molstar__compound-affinity'>—</span>
            </button>
          </div>
          {orderedEntries.map(({ entry, index }) => {
            const active = selectedDockingIndices.includes(index);
            const compoundLabel = dockingOptionLabel(entry);
            const colorIndex = dockingColorIndices[index] ?? 0;
            return (
              <React.Fragment key={`${entry.residueName}-${index}`}>
                <div className='synon-biomed-molstar__compound-row' data-active={active ? 'true' : undefined}>
                  <button
                    type='button'
                    className='synon-biomed-molstar__compound-main'
                    aria-label={t('preview.scientific.structure.quickActions.selectCompound', {
                      compound: compoundLabel,
                    })}
                    aria-pressed={active}
                    disabled={!canInteract}
                    onClick={() => void handleCompoundVisibilityToggle(index)}
                  >
                    <span className='synon-biomed-molstar__compound-marker' aria-hidden='true' />
                    <span className='synon-biomed-molstar__compound-copy'>
                      <strong>{entry.candidateId}</strong>
                    </span>
                    <span className='synon-biomed-molstar__compound-affinity'>
                      {entry.affinityKcalMol === undefined
                        ? t('preview.scientific.structure.quickActions.referenceScorePending')
                        : entry.affinityKcalMol.toFixed(3)}
                    </span>
                  </button>
                  <button
                    type='button'
                    className='synon-biomed-molstar__compound-color-trigger'
                    aria-label={t('preview.scientific.structure.quickActions.compoundCarbonColor', {
                      compound: entry.candidateId,
                    })}
                    aria-expanded={expandedDockingColorIndex === index}
                    disabled={!canInteract}
                    onClick={() => setExpandedDockingColorIndex((current) => (current === index ? null : index))}
                  >
                    <span
                      aria-hidden='true'
                      style={{
                        backgroundColor: `#${DOCKING_COLOR_OPTIONS[colorIndex].value.toString(16).padStart(6, '0')}`,
                      }}
                    />
                  </button>
                </div>
                {expandedDockingColorIndex === index && (
                  <div
                    className='synon-biomed-molstar__compound-color-palette'
                    role='group'
                    aria-label={t('preview.scientific.structure.quickActions.compoundCarbonColor', {
                      compound: entry.candidateId,
                    })}
                  >
                    {DOCKING_COLOR_OPTIONS.map((option, optionIndex) => (
                      <button
                        key={option.key}
                        type='button'
                        aria-label={t('preview.scientific.structure.quickActions.selectCompoundCarbonColor', {
                          compound: entry.candidateId,
                          color: t(`preview.scientific.structure.quickActions.color.${option.key}` as const),
                        })}
                        aria-pressed={colorIndex === optionIndex}
                        data-active={colorIndex === optionIndex ? 'true' : undefined}
                        disabled={!canInteract}
                        style={{
                          backgroundColor: `#${option.value.toString(16).padStart(6, '0')}`,
                        }}
                        onClick={() => void handleDockingColorSelection(index, optionIndex)}
                      />
                    ))}
                  </div>
                )}
              </React.Fragment>
            );
          })}
        </div>
        <footer className='synon-biomed-molstar__compound-bulk-actions'>
          <button
            type='button'
            disabled={!canInteract || selectedDockingIndices.length === dockingEnsemble.entries.length}
            onClick={() => void applyDockingMultiSelection(dockingEnsemble.entries.map((_, index) => index))}
          >
            {t('preview.scientific.structure.quickActions.selectAllCompounds')}
          </button>
          <button
            type='button'
            disabled={!canInteract || selectedDockingIndices.length === 0}
            onClick={() => void applyDockingMultiSelection([])}
          >
            {t('preview.scientific.structure.quickActions.clearAllCompounds')}
          </button>
        </footer>
      </section>
    );
  };

  const renderLigandDepiction = (): React.ReactNode => {
    const dockingEntryForDepiction =
      dockingEnsemble && dockingDepictionIndex !== null ? dockingEnsemble.entries[dockingDepictionIndex] : undefined;
    if (dockingEnsemble && !dockingEntryForDepiction) return null;
    if (!dockingEnsemble && !structureComposition?.hasLigand) return null;
    const depiction = dockingEntryForDepiction ? dockingDepictions[dockingDepictionIndex!] : structureLigandDepiction;
    const ligandLabel = dockingEntryForDepiction?.candidateId ?? structureLigandDepiction?.residueName ?? 'Ligand';
    const themedSvg = depiction?.svg ? prepareLigandDepictionSvg(depiction.svg, canvasBackground) : null;
    const depictionLoading = dockingEntryForDepiction ? dockingDepictionLoading : structureLigandDepictionLoading;
    return (
      <aside
        className='synon-biomed-molstar__ligand-depiction'
        data-background={canvasBackground}
        data-collapsed={!ligandDepictionExpanded ? 'true' : undefined}
        data-testid={
          dockingEntryForDepiction ? 'synon-biomed-docking-ligand-depiction' : 'synon-biomed-structure-ligand-depiction'
        }
        aria-label={t('preview.scientific.structure.quickActions.ligandDepiction')}
      >
        <button
          type='button'
          className='synon-biomed-molstar__ligand-depiction-toggle'
          aria-label={t(
            ligandDepictionExpanded
              ? 'preview.scientific.structure.quickActions.collapseLigandDepiction'
              : 'preview.scientific.structure.quickActions.expandLigandDepiction'
          )}
          aria-expanded={ligandDepictionExpanded}
          onClick={() => setLigandDepictionExpanded((current) => !current)}
        >
          <span>{t('preview.scientific.structure.quickActions.ligandDepiction')}</span>
          {ligandDepictionExpanded ? <Down size={11} /> : <Up size={11} />}
        </button>
        {ligandDepictionExpanded && (
          <>
            <div className='synon-biomed-molstar__ligand-depiction-canvas'>
              {themedSvg ? (
                <div
                  ref={ligandDepictionSvgRef}
                  className='synon-biomed-molstar__ligand-depiction-svg'
                  role='img'
                  aria-label={t('preview.scientific.structure.quickActions.ligandDepictionAlt', {
                    ligand: ligandLabel,
                  })}
                  // The SVG is emitted by the lockfile-pinned, same-origin RDKit
                  // worker from a validated ligand graph. Inline mounting avoids
                  // Chromium's intermittent broken data-URI image state during HMR.
                  dangerouslySetInnerHTML={{ __html: themedSvg }}
                />
              ) : depictionLoading ? (
                <Spin size={18} />
              ) : (
                <span>{t('preview.scientific.structure.quickActions.ligandDepictionUnavailable')}</span>
              )}
            </div>
            <footer>
              {dockingEntryForDepiction && selectedDockingIndices.length > 1 && (
                <button
                  type='button'
                  aria-label={t('preview.scientific.structure.quickActions.previousSelectedLigand')}
                  onClick={() => stepDockingDepiction(-1)}
                >
                  <Left size={13} />
                </button>
              )}
              <strong title={ligandLabel}>{ligandLabel}</strong>
              {dockingEntryForDepiction && selectedDockingIndices.length > 1 && (
                <button
                  type='button'
                  aria-label={t('preview.scientific.structure.quickActions.nextSelectedLigand')}
                  onClick={() => stepDockingDepiction(1)}
                >
                  <Right size={13} />
                </button>
              )}
            </footer>
          </>
        )}
      </aside>
    );
  };

  const renderPocketInteractionLegend = (): React.ReactNode => {
    if (!pocketVisible) return null;
    return (
      <aside
        className='synon-biomed-molstar__pocket-interaction-legend'
        data-background={canvasBackground}
        data-collapsed={!interactionLegendExpanded ? 'true' : undefined}
        data-testid='synon-biomed-pocket-interaction-legend'
        aria-label={t('preview.scientific.structure.quickActions.interactionLegendTitle')}
        style={{
          right: visibleRightPanelInset >= 20 ? visibleRightPanelInset + 8 : 8,
        }}
      >
        <header>
          <strong>{t('preview.scientific.structure.quickActions.interactionLegendTitle')}</strong>
          <button
            type='button'
            className='synon-biomed-molstar__pocket-interaction-legend-toggle'
            aria-label={t(
              interactionLegendExpanded
                ? 'preview.scientific.structure.quickActions.collapseInteractionLegend'
                : 'preview.scientific.structure.quickActions.expandInteractionLegend'
            )}
            aria-expanded={interactionLegendExpanded}
            onClick={() => setInteractionLegendExpanded((current) => !current)}
          >
            {interactionLegendExpanded ? <Down size={11} /> : <Up size={11} />}
          </button>
        </header>
        {interactionLegendExpanded && (
          <>
            <small data-status={pocketStrengthStatus}>
              {t(
                `preview.scientific.structure.quickActions.${
                  pocketStrengthStatus === 'loading'
                    ? 'interactionStrengthLoading'
                    : pocketStrengthStatus === 'unavailable'
                      ? 'interactionStrengthUnavailable'
                      : 'interactionStrengthScale'
                }` as const
              )}
            </small>
            {POCKET_INTERACTION_LEGEND.map((interaction) => {
              const enabled = pocketInteractionVisibility[interaction.provider] !== false;
              const label = t(
                `preview.scientific.structure.quickActions.interactionLegend.${interaction.key}` as const
              );
              return (
                <div key={interaction.key}>
                  <i aria-hidden='true' style={{ borderTopColor: interaction.color }} />
                  <span>{label}</span>
                  <button
                    type='button'
                    aria-label={`${label} ${enabled ? 'ON' : 'OFF'}`}
                    aria-pressed={enabled}
                    disabled={!canInteract}
                    onClick={() => void handlePocketInteractionVisibilityChange(interaction.provider)}
                  >
                    {enabled ? 'ON' : 'OFF'}
                  </button>
                </div>
              );
            })}
          </>
        )}
      </aside>
    );
  };

  const renderElectrostaticLegend = (): React.ReactNode => {
    if (!hasElectrostaticSurface) return null;
    const gradient = `linear-gradient(90deg, ${ELECTROSTATIC_COLOR_STOPS.negative} 0%, ${ELECTROSTATIC_COLOR_STOPS.neutral} 50%, ${ELECTROSTATIC_COLOR_STOPS.positive} 100%)`;
    return (
      <aside
        className='synon-biomed-molstar__electrostatic-legend'
        data-background={canvasBackground}
        data-collapsed={!electrostaticLegendExpanded ? 'true' : undefined}
        data-testid='synon-biomed-electrostatic-legend'
        aria-label={t('preview.scientific.structure.quickActions.electrostaticLegendTitle')}
      >
        <button
          type='button'
          className='synon-biomed-molstar__electrostatic-legend-toggle'
          aria-label={t(
            electrostaticLegendExpanded
              ? 'preview.scientific.structure.quickActions.collapseElectrostaticLegend'
              : 'preview.scientific.structure.quickActions.expandElectrostaticLegend'
          )}
          aria-expanded={electrostaticLegendExpanded}
          onClick={() => setElectrostaticLegendExpanded((current) => !current)}
        >
          <span>{t('preview.scientific.structure.quickActions.electrostaticLegendTitle')}</span>
          <i aria-hidden='true' style={{ background: gradient }} />
          {electrostaticLegendExpanded ? <Up size={11} /> : <Down size={11} />}
        </button>
        {electrostaticLegendExpanded && (
          <div className='synon-biomed-molstar__electrostatic-legend-scale'>
            <span>{t('preview.scientific.structure.quickActions.electrostaticLegendNegative')}</span>
            <i
              role='img'
              aria-label={t('preview.scientific.structure.quickActions.electrostaticLegendGradient')}
              style={{ background: gradient }}
            />
            <span>{t('preview.scientific.structure.quickActions.electrostaticLegendNeutral')}</span>
            <span>{t('preview.scientific.structure.quickActions.electrostaticLegendPositive')}</span>
            {electrostaticMapReport && (
              <>
                <small>
                  {t('preview.scientific.structure.quickActions.electrostaticLegendSummary', {
                    engine: electrostaticMapReport.engine,
                    version: electrostaticMapReport.engine_version,
                    minimum: electrostaticMapReport.color_range[0],
                    maximum: electrostaticMapReport.color_range[1],
                    unit: electrostaticMapReport.potential_unit,
                    ph: electrostaticMapReport.ph,
                  })}
                </small>
                {electrostaticMapReport.warnings.length > 0 && (
                  <small role='status'>
                    {t('preview.scientific.structure.quickActions.electrostaticLegendWarnings', {
                      warnings: electrostaticMapReport.warnings.join(' · '),
                    })}
                  </small>
                )}
              </>
            )}
          </div>
        )}
      </aside>
    );
  };

  const renderInteractionDiagramPanel = (): React.ReactNode => (
    <aside
      className={`synon-biomed-molstar__interaction-panel${
        interactionDiagramExpanded ? ' synon-biomed-molstar__interaction-panel--expanded' : ''
      }`}
      data-testid='synon-biomed-molstar-interaction-diagram'
      aria-label={t('preview.scientific.structure.quickActions.interactionDiagram')}
    >
      <header>
        <span>
          <strong>{t('preview.scientific.structure.quickActions.interactionDiagram')}</strong>
          {(interactionDiagramLoading ? interactionDiagramPendingPoseLabel : interactionDiagram?.report.pose_label) && (
            <small>
              {interactionDiagramLoading ? interactionDiagramPendingPoseLabel : interactionDiagram?.report.pose_label}
            </small>
          )}
        </span>
        <span className='synon-biomed-molstar__interaction-panel-actions'>
          <button
            type='button'
            onClick={() => setInteractionDiagramExpanded((current) => !current)}
            aria-label={t(
              interactionDiagramExpanded
                ? 'preview.scientific.structure.quickActions.restoreInteractionDiagram'
                : 'preview.scientific.structure.quickActions.expandInteractionDiagram'
            )}
            aria-pressed={interactionDiagramExpanded}
          >
            <PreviewOpen size={14} />
          </button>
          <button
            type='button'
            onClick={() => handleInteractionDiagramDownload('svg')}
            disabled={!interactionDiagram || interactionDiagramLoading}
            aria-label={t('preview.scientific.structure.quickActions.downloadInteractionDiagram')}
          >
            SVG
          </button>
          <button
            type='button'
            onClick={() => handleInteractionDiagramDownload('png')}
            disabled={!interactionDiagram || interactionDiagramLoading}
            aria-label={t('preview.scientific.structure.quickActions.downloadInteractionDiagramPng')}
          >
            PNG
          </button>
          <button
            type='button'
            onClick={handleInteractionDiagramToggle}
            aria-label={t('preview.scientific.structure.quickActions.closeInteractionDiagram')}
          >
            ×
          </button>
        </span>
      </header>
      <div className='synon-biomed-molstar__interaction-panel-body'>
        {interactionDiagram && (
          <img
            src={`data:image/svg+xml;charset=utf-8,${encodeURIComponent(interactionDiagram.svg)}`}
            alt={t('preview.scientific.structure.quickActions.interactionDiagramAlt', {
              ligand: interactionDiagram.report.ligand_label,
            })}
          />
        )}
        {interactionDiagramLoading && (
          <div
            className={`synon-biomed-molstar__interaction-panel-state${
              interactionDiagram ? ' synon-biomed-molstar__interaction-panel-state--overlay' : ''
            }`}
            role='status'
            aria-live='polite'
          >
            <Spin size={20} />
            <span>{t('preview.scientific.structure.quickActions.loadingInteractionDiagram')}</span>
          </div>
        )}
        {!interactionDiagramLoading && interactionDiagramError && (
          <div className='synon-biomed-molstar__interaction-panel-state' role='status'>
            {interactionDiagramError}
          </div>
        )}
      </div>
      {interactionDiagram && !interactionDiagramLoading && (
        <footer>
          {t('preview.scientific.structure.quickActions.interactionDiagramSummary', {
            engine: interactionDiagram.report.engine,
            release: interactionDiagram.report.engine_release,
            dpi: interactionDiagram.report.png_dpi,
          })}
        </footer>
      )}
    </aside>
  );

  const dockingPanelActive = Boolean(dockingEntry && dockingEnsemble);
  const structureObjectPanelActive = Boolean(!dockingPanelActive && structureComposition?.objects.length);
  const rightPanelActive = dockingPanelActive || structureObjectPanelActive;
  const visibleRightPanelWidth = rightPanelExpanded ? dockingPanelWidth : RIGHT_PANEL_COLLAPSED_WIDTH;
  const visibleRightPanelInset = rightPanelExpanded && dockingCanvasInset >= 20 ? dockingCanvasInset : 0;

  return (
    <section
      className='synon-biomed-molstar size-full min-h-0'
      aria-label={t('preview.scientific.structure.preview')}
      data-testid='synon-biomed-molstar-viewer'
    >
      <div
        ref={stageRef}
        className='synon-biomed-molstar__stage'
        data-layout='docked'
        data-testid='synon-biomed-molstar-stage'
        aria-busy={Boolean(busyAction)}
      >
        <div
          ref={controlDockRef}
          className='synon-biomed-molstar__control-dock'
          data-surface='integrated'
          data-collapsed={!leftToolbarExpanded ? 'true' : undefined}
          data-testid='synon-biomed-molstar-control-dock'
        >
          <div
            className='synon-biomed-molstar__quick-toolbar'
            role='toolbar'
            aria-orientation='vertical'
            aria-label={t('preview.scientific.structure.quickActions.toolbarLabel')}
            data-placement='left'
            data-testid='synon-biomed-molstar-quick-toolbar'
          >
            <div
              ref={quickActionsRef}
              className='synon-biomed-molstar__quick-actions'
              data-orientation='vertical'
              data-collapsed={!leftToolbarExpanded ? 'true' : undefined}
              data-testid='synon-biomed-molstar-quick-actions'
            >
              <button
                type='button'
                className='synon-biomed-molstar__toolbar-action'
                data-testid='synon-biomed-molstar-quick-pocket'
                aria-label={t('preview.scientific.structure.quickActions.pocket')}
                aria-pressed={pocketVisible}
                data-active={pocketVisible ? 'true' : undefined}
                onClick={() => void handlePocketAction()}
                disabled={!canInteract || !hasProteinContent || !hasLigandContent}
                title={t('preview.scientific.structure.quickActions.pocketDescription')}
              >
                {t('preview.scientific.structure.quickActions.pocket')}
              </button>
              <div
                className='synon-biomed-molstar__style-options'
                role='group'
                aria-label={t('preview.scientific.structure.quickActions.displayStyle')}
              >
                {(
                  [
                    ['initial', t('preview.scientific.structure.quickActions.viewInitial')],
                    ['ball-and-stick', t('preview.scientific.structure.quickActions.viewBallAndStick')],
                    ['line', t('preview.scientific.structure.quickActions.viewLine')],
                    ['surface', t('preview.scientific.structure.quickActions.viewSurface')],
                    ['pocket-surface', t('preview.scientific.structure.quickActions.viewPocketSurface')],
                    ['ligand-surface', t('preview.scientific.structure.quickActions.viewLigandSurface')],
                  ] as Array<[DisplayRepresentation, string]>
                ).map(([representation, label]) => (
                  <button
                    key={representation}
                    type='button'
                    className='synon-biomed-molstar__style-button'
                    data-active={
                      (
                        representation === 'initial'
                          ? !pocketVisible && viewLayers.length === 0
                          : viewLayers.includes(representation)
                      )
                        ? 'true'
                        : undefined
                    }
                    aria-label={label}
                    aria-pressed={
                      representation === 'initial'
                        ? !pocketVisible && viewLayers.length === 0
                        : viewLayers.includes(representation)
                    }
                    disabled={!canInteract || !isRepresentationAvailable(representation)}
                    onClick={() => void handleViewRepresentation(representation)}
                  >
                    {label}
                  </button>
                ))}
              </div>
              <button
                type='button'
                className='synon-biomed-molstar__toolbar-action synon-biomed-molstar__interaction-toggle'
                data-testid='synon-biomed-molstar-interaction-diagram-toggle'
                aria-label={t('preview.scientific.structure.quickActions.interactionDiagram')}
                aria-pressed={interactionDiagramOpen}
                data-active={interactionDiagramOpen ? 'true' : undefined}
                onClick={handleInteractionDiagramToggle}
                disabled={!canInteract || !hasInteractionDiagramContext}
                title={t('preview.scientific.structure.quickActions.interactionDiagramDescription')}
              >
                {toolbarDensity === 'minimal'
                  ? '2D'
                  : t('preview.scientific.structure.quickActions.interactionDiagram')}
              </button>
              {toolbarDensity === 'full' && (
                <button
                  type='button'
                  className='synon-biomed-molstar__toolbar-action'
                  data-testid='synon-biomed-molstar-minimize'
                  aria-label={t('preview.scientific.structure.quickActions.minimization')}
                  onClick={() => void handleMinimize()}
                  disabled={!canInteract || !selectedLigandResidueName}
                  title={t('preview.scientific.structure.quickActions.minimizationDescription')}
                >
                  {t('preview.scientific.structure.quickActions.minimization')}
                </button>
              )}
              {toolbarDensity === 'full' &&
                BACKGROUND_SEQUENCE.map((background) => {
                  const label =
                    background === 'white'
                      ? t('preview.scientific.structure.quickActions.backgroundWhite')
                      : t('preview.scientific.structure.quickActions.backgroundDark');
                  return (
                    <button
                      key={background}
                      type='button'
                      className='synon-biomed-molstar__toolbar-action'
                      data-testid={`synon-biomed-molstar-background-${background}`}
                      data-active={canvasBackground === background ? 'true' : undefined}
                      aria-label={label}
                      aria-pressed={canvasBackground === background}
                      onClick={() => handleBackgroundSelection(background)}
                      disabled={!canInteract}
                    >
                      {label}
                    </button>
                  );
                })}
              {toolbarDensity === 'full' && (
                <button
                  type='button'
                  className='synon-biomed-molstar__toolbar-action'
                  data-testid='synon-biomed-molstar-quick-snapshot'
                  aria-label={t('preview.scientific.structure.quickActions.snapshot')}
                  onClick={() => void handleCapture()}
                  disabled={!canInteract}
                  title={t('preview.scientific.structure.quickActions.snapshotDescription')}
                >
                  {t('preview.scientific.structure.quickActions.snapshot')}
                </button>
              )}
              {toolbarDensity === 'full' && (
                <button
                  type='button'
                  className='synon-biomed-molstar__toolbar-action'
                  data-testid='synon-biomed-molstar-save-pose'
                  aria-label={t('preview.scientific.structure.quickActions.savePose')}
                  onClick={() => void handleSavePose()}
                  disabled={!canInteract}
                  title={t('preview.scientific.structure.quickActions.poseDescription')}
                >
                  {t('preview.scientific.structure.quickActions.savePose')}
                </button>
              )}
              {trajectoryModel.count > 1 ? (
                <div
                  className='synon-biomed-molstar__model-navigator'
                  role='group'
                  aria-label={t('preview.scientific.structure.quickActions.modelNavigation')}
                  data-testid='synon-biomed-molstar-model-navigator'
                >
                  <button
                    type='button'
                    aria-label={t('preview.scientific.structure.quickActions.previousModel')}
                    disabled={!canInteract}
                    onClick={() => void handleTrajectoryModelChange(-1)}
                  >
                    ‹
                  </button>
                  <span aria-live='polite'>
                    {t('preview.scientific.structure.quickActions.modelPosition', {
                      current: trajectoryModel.index + 1,
                      total: trajectoryModel.count,
                    })}
                  </span>
                  <button
                    type='button'
                    aria-label={t('preview.scientific.structure.quickActions.nextModel')}
                    disabled={!canInteract}
                    onClick={() => void handleTrajectoryModelChange(1)}
                  >
                    ›
                  </button>
                </div>
              ) : null}
              {toolbarDensity !== 'full' && (
                <button
                  type='button'
                  className='synon-biomed-molstar__more-button'
                  data-testid='synon-biomed-molstar-more-actions'
                  aria-label={t('preview.scientific.structure.quickActions.moreActions')}
                  aria-haspopup='dialog'
                  aria-expanded={activePanel === 'more'}
                  title={t('preview.scientific.structure.quickActions.moreActionsDescription')}
                  disabled={!canInteract}
                  onClick={() => setActivePanel((current) => (current === 'more' ? null : 'more'))}
                >
                  <MoreOne size={15} />
                </button>
              )}
              <button
                type='button'
                className='synon-biomed-molstar__toolbar-collapse'
                data-testid='synon-biomed-molstar-toolbar-collapse'
                aria-label={t(
                  leftToolbarExpanded
                    ? 'preview.scientific.structure.quickActions.collapseToolbar'
                    : 'preview.scientific.structure.quickActions.expandToolbar'
                )}
                aria-expanded={leftToolbarExpanded}
                onClick={() => setLeftToolbarExpanded((current) => !current)}
              >
                {leftToolbarExpanded ? <Up size={12} /> : <Down size={12} />}
              </button>
            </div>
          </div>
          {renderQuickPanel()}
          {((busyAction && busyAction !== 'docking-entry') || actionFeedback) && (
            <div className='synon-biomed-molstar__quick-status' role='status' aria-live='polite'>
              {busyAction ? t('preview.scientific.structure.quickActions.working') : actionFeedback}
            </div>
          )}
        </div>
        <div
          className='synon-biomed-molstar__host-shell'
          style={{
            paddingRight: rightPanelActive ? visibleRightPanelInset : 0,
          }}
        >
          <div ref={hostRef} data-testid='synon-biomed-structure-canvas' className='synon-biomed-molstar__host' />
          {renderElectrostaticLegend()}
          {interactionDiagramOpen && !interactionDiagramExpanded ? renderInteractionDiagramPanel() : null}
          {renderLigandDepiction()}
          {renderPocketInteractionLegend()}
          {rightPanelActive && (
            <aside
              className='synon-biomed-molstar__docking-card'
              data-testid={
                dockingPanelActive ? 'synon-biomed-docking-score-card' : 'synon-biomed-structure-object-card'
              }
              data-collapsed={!rightPanelExpanded ? 'true' : undefined}
              aria-live='polite'
              style={{
                width: visibleRightPanelWidth,
                height: rightPanelExpanded ? (dockingPanelHeight ?? '100%') : RIGHT_PANEL_COLLAPSED_WIDTH,
              }}
            >
              <button
                type='button'
                className='synon-biomed-molstar__right-panel-toggle'
                data-testid='synon-biomed-molstar-right-panel-toggle'
                aria-label={t(
                  rightPanelExpanded
                    ? 'preview.scientific.structure.quickActions.collapseDockingPanel'
                    : 'preview.scientific.structure.quickActions.expandDockingPanel'
                )}
                aria-expanded={rightPanelExpanded}
                onClick={() => {
                  setExpandedDockingColorIndex(null);
                  setStructureLigandColorExpanded(false);
                  setRightPanelExpanded((current) => !current);
                }}
              >
                {rightPanelExpanded ? <Right size={12} /> : <Left size={12} />}
              </button>
              {rightPanelExpanded && (
                <>
                  <div
                    className='synon-biomed-molstar__docking-resize-handle synon-biomed-molstar__docking-resize-handle--width'
                    role='separator'
                    aria-orientation='vertical'
                    aria-label={t('preview.scientific.structure.quickActions.resizeDockingPanelWidth')}
                    tabIndex={0}
                    onPointerDown={(event) => startDockingPanelResize('horizontal', event)}
                    onKeyDown={(event) => handleDockingPanelResizeKey('horizontal', event)}
                  />
                  <div
                    className='synon-biomed-molstar__docking-resize-handle synon-biomed-molstar__docking-resize-handle--height'
                    role='separator'
                    aria-orientation='horizontal'
                    aria-label={t('preview.scientific.structure.quickActions.resizeDockingPanelHeight')}
                    tabIndex={0}
                    onPointerDown={(event) => startDockingPanelResize('vertical', event)}
                    onKeyDown={(event) => handleDockingPanelResizeKey('vertical', event)}
                  />
                  {dockingPanelActive ? renderCompoundList() : renderStructureObjectList()}
                </>
              )}
            </aside>
          )}
        </div>
        {loading && !error && (
          <div
            className='synon-biomed-molstar__overlay'
            aria-label={t('preview.scientific.loadingNamed', {
              name: filename,
            })}
          >
            <Spin />
          </div>
        )}
        {error && (
          <Empty
            className='synon-biomed-molstar__overlay'
            description={t(scientificPreviewErrorKey(error), {
              kind: t('preview.scientific.structure.kind'),
              ...error.details,
            })}
          />
        )}
      </div>
      <footer className='synon-biomed-molstar__instructions'>{t('preview.scientific.structure.instructions')}</footer>
      {tooltip && typeof document !== 'undefined'
        ? createPortal(
            <div
              className='synon-biomed-molstar__tooltip'
              data-testid='synon-biomed-molstar-tooltip'
              role='tooltip'
              style={{ left: tooltip.left, top: tooltip.top }}
            >
              {tooltip.text}
            </div>,
            document.body
          )
        : null}
      {interactionDiagramOpen && interactionDiagramExpanded && typeof document !== 'undefined'
        ? createPortal(renderInteractionDiagramPanel(), document.body)
        : null}
    </section>
  );
};

export default SynonBiomedStructureViewer;
