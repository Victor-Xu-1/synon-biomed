/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { Text } from 'molstar/lib/mol-geo/geometry/text/text';
import { TextBuilder } from 'molstar/lib/mol-geo/geometry/text/text-builder';
import { Shape } from 'molstar/lib/mol-model/shape';
import { PluginStateObject } from 'molstar/lib/mol-plugin-state/objects';
import { StateTransformer } from 'molstar/lib/mol-state';
import { Color } from 'molstar/lib/mol-util/color';
import { ParamDefinition as PD } from 'molstar/lib/mol-util/param-definition';

export type MolstarInteractionStrengthRecord = {
  kind: string;
  strength_index: number;
  strength_level?: string;
  ligand_position?: readonly number[] | null;
  protein_position?: readonly number[] | null;
  ligand_label?: string;
  residue?: {
    name?: string;
    number?: number;
    chain?: string;
  };
};

export type MolstarInteractionStrengthVisibility = Readonly<Record<string, boolean | undefined>>;

export type MolstarInteractionStrengthLabel = {
  position: readonly [number, number, number];
  text: string;
  tooltip: string;
  color: Color;
};

export type MolstarInteractionStrengthLabelData = {
  labels: readonly MolstarInteractionStrengthLabel[];
};

const MAX_STRENGTH_LABELS = 32;

const INTERACTION_PROVIDER_BY_KIND: Readonly<Record<string, string>> = {
  'hydrogen-bond': 'hydrogen-bonds',
  'weak-hydrogen-bond': 'weak-hydrogen-bonds',
  hydrophobic: 'hydrophobic',
  ionic: 'ionic',
  'cation-pi': 'cation-pi',
  'pi-stacking': 'pi-stacking',
  'halogen-bond': 'halogen-bonds',
  'metal-coordination': 'metal-coordination',
  'water-bridge': 'water-bridges',
  'vdw-contact': 'hydrophobic',
};

const INTERACTION_COLOR_BY_KIND: Readonly<Record<string, Color>> = {
  'hydrogen-bond': Color(0x2b83ba),
  'weak-hydrogen-bond': Color(0xc5ddec),
  hydrophobic: Color(0x808080),
  ionic: Color(0xf0c814),
  'cation-pi': Color(0xff8000),
  'pi-stacking': Color(0x8cb366),
  'halogen-bond': Color(0x40ffbf),
  'metal-coordination': Color(0x8c4099),
  'water-bridge': Color(0x00ccee),
  'vdw-contact': Color(0x808080),
};

const isPosition = (value: readonly number[] | null | undefined): value is readonly [number, number, number] =>
  Array.isArray(value) && value.length === 3 && value.every(Number.isFinite);

const strengthLabelIdentity = (record: MolstarInteractionStrengthRecord): string => {
  const residue = record.residue;
  return [
    record.ligand_label ?? '',
    record.kind,
    residue?.chain ?? '',
    residue?.number ?? '',
    residue?.name ?? '',
  ].join(':');
};

export const createMolstarInteractionStrengthLabelData = (
  records: readonly MolstarInteractionStrengthRecord[],
  visibility: MolstarInteractionStrengthVisibility = {}
): MolstarInteractionStrengthLabelData => {
  const strongestByInteraction = new Map<string, MolstarInteractionStrengthRecord>();
  for (const record of records) {
    const provider = INTERACTION_PROVIDER_BY_KIND[record.kind];
    if (!provider || visibility[provider] === false) continue;
    if (!isPosition(record.ligand_position) || !isPosition(record.protein_position)) continue;
    if (!Number.isFinite(record.strength_index)) continue;
    const identity = strengthLabelIdentity(record);
    const current = strongestByInteraction.get(identity);
    if (!current || record.strength_index > current.strength_index) strongestByInteraction.set(identity, record);
  }

  const labels = [...strongestByInteraction.values()]
    .toSorted((left, right) => right.strength_index - left.strength_index)
    .slice(0, MAX_STRENGTH_LABELS)
    .map((record): MolstarInteractionStrengthLabel => {
      const strength = Math.max(0, Math.min(1, record.strength_index));
      const ligandPosition = record.ligand_position as readonly [number, number, number];
      const proteinPosition = record.protein_position as readonly [number, number, number];
      const residue = record.residue;
      const residueLabel = `${residue?.name ?? ''} ${residue?.number ?? ''}`.trim();
      const level = record.strength_level ? ` · ${record.strength_level}` : '';
      return {
        position: [
          (ligandPosition[0] + proteinPosition[0]) / 2,
          (ligandPosition[1] + proteinPosition[1]) / 2,
          (ligandPosition[2] + proteinPosition[2]) / 2,
        ],
        text: strength.toFixed(2),
        tooltip: `${record.kind}${residueLabel ? ` · ${residueLabel}` : ''} · relative strength ${strength.toFixed(2)}${level}`,
        color: INTERACTION_COLOR_BY_KIND[record.kind] ?? Color(0x334155),
      };
    });

  return { labels };
};

const EMPTY_STRENGTH_LABEL_DATA: MolstarInteractionStrengthLabelData = { labels: [] };

const buildInteractionStrengthLabelShape = (
  data: MolstarInteractionStrengthLabelData,
  props: PD.Values<Text.Params>,
  previous?: Text
) => {
  const builder = TextBuilder.create(props, Math.max(16, data.labels.length), 16, previous);
  data.labels.forEach((label, group) => {
    builder.add(label.text, label.position[0], label.position[1], label.position[2], 0.12, 0.52, group);
  });
  return Shape.create(
    'Relative interaction strengths',
    data,
    builder.getText(),
    (group) => data.labels[group]?.color ?? Color(0x334155),
    () => 1,
    (group) => data.labels[group]?.tooltip ?? 'Relative interaction strength'
  );
};

const createInteractionStrengthLabelShapeProvider = (data: MolstarInteractionStrengthLabelData) => ({
  label: 'Relative interaction strengths',
  data,
  params: PD.withDefaults(Text.Params, {
    sizeFactor: 1,
    fontFamily: 'sans-serif',
    fontWeight: 'bold',
    background: true,
    backgroundMargin: 0.25,
    backgroundColor: Color(0xf8fafc),
    backgroundOpacity: 0.94,
    borderWidth: 0.08,
    borderColor: Color(0x334155),
    tether: false,
    attachment: 'middle-center',
    alpha: 1,
  }),
  getShape: (
    _context: unknown,
    nextData: MolstarInteractionStrengthLabelData,
    props: PD.Values<Text.Params>,
    previous?: Shape<Text>
  ) => buildInteractionStrengthLabelShape(nextData, props, previous?.geometry),
  geometryUtils: Text.Utils,
});

const InteractionStrengthLabelTransform = StateTransformer.builderFactory('synon-biomed');

export const SynonBiomedInteractionStrengthLabels = InteractionStrengthLabelTransform({
  name: 'interaction-strength-labels',
  display: { name: 'Relative interaction strengths' },
  from: PluginStateObject.Root,
  to: PluginStateObject.Shape.Provider,
  params: {
    data: PD.Value<MolstarInteractionStrengthLabelData>(EMPTY_STRENGTH_LABEL_DATA, { isHidden: true }),
  },
})({
  apply({ params }) {
    return new PluginStateObject.Shape.Provider(createInteractionStrengthLabelShapeProvider(params.data), {
      label: 'Relative interaction strengths',
    });
  },
  update({ b, newParams }) {
    b.data.data = newParams.data;
    return StateTransformer.UpdateResult.Updated;
  },
});
