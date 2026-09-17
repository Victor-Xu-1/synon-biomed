import React from 'react';
import type { StructureObjectKind, StructureObjectSummary } from './structureComposition';

export type StructureObjectListLabels = {
  title: string;
  protein: string;
  ligand: string;
  toggleProtein: string;
  toggleLigand: string;
  atoms: (count: number) => string;
  ligandColor: string;
  colorOptions: readonly string[];
  selectAll: string;
  clearAll: string;
};

type StructureObjectListProps = {
  objects: readonly StructureObjectSummary[];
  visibility: Readonly<Record<StructureObjectKind, boolean>>;
  labels: StructureObjectListLabels;
  disabled?: boolean;
  ligandColorOptions: readonly number[];
  ligandColorIndex: number;
  ligandColorExpanded: boolean;
  onToggle: (kind: StructureObjectKind) => void;
  onToggleLigandColor: () => void;
  onSelectLigandColor: (index: number) => void;
  onSelectAll: () => void;
  onClearAll: () => void;
};

export const StructureObjectList: React.FC<StructureObjectListProps> = ({
  objects,
  visibility,
  labels,
  disabled = false,
  ligandColorOptions,
  ligandColorIndex,
  ligandColorExpanded,
  onToggle,
  onToggleLigandColor,
  onSelectLigandColor,
  onSelectAll,
  onClearAll,
}) => (
  <section
    className='synon-biomed-molstar__compound-browser'
    aria-label={labels.title}
    data-testid='synon-biomed-structure-object-list'
  >
    <header>
      <strong>{labels.title}</strong>
      <span>{objects.length}</span>
    </header>
    <div className='synon-biomed-molstar__compound-list'>
      {objects.map((object) => {
        const visible = visibility[object.kind];
        const title = object.kind === 'protein' ? labels.protein : labels.ligand;
        const toggleLabel = object.kind === 'protein' ? labels.toggleProtein : labels.toggleLigand;
        const detail =
          object.kind === 'ligand' && object.residueNames.length > 0
            ? object.residueNames.join(', ')
            : labels.atoms(object.atomCount);
        return (
          <React.Fragment key={object.id}>
            <div
              className={`synon-biomed-molstar__compound-row${
                object.kind === 'protein' ? ' synon-biomed-molstar__compound-row--protein' : ''
              }`}
              data-active={visible ? 'true' : undefined}
            >
              <button
                type='button'
                className='synon-biomed-molstar__compound-main'
                aria-label={toggleLabel}
                aria-pressed={visible}
                disabled={disabled}
                onClick={() => onToggle(object.kind)}
              >
                <span className='synon-biomed-molstar__compound-marker' aria-hidden='true' />
                <span className='synon-biomed-molstar__compound-copy'>
                  <strong>{title}</strong>
                </span>
                <span className='synon-biomed-molstar__compound-affinity' title={detail}>
                  {detail}
                </span>
              </button>
              {object.kind === 'ligand' && (
                <button
                  type='button'
                  className='synon-biomed-molstar__compound-color-trigger'
                  aria-label={labels.ligandColor}
                  aria-expanded={ligandColorExpanded}
                  disabled={disabled}
                  onClick={onToggleLigandColor}
                >
                  <span
                    aria-hidden='true'
                    style={{
                      backgroundColor: `#${(ligandColorOptions[ligandColorIndex] ?? ligandColorOptions[0] ?? 0x0f766e)
                        .toString(16)
                        .padStart(6, '0')}`,
                    }}
                  />
                </button>
              )}
            </div>
            {object.kind === 'ligand' && ligandColorExpanded && (
              <div
                className='synon-biomed-molstar__compound-color-palette'
                role='group'
                aria-label={labels.ligandColor}
              >
                {ligandColorOptions.map((color, index) => (
                  <button
                    key={color}
                    type='button'
                    aria-label={labels.colorOptions[index]}
                    aria-pressed={ligandColorIndex === index}
                    data-active={ligandColorIndex === index ? 'true' : undefined}
                    disabled={disabled}
                    style={{ backgroundColor: `#${color.toString(16).padStart(6, '0')}` }}
                    onClick={() => onSelectLigandColor(index)}
                  />
                ))}
              </div>
            )}
          </React.Fragment>
        );
      })}
    </div>
    {objects.some((object) => object.kind === 'ligand') && (
      <footer className='synon-biomed-molstar__compound-bulk-actions'>
        <button type='button' disabled={disabled || visibility.ligand} onClick={onSelectAll}>
          {labels.selectAll}
        </button>
        <button type='button' disabled={disabled || !visibility.ligand} onClick={onClearAll}>
          {labels.clearAll}
        </button>
      </footer>
    )}
  </section>
);
