/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

export type MolstarPoseAtom = {
  recordName: 'ATOM' | 'HETATM';
  serial: number;
  atomName: string;
  residueName: string;
  chainId: string;
  /** Distinguishes symmetry/model instances that share the same display chain ID. */
  chainKey?: string;
  residueNumber: number;
  alternateLocation?: string;
  insertionCode?: string;
  x: number;
  y: number;
  z: number;
  occupancy?: number;
  temperatureFactor?: number;
  element?: string;
};

const padLeft = (value: string | number, width: number): string => String(value).slice(0, width).padStart(width, ' ');

const padRight = (value: string | number, width: number): string => String(value).slice(0, width).padEnd(width, ' ');

const PDB_CHAIN_IDS = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';

const assertPdbField = (condition: boolean, field: string): void => {
  if (!condition) throw new Error(`MOLSTAR_PDB_FIELD_UNREPRESENTABLE:${field}`);
};

const normalizePdbCode = (value: string | undefined, field: string): string => {
  const normalized = value?.trim() ?? '';
  if (!normalized || normalized === '.' || normalized === '?') return ' ';
  assertPdbField(normalized.length === 1 && /^[A-Za-z0-9]$/.test(normalized), field);
  return normalized;
};

const formatBoundedNumber = (value: number, width: number, decimals: number, field: string): string => {
  assertPdbField(Number.isFinite(value), field);
  const formatted = value.toFixed(decimals);
  assertPdbField(formatted.length <= width, field);
  return formatted.padStart(width, ' ');
};

const allocatePdbChains = (atoms: readonly MolstarPoseAtom[]): Map<string, string> => {
  const assignments = new Map<string, string>();
  const preferredByIdentity = new Map<string, string>();
  for (const atom of atoms) {
    const identity = atom.chainKey?.trim() || `chain:${atom.chainId.trim() || 'A'}`;
    if (!preferredByIdentity.has(identity)) preferredByIdentity.set(identity, atom.chainId.trim());
  }
  const reservedPreferred = new Set(
    [...preferredByIdentity.values()].filter((preferred) => preferred.length === 1 && PDB_CHAIN_IDS.includes(preferred))
  );
  const owners = new Map<string, string>();
  for (const [identity, preferred] of preferredByIdentity) {
    let assigned =
      preferred.length === 1 && PDB_CHAIN_IDS.includes(preferred) && !owners.has(preferred) ? preferred : '';
    if (!assigned) {
      assigned =
        [...PDB_CHAIN_IDS].find((candidate) => !owners.has(candidate) && !reservedPreferred.has(candidate)) ??
        [...PDB_CHAIN_IDS].find((candidate) => !owners.has(candidate)) ??
        '';
    }
    assertPdbField(Boolean(assigned), 'chainId');
    assignments.set(identity, assigned);
    owners.set(assigned, identity);
  }
  return assignments;
};

const formatAtomName = (name: string, element: string): string => {
  const normalizedName = name.trim().slice(0, 4);
  const normalizedElement = element.trim();
  if (normalizedElement.length === 1 && normalizedName.length < 4) {
    return ` ${padRight(normalizedName, 3)}`;
  }
  return padRight(normalizedName, 4);
};

/**
 * Serializes current Mol* world coordinates without silently truncating PDB
 * identifiers or numeric fields. Distinct unit/chain instances are mapped to
 * distinct one-character chain IDs, which keeps symmetry copies unambiguous.
 */
export const formatMolstarPosePdb = (atoms: readonly MolstarPoseAtom[]): string => {
  assertPdbField(atoms.length <= 99_999, 'atomCount');
  const chainAssignments = allocatePdbChains(atoms);
  const lines = atoms.map((atom) => {
    assertPdbField(atom.recordName === 'ATOM' || atom.recordName === 'HETATM', 'recordName');
    assertPdbField(Number.isInteger(atom.serial) && atom.serial > 0 && atom.serial <= 99_999, 'serial');
    assertPdbField(atom.atomName.trim().length > 0 && atom.atomName.trim().length <= 4, 'atomName');
    assertPdbField(atom.residueName.trim().length > 0 && atom.residueName.trim().length <= 3, 'residueName');
    assertPdbField(Number.isInteger(atom.residueNumber), 'residueNumber');
    const residueNumberText = String(atom.residueNumber);
    assertPdbField(residueNumberText.length <= 4, 'residueNumber');
    const normalizedElement = (atom.element || '').trim().toUpperCase();
    assertPdbField(normalizedElement.length <= 2 && /^[A-Z]*$/.test(normalizedElement), 'element');

    const recordName = padRight(atom.recordName, 6);
    const atomName = formatAtomName(atom.atomName, normalizedElement);
    const alternateLocation = normalizePdbCode(atom.alternateLocation, 'alternateLocation');
    const residueName = padRight(atom.residueName.trim(), 3);
    const chainIdentity = atom.chainKey?.trim() || `chain:${atom.chainId.trim() || 'A'}`;
    const chainId = chainAssignments.get(chainIdentity);
    assertPdbField(Boolean(chainId), 'chainId');
    const residueNumber = residueNumberText.padStart(4, ' ');
    const insertionCode = normalizePdbCode(atom.insertionCode, 'insertionCode');
    const occupancy = formatBoundedNumber(atom.occupancy ?? 1, 6, 2, 'occupancy');
    const temperatureFactor = formatBoundedNumber(atom.temperatureFactor ?? 0, 6, 2, 'temperatureFactor');
    const element = padLeft(normalizedElement, 2);
    const x = formatBoundedNumber(atom.x, 8, 3, 'x');
    const y = formatBoundedNumber(atom.y, 8, 3, 'y');
    const z = formatBoundedNumber(atom.z, 8, 3, 'z');

    return `${recordName}${String(atom.serial).padStart(5, ' ')} ${atomName}${alternateLocation}${residueName} ${chainId}${residueNumber}${insertionCode}   ${x}${y}${z}${occupancy}${temperatureFactor}          ${element}`.padEnd(
      80,
      ' '
    );
  });

  return `${lines.join('\n')}\nEND\n`;
};
