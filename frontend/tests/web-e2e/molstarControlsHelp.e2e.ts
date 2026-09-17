import { expect, test } from './officialChromeTest';
import {
  createScientificWorkspace,
  loginToScientificWorkbench,
  openScientificArtifact,
  removeScientificWorkspace,
  uploadScientificArtifact,
} from './synonBiomedScientificFixture';

const PDB_FIXTURE = `HEADER    SYNON BIOMED TOOLTIP ACCEPTANCE
ATOM      1  N   MET A   1       0.000   0.000   0.000  1.00 20.00           N
ATOM      2  CA  GLY A   2       3.800   0.000   0.000  1.00 20.00           C
END
`;

test.use({ viewport: { width: 1440, height: 900 } });

test('explains every native Mol* preview control on hover and focus', async ({ page }) => {
  await loginToScientificWorkbench(page);
  const workspace = await createScientificWorkspace(page, 'molstar-controls-help-acceptance');
  try {
    const artifact = await uploadScientificArtifact(page, workspace, {
      filename: 'controls-help.pdb',
      contentType: 'chemical/x-pdb',
      source: PDB_FIXTURE,
    });
    await openScientificArtifact(page, artifact.artifactId);

    const region = page.getByRole('region', { name: /3D 结构预览/u });
    await expect(region).toBeVisible();
    await region.getByTestId('synon-biomed-molstar-quick-view').click();
    await region.getByTestId('synon-biomed-molstar-advanced').click();
    const controls = region.locator('[data-testid="synon-biomed-structure-canvas"] button[data-synon-molstar-control]');
    await expect.poll(async () => controls.count(), { timeout: 30000 }).toBeGreaterThanOrEqual(12);

    const controlState = await controls.evaluateAll((buttons) =>
      buttons.map((button) => ({
        control: button.getAttribute('data-synon-molstar-control'),
        title: button.getAttribute('title'),
        aria: button.getAttribute('aria-label'),
        help: button.getAttribute('data-synon-molstar-help'),
      }))
    );
    const controlIds = [...new Set(controlState.map(({ control }) => control))].sort();
    expect(controlIds).toEqual([
      'augmentedReality',
      'controlsPanel',
      'expandedViewport',
      'fullscreen',
      'illumination',
      'orientAxes',
      'resetAxes',
      'resetZoom',
      'screenshot',
      'selectAnimation',
      'selectionMode',
      'settings',
    ]);
    expect(controlState.every(({ title, aria, help }) => Boolean(title && aria && help))).toBe(true);

    const selection = region.locator('button[data-synon-molstar-control="selectionMode"]');
    await selection.hover();
    await expect(page.getByRole('tooltip')).toHaveText('选择模式：开启后可点击选择结构中的原子、残基或其他对象。');
    await selection.focus();
    await expect(page.getByRole('tooltip')).toBeVisible();
  } finally {
    await removeScientificWorkspace(page, workspace);
  }
});
