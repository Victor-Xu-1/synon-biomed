import { expect, test } from '@playwright/test';
import {
  createScientificWorkspace,
  inspectRenderedPixels,
  loginToScientificWorkbench,
  openScientificArtifact,
  removeScientificWorkspace,
  uploadScientificArtifact,
} from './synonBiomedScientificFixture';

const PDB_FIXTURE = `HEADER    SYNON BIOMED READ ONLY INTERACTION ACCEPTANCE
ATOM      1  ND2 ASN A  42       0.000   0.000   0.000  1.00 20.00           N
ATOM      2  CA  GLY A  43       3.800   3.000   0.000  1.00 20.00           C
HETATM    3  O1  ATP C 101       2.800   0.000   0.000  1.00 20.00           O
HETATM    4  C1  ATP C 101       0.000   3.000   0.000  1.00 20.00           C
CONECT    3    4
TER
END
`;

const PDB_ENSEMBLE_FIXTURE = `HEADER    SYNON BIOMED MULTI MODEL ACCEPTANCE
MODEL        1
HETATM    1  C1  LIG L   1      -1.450   0.000   0.000  1.00 20.00           C
HETATM    2  C2  LIG L   1       0.000   0.000   0.000  1.00 20.00           C
HETATM    3  O1  LIG L   1       1.250   0.700   0.000  1.00 20.00           O
HETATM    4  N1  LIG L   1       0.000  -1.350   0.000  1.00 20.00           N
CONECT    1    2
CONECT    2    1    3    4
CONECT    3    2
CONECT    4    2
ENDMDL
MODEL        2
HETATM    1  C1  LIG L   1      -1.450   0.000   0.500  1.00 20.00           C
HETATM    2  C2  LIG L   1       0.000   0.000   0.000  1.00 20.00           C
HETATM    3  O1  LIG L   1       0.900   1.050  -0.300  1.00 20.00           O
HETATM    4  N1  LIG L   1       0.350  -1.250   0.450  1.00 20.00           N
CONECT    1    2
CONECT    2    1    3    4
CONECT    3    2
CONECT    4    2
ENDMDL
END
`;

test.use({ viewport: { width: 1440, height: 900 } });

test('keeps structure preview read-only and displays interactions through the real Mol* renderer', async ({ page }) => {
  const structureErrors: string[] = [];
  page.on('console', (message) => {
    if (message.type() === 'error' && /SynonBiomedStructureViewer|Mol\*|removeChild/iu.test(message.text())) {
      structureErrors.push(message.text());
    }
  });
  page.on('pageerror', (error) => {
    if (/SynonBiomedStructureViewer|Mol\*|removeChild/iu.test(error.message)) structureErrors.push(error.message);
  });

  await loginToScientificWorkbench(page);
  const workspace = await createScientificWorkspace(page, 'structure-read-only-acceptance');
  try {
    const artifact = await uploadScientificArtifact(page, workspace, {
      filename: 'interaction-view.pdb',
      contentType: 'chemical/x-pdb',
      source: PDB_FIXTURE,
    });
    await openScientificArtifact(page, artifact.artifactId);

    const region = page.getByRole('region', { name: /3D 结构预览/u });
    await expect(region).toBeVisible();
    await expect(region.getByRole('complementary', { name: /表示层编辑器/u })).toHaveCount(0);
    await expect(region.getByRole('tab', { name: /Ligand 编辑/u })).toHaveCount(0);
    await expect(region.getByRole('button', { name: /添加图层/u })).toHaveCount(0);

    // One pocket action owns residue, interaction, and distance rendering;
    // the retired independent toggles must not reintroduce competing state.
    const pocketMode = region.getByTestId('synon-biomed-molstar-quick-pocket');
    await expect(pocketMode).toBeEnabled();
    await pocketMode.click();
    await expect(pocketMode).toHaveAttribute('aria-pressed', 'true');

    const canvas = region.locator('canvas');
    await expect(canvas).toHaveCount(1);
    await expect.poll(async () => (await inspectRenderedPixels(canvas)).nonWhite).toBeGreaterThan(100);
    const quickToolbar = region.getByTestId('synon-biomed-molstar-quick-toolbar');
    expect(await quickToolbar.evaluate((element) => element.scrollWidth <= element.clientWidth + 1)).toBe(true);
    expect(structureErrors).toEqual([]);
  } finally {
    await removeScientificWorkspace(page, workspace);
  }
});

test('switches a multi-model complex inside the built-in 3D preview', async ({ page }) => {
  const structureErrors: string[] = [];
  page.on('console', (message) => {
    if (message.type() === 'error' && /SynonBiomedStructureViewer|Mol\*|removeChild/iu.test(message.text())) {
      structureErrors.push(message.text());
    }
  });
  page.on('pageerror', (error) => {
    if (/SynonBiomedStructureViewer|Mol\*|removeChild/iu.test(error.message)) structureErrors.push(error.message);
  });

  await loginToScientificWorkbench(page);
  const workspace = await createScientificWorkspace(page, 'structure-ensemble-acceptance');
  try {
    const artifact = await uploadScientificArtifact(page, workspace, {
      filename: 'ensemble.pdb',
      contentType: 'chemical/x-pdb',
      source: PDB_ENSEMBLE_FIXTURE,
    });
    await openScientificArtifact(page, artifact.artifactId);

    const region = page.getByRole('region', { name: /3D 结构预览/u });
    const modelNavigator = region.getByRole('group', { name: /结构模型/u });
    await expect(modelNavigator).toBeVisible();
    await expect(modelNavigator).toContainText('模型 1 / 2');

    await modelNavigator.getByRole('button', { name: /下一个模型/u }).click();
    await expect(modelNavigator).toContainText('模型 2 / 2');

    const canvas = region.locator('canvas');
    await expect(canvas).toHaveCount(1);
    await expect.poll(async () => (await inspectRenderedPixels(canvas)).nonWhite).toBeGreaterThan(100);
    expect(structureErrors).toEqual([]);
  } finally {
    await removeScientificWorkspace(page, workspace);
  }
});
