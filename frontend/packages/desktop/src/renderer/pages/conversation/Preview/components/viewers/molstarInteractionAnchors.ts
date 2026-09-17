/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { OrderedSet } from 'molstar/lib/mol-data/int';
import { Spheres } from 'molstar/lib/mol-geo/geometry/spheres/spheres';
import { SpheresBuilder } from 'molstar/lib/mol-geo/geometry/spheres/spheres-builder';
import { Vec3 } from 'molstar/lib/mol-math/linear-algebra';
import { Shape } from 'molstar/lib/mol-model/shape';
import { Unit, type Structure, type StructureElement } from 'molstar/lib/mol-model/structure';
import { InteractionsProvider } from 'molstar/lib/mol-model-props/computed/interactions';
import {
  InteractionFlag,
  InteractionType,
  interactionTypeLabel,
} from 'molstar/lib/mol-model-props/computed/interactions/common';
import { Features } from 'molstar/lib/mol-model-props/computed/interactions/features';
import { PluginStateObject } from 'molstar/lib/mol-plugin-state/objects';
import { StateTransformer } from 'molstar/lib/mol-state';
import { Color } from 'molstar/lib/mol-util/color';
import { ParamDefinition as PD } from 'molstar/lib/mol-util/param-definition';

export type MolstarInteractionFeatureAnchor = {
  key: string;
  position: readonly [number, number, number];
  memberCount: number;
  interactionType: InteractionType;
  tooltip: string;
};

export type MolstarInteractionFeatureAnchorData = {
  anchors: readonly MolstarInteractionFeatureAnchor[];
};

const EMPTY_INTERACTION_FEATURE_ANCHORS: MolstarInteractionFeatureAnchorData = { anchors: [] };

const INTERACTION_COLOR_BY_TYPE: Readonly<Record<InteractionType, Color>> = {
  [InteractionType.Unknown]: Color(0x94a3b8),
  [InteractionType.Ionic]: Color(0xf0c814),
  [InteractionType.CationPi]: Color(0xff8000),
  [InteractionType.PiStacking]: Color(0x8cb366),
  [InteractionType.HydrogenBond]: Color(0x2b83ba),
  [InteractionType.HalogenBond]: Color(0x40ffbf),
  [InteractionType.Hydrophobic]: Color(0x808080),
  [InteractionType.MetalCoordination]: Color(0x8c4099),
  [InteractionType.WeakHydrogenBond]: Color(0xc5ddec),
  [InteractionType.WaterBridge]: Color(0x00ccee),
};

export const selectMolstarInteractionFeatureAnchors = (
  candidates: readonly MolstarInteractionFeatureAnchor[]
): MolstarInteractionFeatureAnchor[] => {
  const anchors = new Map<string, MolstarInteractionFeatureAnchor>();
  for (const candidate of candidates) {
    // A one-atom feature already has a visible atom at the line endpoint.
    // Multi-atom features (aromatic rings, guanidinium, carboxylate, etc.)
    // terminate at a chemically meaningful centroid that otherwise appears
    // to float in empty space.
    if (candidate.memberCount <= 1 || anchors.has(candidate.key)) continue;
    anchors.set(candidate.key, candidate);
  }
  return [...anchors.values()];
};

const featureContainsLigandAtom = (
  features: Features,
  featureIndex: number,
  ligandMembers: ReadonlySet<number> | undefined
): boolean => {
  if (!ligandMembers) return false;
  for (let offset = features.offsets[featureIndex]; offset < features.offsets[featureIndex + 1]; offset += 1) {
    if (ligandMembers.has(features.members[offset])) return true;
  }
  return false;
};

