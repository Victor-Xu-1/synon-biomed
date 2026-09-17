/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

export type MolstarControlHelpId =
  | 'selectAnimation'
  | 'resetZoom'
  | 'orientAxes'
  | 'resetAxes'
  | 'screenshot'
  | 'controlsPanel'
  | 'expandedViewport'
  | 'fullscreen'
  | 'settings'
  | 'illumination'
  | 'augmentedReality'
  | 'selectionMode';

type MolstarControlDescriptor = {
  id: MolstarControlHelpId;
  titles?: readonly string[];
  labels?: readonly string[];
};

const MOLSTAR_CONTROL_DESCRIPTORS: readonly MolstarControlDescriptor[] = [
  { id: 'selectAnimation', titles: ['Select Animation'] },
  {
    id: 'resetZoom',
    titles: ['Reset Zoom', 'Set camera zoom to fit the visible scene into view'],
    labels: ['Reset Zoom'],
  },
  {
    id: 'orientAxes',
    titles: ['Align principal component axes of the loaded structures to the screen axes (“lay flat”)'],
    labels: ['Orient Axes'],
  },
  {
    id: 'resetAxes',
    titles: ['Align Cartesian axes to the screen axes'],
    labels: ['Reset Axes'],
  },
  { id: 'screenshot', titles: ['Screenshot / State Snapshot'] },
  { id: 'controlsPanel', titles: ['Toggle Controls Panel'] },
  { id: 'expandedViewport', titles: ['Toggle Expanded Viewport'] },
  { id: 'fullscreen', titles: ['Fullscreen'], labels: ['Fullscreen'] },
  { id: 'settings', titles: ['Settings / Controls Info'] },
  { id: 'illumination', titles: ['Illumination'] },
  { id: 'augmentedReality', titles: ['Augmented/Virtual Reality unavailable'] },
  { id: 'selectionMode', titles: ['Toggle Selection Mode'] },
];

const MOLSTAR_CONTROL_HELP_ATTRIBUTE = 'data-synon-molstar-control';

function normalizeControlText(value: string | null | undefined): string {
  return value?.replace(/\s+/g, ' ').trim().toLowerCase() ?? '';
}

function readControlId(value: string | null | undefined): MolstarControlHelpId | undefined {
  if (!value) return undefined;
  return MOLSTAR_CONTROL_DESCRIPTORS.some((descriptor) => descriptor.id === value)
    ? (value as MolstarControlHelpId)
    : undefined;
}

export function resolveMolstarControlHelpId(
  title: string | null | undefined,
  label: string | null | undefined,
  existingId?: string | null
): MolstarControlHelpId | undefined {
  const persistedId = readControlId(existingId);
  if (persistedId) return persistedId;

  const normalizedTitle = normalizeControlText(title);
  const normalizedLabel = normalizeControlText(label);
  return MOLSTAR_CONTROL_DESCRIPTORS.find((descriptor) => {
    const titles = descriptor.titles?.map(normalizeControlText) ?? [];
    const labels = descriptor.labels?.map(normalizeControlText) ?? [];
    return titles.includes(normalizedTitle) || labels.includes(normalizedLabel);
  })?.id;
}

export function annotateMolstarControls(host: HTMLElement, describe: (id: MolstarControlHelpId) => string): void {
  const buttons = host.querySelectorAll<HTMLButtonElement>('.msp-plugin button');
  buttons.forEach((button) => {
    const controlId = resolveMolstarControlHelpId(
      button.getAttribute('title'),
      button.textContent,
      button.getAttribute(MOLSTAR_CONTROL_HELP_ATTRIBUTE)
    );
    if (!controlId) return;

    const description = describe(controlId).trim();
    if (!description) return;

    button.setAttribute(MOLSTAR_CONTROL_HELP_ATTRIBUTE, controlId);
    button.setAttribute('title', description);
    button.setAttribute('aria-label', description);
    button.setAttribute('data-synon-molstar-help', description);
  });
}
