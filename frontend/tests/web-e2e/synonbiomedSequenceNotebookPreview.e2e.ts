import { expect, test, type Locator } from '@playwright/test';
import {
  createScientificWorkspace,
  inspectRenderedPixels,
  loginToScientificWorkbench,
  openScientificArtifact,
  removeScientificWorkspace,
  uploadScientificArtifact,
  type UploadedArtifact,
} from './synonBiomedScientificFixture';

const GENBANK_FIXTURE = `LOCUS       SYNON001                 120 bp    DNA     circular SYN 11-JUL-2026
DEFINITION  Synthetic Synon Biomed test plasmid.
ACCESSION   SYNON001
VERSION     SYNON001.1
FEATURES             Location/Qualifiers
     source          1..120
                     /organism="synthetic construct"
     promoter        1..20
                     /label="synon_promoter"
     CDS             21..90
                     /gene="synA"
                     /label="synA CDS"
ORIGIN
        1 atgcatgcat gcatgcatgc atgcatgcat gcatgcatgc atgcatgcat gcatgcatgc
       61 atgcatgcat gcatgcatgc atgcatgcat gcatgcatgc atgcatgcat gcatgcatgc
//`;

const NOTEBOOK_FIXTURE = JSON.stringify({
  nbformat: 4,
  nbformat_minor: 5,
  metadata: {
    kernelspec: { display_name: 'Python 3 (Synon)', language: 'python', name: 'python3' },
    language_info: { name: 'python' },
  },
  cells: [
    { cell_type: 'markdown', source: ['# STAT6 validation\n', 'Synon Biomed notebook fixture'] },
    {
      cell_type: 'code',
      execution_count: 7,
      source: ['print("validated")'],
      outputs: [
        { output_type: 'stream', name: 'stdout', text: ['\u001b[32mvalidated\u001b[0m\n'] },
        {
          output_type: 'display_data',
          data: {
            'text/html': '<strong data-result="safe">Rich output ready</strong><script>window.__unsafe = true</script>',
          },
        },
      ],
    },
  ],
});

const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'narrow', width: 390, height: 844 },
] as const;

for (const viewport of viewports) {
  test.describe(viewport.name, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test('renders real GenBank and ipynb artifacts in the upgraded SynonAI preview system', async ({
      page,
    }, testInfo) => {
      await loginToScientificWorkbench(page);
      const workspace = await createScientificWorkspace(page, `sequence-notebook-${viewport.name}`);
      const fixtures: UploadedArtifact[] = [];
      try {
        fixtures.push(
          await uploadScientificArtifact(page, workspace, {
            filename: `sequence-e2e-${viewport.name}-${Date.now()}.gbk`,
            contentType: 'chemical/seq-na-genbank',
            source: GENBANK_FIXTURE,
          })
        );
        fixtures.push(
          await uploadScientificArtifact(page, workspace, {
            filename: `notebook-e2e-${viewport.name}-${Date.now()}.ipynb`,
            contentType: 'application/x-ipynb+json',
            source: NOTEBOOK_FIXTURE,
          })
        );

        await openScientificArtifact(page, fixtures[0].artifactId);
        const sequenceRegion = page.getByRole('region', { name: '序列预览' });
        await expect(sequenceRegion).toBeVisible();
        await expect(sequenceRegion.getByText('120 bp · 2 个注释')).toBeVisible();
        await expect(sequenceRegion.getByRole('radio', { name: '双视图' })).toBeChecked();
        const sequenceHost = sequenceRegion.getByTestId('synon-biomed-sequence-host');
        await expect(sequenceHost).toBeVisible();
        await expect(sequenceHost.locator('svg')).not.toHaveCount(0);
        await expect.poll(async () => (await inspectRenderedPixels(sequenceHost)).chromatic).toBeGreaterThan(50);
        await sequenceRegion.getByRole('radio', { name: '线性' }).click();
        await expect(sequenceRegion.getByRole('radio', { name: '线性' })).toBeChecked();
        await sequenceRegion.getByRole('slider', { name: '序列缩放' }).fill('75');
        await expect(sequenceRegion.getByText('75%')).toBeVisible();
        await page.screenshot({ path: testInfo.outputPath(`sequence-${viewport.name}.png`) });

        await openScientificArtifact(page, fixtures[1].artifactId);
        const notebookRegion = page.getByRole('region', { name: 'Notebook 预览' });
        await expect(notebookRegion).toBeVisible();
        await expect(notebookRegion.getByText('Python 3 (Synon)')).toBeVisible();
        await expect(notebookRegion.getByText('2 个单元格 · nbformat 4.5')).toBeVisible();
        await expect(notebookRegion.getByText('STAT6 validation')).toBeVisible();
        await expect(notebookRegion.getByText('print("validated")')).toBeVisible();
        await expect(notebookRegion.getByText('validated', { exact: true })).toBeVisible();
        await expect(notebookRegion.locator('[data-result="safe"]')).toHaveText('Rich output ready');
        await expect(notebookRegion.locator('script')).toHaveCount(0);
        expect(await page.evaluate(() => (window as Window & { __unsafe?: boolean }).__unsafe)).toBeUndefined();
        await assertInsideViewport(notebookRegion, viewport.width);
        await page.screenshot({ path: testInfo.outputPath(`notebook-${viewport.name}.png`) });
      } finally {
        await removeScientificWorkspace(page, workspace);
      }
    });
  });
}

async function assertInsideViewport(locator: Locator, viewportWidth: number) {
  const box = await locator.boundingBox();
  expect(box).not.toBeNull();
  expect(box!.x).toBeGreaterThanOrEqual(0);
  expect(box!.x).toBeLessThan(viewportWidth);
  expect(box!.width).toBeGreaterThan(0);
}