export const createMolstarInteractionFeatureAnchorData = (
  structure: Structure,
  ligandLoci: StructureElement.Loci
): MolstarInteractionFeatureAnchorData => {
  const interactions = InteractionsProvider.get(structure).value;
  if (!interactions) return EMPTY_INTERACTION_FEATURE_ANCHORS;

  const ligandMembersByUnit = new Map<number, Set<number>>();
  for (const element of ligandLoci.elements) {
    if (!Unit.isAtomic(element.unit)) continue;
    const members = ligandMembersByUnit.get(element.unit.id) ?? new Set<number>();
    OrderedSet.forEach(element.indices, (index) => members.add(index));
    ligandMembersByUnit.set(element.unit.id, members);
  }

  const candidates: MolstarInteractionFeatureAnchor[] = [];
  const appendEndpoint = (unit: Unit.Atomic, featureIndex: number, interactionType: InteractionType) => {
    const features = interactions.unitsFeatures.get(unit.id);
    if (!features) return;
    const position = Features.setPosition(Vec3(), unit, featureIndex as Features.FeatureIndex, features);
    candidates.push({
      key: `${unit.id}:${featureIndex}:${interactionType}`,
      position: [position[0], position[1], position[2]],
      memberCount: features.offsets[featureIndex + 1] - features.offsets[featureIndex],
      interactionType,
      tooltip: `${interactionTypeLabel(interactionType)} interaction center`,
    });
  };
  const appendInteraction = (
    unitA: Unit.Atomic,
    featureIndexA: number,
    unitB: Unit.Atomic,
    featureIndexB: number,
    interactionType: InteractionType,
    flag: InteractionFlag
  ) => {
    if (flag === InteractionFlag.Filtered) return;
    const featuresA = interactions.unitsFeatures.get(unitA.id);
    const featuresB = interactions.unitsFeatures.get(unitB.id);
    if (!featuresA || !featuresB) return;
    const ligandA = featureContainsLigandAtom(featuresA, featureIndexA, ligandMembersByUnit.get(unitA.id));
    const ligandB = featureContainsLigandAtom(featuresB, featureIndexB, ligandMembersByUnit.get(unitB.id));
    if (ligandA === ligandB) return;
    appendEndpoint(unitA, featureIndexA, interactionType);
    appendEndpoint(unitB, featureIndexB, interactionType);
  };

  for (const unit of structure.units) {
    if (!Unit.isAtomic(unit)) continue;
    const contacts = interactions.unitsContacts.get(unit.id);
    if (!contacts) continue;
    for (let edge = 0; edge < contacts.edgeCount; edge += 1) {
      appendInteraction(
        unit,
        contacts.a[edge],
        unit,
        contacts.b[edge],
        contacts.edgeProps.type[edge],
        contacts.edgeProps.flag[edge]
      );
    }
  }

  for (const edge of interactions.contacts.edges) {
    const unitA = structure.unitMap.get(edge.unitA);
    const unitB = structure.unitMap.get(edge.unitB);
    if (!Unit.isAtomic(unitA) || !Unit.isAtomic(unitB)) continue;
    appendInteraction(unitA, edge.indexA, unitB, edge.indexB, edge.props.type, edge.props.flag);
  }

  for (const bridge of interactions.bridges) {
    const unitA = structure.unitMap.get(bridge.unitA);
    const unitB = structure.unitMap.get(bridge.unitB);
    if (!Unit.isAtomic(unitA) || !Unit.isAtomic(unitB)) continue;
    appendInteraction(unitA, bridge.indexA, unitB, bridge.indexB, bridge.props.type, bridge.props.flag);
  }

  return { anchors: selectMolstarInteractionFeatureAnchors(candidates) };
};

const buildInteractionFeatureAnchorShape = (data: MolstarInteractionFeatureAnchorData, previous?: Spheres) => {
  const builder = SpheresBuilder.create(Math.max(16, data.anchors.length), 16, previous);
  data.anchors.forEach((anchor, group) => {
    builder.add(anchor.position[0], anchor.position[1], anchor.position[2], group);
  });
  return Shape.create(
    'Interaction feature centers',
    data,
    builder.getSpheres(),
    (group) => INTERACTION_COLOR_BY_TYPE[data.anchors[group]?.interactionType ?? InteractionType.Unknown],
    () => 0.11,
    (group) => data.anchors[group]?.tooltip ?? 'Interaction center'
  );
};

const createInteractionFeatureAnchorShapeProvider = (data: MolstarInteractionFeatureAnchorData) => ({
  label: 'Interaction feature centers',
  data,
  params: PD.withDefaults(Spheres.Params, {
    quality: 'medium',
    sizeFactor: 1,
    alpha: 0.94,
  }),
  getShape: (
    _context: unknown,
    nextData: MolstarInteractionFeatureAnchorData,
    _props: PD.Values<Spheres.Params>,
    previous?: Shape<Spheres>
  ) => buildInteractionFeatureAnchorShape(nextData, previous?.geometry),
  geometryUtils: Spheres.Utils,
});

const InteractionFeatureAnchorTransform = StateTransformer.builderFactory('synon-biomed');

export const SynonBiomedInteractionFeatureAnchors = InteractionFeatureAnchorTransform({
  name: 'interaction-feature-anchors',
  display: { name: 'Interaction feature centers' },
  from: PluginStateObject.Root,
  to: PluginStateObject.Shape.Provider,
  params: {
    data: PD.Value<MolstarInteractionFeatureAnchorData>(EMPTY_INTERACTION_FEATURE_ANCHORS, { isHidden: true }),
  },
})({
  apply({ params }) {
    return new PluginStateObject.Shape.Provider(createInteractionFeatureAnchorShapeProvider(params.data), {
      label: 'Interaction feature centers',
    });
  },
  update({ b, newParams }) {
    b.data.data = newParams.data;
    return StateTransformer.UpdateResult.Updated;
  },
});
