import { Unit, type Structure } from 'molstar/lib/mol-model/structure';
import { isProtein } from 'molstar/lib/mol-model/structure/model/types';
import { AtomPartialCharge } from 'molstar/lib/mol-model-formats/structure/property/partial-charge';
import { Color } from 'molstar/lib/mol-util/color/index';

export type MolstarElectrostaticColorTheme = 'partial-charge' | 'formal-charge' | 'residue-charge' | 'no-charge-data';

export const ELECTROSTATIC_COLOR_STOPS = {
  negative: '#d70201',
  neutral: '#fdfdfd',
  positive: '#010efd',
} as const;

// Match the selected publication reference's sampled red-white-blue scale.
const CHARGE_COLORS = [Color(0xd70201), Color(0xfdfdfd), Color(0x010efd)];
export const ELECTROSTATIC_NEUTRAL_COLOR = Color(0xfdfdfd);

export type MolstarElectrostaticVolume = {
  ref: string;
  range: readonly [number, number];
};

const isCharge = (value: number): boolean => Number.isFinite(value) && Math.abs(value) > 1e-6;

/** Inspect represented atoms, not the full model columns shared by every component. */
export const resolveElectrostaticColorTheme = (structure: Structure): MolstarElectrostaticColorTheme => {
  let formalCharge = false;
  let protein = false;
  for (const unit of structure.units) {
    if (!Unit.isAtomic(unit)) continue;
    const partial = AtomPartialCharge.Provider.get(unit.model)?.data;
    const hierarchy = unit.model.atomicHierarchy;
    for (let index = 0; index < unit.elements.length; index += 1) {
      const atom = unit.elements[index];
      if (partial && isCharge(partial.value(atom))) return 'partial-charge';
      if (hierarchy.atoms.pdbx_formal_charge.isDefined && isCharge(hierarchy.atoms.pdbx_formal_charge.value(atom))) {
        formalCharge = true;
      }
      if (isProtein(hierarchy.derived.residue.moleculeType[unit.residueIndex[atom]])) protein = true;
    }
  }
  return formalCharge ? 'formal-charge' : protein ? 'residue-charge' : 'no-charge-data';
};

export const resolvePartialChargeScale = (structure: Structure): number => {
  let maximum = 0.05;
  for (const unit of structure.units) {
    if (!Unit.isAtomic(unit)) continue;
    const partial = AtomPartialCharge.Provider.get(unit.model)?.data;
    if (!partial) continue;
    for (let index = 0; index < unit.elements.length; index += 1) {
      const atom = unit.elements[index];
      const charge = partial.value(atom);
      if (Number.isFinite(charge)) maximum = Math.max(maximum, Math.abs(charge));
    }
  }
  return maximum;
};

export const resolveElectrostaticColorParams = (theme: MolstarElectrostaticColorTheme, partialChargeScale = 1) => {
  if (theme === 'partial-charge') {
    const scale = Math.max(0.05, partialChargeScale);
    return {
      domain: [-scale, scale] as [number, number],
      list: { kind: 'interpolate' as const, colors: CHARGE_COLORS },
    };
  }
  if (theme === 'formal-charge') {
    return {
      domain: [-3, 3] as [number, number],
      list: { kind: 'set' as const, colors: CHARGE_COLORS },
    };
  }
  return undefined;
};

export const resolveSurfaceColorSettings = (structure: Structure, volume?: MolstarElectrostaticVolume) => {
  if (volume?.ref) {
    return {
      color: 'external-volume' as const,
      colorParams: {
        volume: { ref: volume.ref },
        coloring: {
          name: 'absolute-value' as const,
          params: {
            domain: {
              name: 'custom' as const,
              params: [...volume.range] as [number, number],
            },
            list: { kind: 'interpolate' as const, colors: CHARGE_COLORS },
          },
        },
        defaultColor: ELECTROSTATIC_NEUTRAL_COLOR,
        normalOffset: 0,
        usePalette: false,
      },
    };
  }
  const theme = resolveElectrostaticColorTheme(structure);
  return {
    color: theme === 'no-charge-data' ? ('uniform' as const) : theme,
    colorParams:
      theme === 'no-charge-data'
        ? { value: ELECTROSTATIC_NEUTRAL_COLOR }
        : resolveElectrostaticColorParams(theme, theme === 'partial-charge' ? resolvePartialChargeScale(structure) : 1),
  };
};
