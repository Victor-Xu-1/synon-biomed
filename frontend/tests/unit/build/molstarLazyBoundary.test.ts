import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const engineSource = readFileSync(
  new URL(
    '../../../packages/desktop/src/renderer/pages/conversation/Preview/components/viewers/molstarStructureEngine.ts',
    import.meta.url
  ),
  'utf8'
);
const loaderSource = readFileSync(
  new URL(
    '../../../packages/desktop/src/renderer/pages/conversation/Preview/components/viewers/scientificPreviewLoaders.tsx',
    import.meta.url
  ),
  'utf8'
);

describe('Mol* lazy runtime boundary', () => {
  it('keeps the broad viewer app and MP4 encoder out while limiting lazy imports to the UI shell', () => {
    expect(loaderSource).toContain("import('./SynonBiomedStructureViewer')");
    const dynamicMolstarImports = [...engineSource.matchAll(/\bimport\(['"](molstar\/[^'"]+)['"]\)/gu)].map(
      (match) => match[1]
    );
    expect(dynamicMolstarImports).toEqual(['molstar/lib/mol-plugin-ui/index', 'molstar/lib/mol-plugin-ui/spec']);
    expect(engineSource).not.toContain('molstar/lib/apps/viewer/plugin-spec');
    expect(engineSource).not.toContain('molstar/lib/apps/viewer/presets');
    expect(engineSource).toMatch(/import\s+["']molstar\/build\/viewer\/molstar\.css["']/);
  });

  it('keeps style and pocket transitions create-before-retire instead of clearing the canvas first', () => {
    const styleTransition = engineSource.slice(
      engineSource.indexOf('const applyRepresentationStyle'),
      engineSource.indexOf('const applyPocketFocus')
    );
    const pocketTransition = engineSource.slice(
      engineSource.indexOf('const applyPocketFocus'),
      engineSource.indexOf('let ligandFocusInFlight')
    );

    expect(styleTransition).toContain('createSynonViewRepresentationPreset(representation, electrostaticVolumes)');
    expect(styleTransition).not.toContain('PresetStructureRepresentations.empty');
    expect(pocketTransition).toContain('previousComponentRefs');
    expect(pocketTransition).toContain('retainedComponentRefs');
    expect(pocketTransition).not.toContain('PresetStructureRepresentations.empty');
  });

  it('applies the non-polar hydrogen filter both on first load and when restoring the initial view', () => {
    const initialPreset = engineSource.slice(
      engineSource.indexOf('const applyRepresentationPreset'),
      engineSource.indexOf('const applyRepresentationStyle')
    );
    const initialLoad = engineSource.slice(
      engineSource.lastIndexOf('load: async'),
      engineSource.lastIndexOf('resize: ()')
    );

    expect(initialPreset).toContain('INITIAL_REPRESENTATION_PRESET_PARAMS');
    expect(initialLoad).toContain('representationPresetParams: INITIAL_REPRESENTATION_PRESET_PARAMS');
  });

  it('commits pose navigation before awaiting refinement while the initial load commits a real pocket projection', () => {
    const poseTransition = engineSource.slice(
      engineSource.indexOf('const replaceDockingLayers'),
      engineSource.indexOf('const loadDockingEnsemble')
    );
    const initialDockingLoad = engineSource.slice(
      engineSource.indexOf('const loadDockingEnsemble'),
      engineSource.indexOf('return {\n    plugin')
    );

    expect(poseTransition).toContain('activateDockingOverview');
    expect(poseTransition).toContain('replaceDockingProjection');
    expect(poseTransition.indexOf('commitDockingLigandUpdate')).toBeLessThan(
      poseTransition.indexOf('await latestDockingRefinement.queue')
    );
    expect(poseTransition.indexOf('activateDockingOverview')).toBeLessThan(
      poseTransition.indexOf('await latestDockingRefinement.queue')
    );
    expect(initialDockingLoad).not.toContain('activateDockingOverview');
    expect(initialDockingLoad).toContain('replaceDockingProjection');
    expect(engineSource).not.toContain('DOCKING_REFINEMENT_DELAY_MS');
    expect(engineSource).not.toContain('scheduleDockingRefinement');
  });

  it('reuses cached ligand graphs and commits selection before bounded pocket refinement', () => {
    const ligandTransition = engineSource.slice(
      engineSource.indexOf('const prepareDockingLigandVisual'),
      engineSource.indexOf('const activateDockingOverview')
    );
    const pocketTransition = engineSource.slice(
      engineSource.indexOf('const applyDockingPocket'),
      engineSource.indexOf('const clearDockingPocket')
    );

    expect(ligandTransition).toContain('dockingLigandVisualCache.get(key)');
    expect(ligandTransition).toContain('shouldReuseDockingLigandVisual');
    expect(ligandTransition).toContain('visible ? activeDockingLigandRefs : []');
    expect(pocketTransition).toContain('latestDockingRefinement.queue');
    expect(pocketTransition).toContain(
      'applyPocketFocus({ ...options, focusCamera: false }, undefined, structure, false)'
    );
    expect(pocketTransition.indexOf('prepareDockingLigandUpdate')).toBeLessThan(
      pocketTransition.indexOf('commitDockingLigandUpdate')
    );
    expect(pocketTransition.indexOf('commitDockingLigandUpdate')).toBeLessThan(
      pocketTransition.indexOf('replaceDockingProjection')
    );
    expect(pocketTransition).toContain('commitDockingLigandUpdate(preparedLigand, true)');
  });

  it('keeps structure preview teardown under one DOM owner', () => {
    const disposal = engineSource.slice(engineSource.lastIndexOf('dispose: () =>'), engineSource.lastIndexOf('};'));
    expect(disposal).toContain('disposeMolstarUiResources(plugin, ownedReactRoot)');
    expect(disposal).not.toContain('reactRoot?.unmount()');
    expect(disposal).not.toContain('host.replaceChildren()');
  });
});
