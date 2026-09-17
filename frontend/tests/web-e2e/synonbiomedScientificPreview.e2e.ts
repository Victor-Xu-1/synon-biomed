import { expect, test } from './officialChromeTest';
import {
  createScientificWorkspace,
  inspectRenderedPixels,
  loginToScientificWorkbench,
  openScientificArtifact,
  removeScientificWorkspace,
  uploadScientificArtifact,
} from './synonBiomedScientificFixture';

const PDB_FIXTURE = `HEADER    SYNON BIOMED SELF-CONTAINED STRUCTURE
ATOM      1  N   ALA A   1      11.104  13.207   8.554  1.00 20.00           N
ATOM      2  CA  ALA A   1      12.560  13.330   8.318  1.00 20.00           C
ATOM      3  C   ALA A   1      13.184  12.006   7.839  1.00 20.00           C
ATOM      4  O   ALA A   1      12.581  10.942   7.973  1.00 20.00           O
ATOM      5  CB  ALA A   1      13.175  14.438   7.430  1.00 20.00           C
ATOM      6  N   GLY A   2      14.397  12.073   7.292  1.00 20.00           N
ATOM      7  CA  GLY A   2      15.084  10.864   6.817  1.00 20.00           C
ATOM      8  C   GLY A   2      15.122   9.738   7.849  1.00 20.00           C
ATOM      9  O   GLY A   2      15.116   8.558   7.493  1.00 20.00           O
ATOM     10  N   SER A   3      15.173  10.084   9.135  1.00 20.00           N
ATOM     11  CA  SER A   3      15.218   9.050  10.162  1.00 20.00           C
ATOM     12  OG  SER A   3      16.527   8.565  10.392  1.00 20.00           O
TER
END
`;

const CSV_FIXTURE = [
  'cell_type,score,count,condition',
  'T cell,0.0283,42,treated',
  'B cell,0.1450,18,control',
  'Monocyte,0.3901,27,treated',
  'NK cell,0.0712,11,control',
  '',
].join('\n');

const MSA_FIXTURE = `>synon_alpha
ACDEFGHIKLMNPQRSTVWYACDEFGHIKL
>synon_beta
ACDEFGHIKLMNPQRSTVWYACDEFGHIK-
>synon_gamma
ACDEFGHIKLMNPQRSTVWYAC-EFGHIKL
>synon_delta
ACDEFGHIKLMNPQRSTVWYACDEYGHIKL
`;

const VCF_FIXTURE = [
  '##fileformat=VCFv4.2',
  '##contig=<ID=chr1,length=248956422>',
  '##INFO=<ID=DP,Number=1,Type=Integer,Description="Read depth">',
  '##FORMAT=<ID=GT,Number=1,Type=String,Description="Genotype">',
  '#CHROM\tPOS\tID\tREF\tALT\tQUAL\tFILTER\tINFO\tFORMAT\tSYNON_SAMPLE',
  'chr1\t1000000\trsSynonFixture\tA\tG\t99\tPASS\tDP=42\tGT\t0/1',
  '',
].join('\n');

const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'narrow', width: 390, height: 844 },
] as const;

for (const viewport of viewports) {
  test.describe(viewport.name, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test('renders self-contained structure, table, MSA, and genome artifacts', async ({ page }, testInfo) => {
      await loginToScientificWorkbench(page);
      const workspace = await createScientificWorkspace(page, `viewers-${viewport.name}`);
      try {
        const pdb = await uploadScientificArtifact(page, workspace, {
          filename: `structure-${viewport.name}.pdb`,
          contentType: 'chemical/x-pdb',
          source: PDB_FIXTURE,
        });
        const csv = await uploadScientificArtifact(page, workspace, {
          filename: `table-${viewport.name}.csv`,
          contentType: 'text/csv',
          source: CSV_FIXTURE,
        });
        const msa = await uploadScientificArtifact(page, workspace, {
          filename: `alignment-${viewport.name}.fasta`,
          contentType: 'text/x-fasta',
          source: MSA_FIXTURE,
        });
        const genome = await uploadScientificArtifact(page, workspace, {
          filename: `genome-${viewport.name}.vcf`,
          contentType: 'text/vcf',
          source: VCF_FIXTURE,
        });

        await openScientificArtifact(page, pdb.artifactId);
        const structureRegion = page.getByRole('region', { name: '3D 结构预览' });
        await expect(structureRegion).toBeVisible();
        await expect(structureRegion.getByTestId('synon-biomed-molstar-quick-view')).toBeEnabled();
        const structureCanvas = structureRegion.locator('canvas');
        await expect(structureCanvas).toHaveCount(1);
        await expect.poll(async () => (await inspectRenderedPixels(structureCanvas)).nonWhite).toBeGreaterThan(1_000);
        await page.screenshot({ path: testInfo.outputPath(`structure-${viewport.name}.png`) });

        await openScientificArtifact(page, csv.artifactId);
        const tableRegion = page.getByRole('region', { name: '数据表格预览' });
        await expect(tableRegion).toBeVisible();
        await expect(tableRegion.getByText('4 行 · 4 列')).toBeVisible();
        await expect(tableRegion.getByRole('columnheader', { name: 'cell_type' })).toBeVisible();
        await expect(tableRegion.getByRole('cell', { name: '0.0283' })).toBeVisible();
        await page.screenshot({ path: testInfo.outputPath(`table-${viewport.name}.png`) });

        await openScientificArtifact(page, msa.artifactId);
        const msaRegion = page.getByRole('region', { name: '多序列比对预览' });
        await expect(msaRegion).toBeVisible();
        await expect(msaRegion.getByText('4 条序列 · 30 个位点')).toBeVisible();
        await expect(msaRegion.getByRole('combobox', { name: '配色方案' })).toHaveValue('clustal2');
        const msaViewer = msaRegion.locator('nightingale-msa');
        await expect(msaViewer).toHaveCount(1);
        await expect(msaViewer).toBeVisible();
        await expect.poll(async () => (await inspectRenderedPixels(msaViewer)).nonWhite).toBeGreaterThan(1_000);
        await page.screenshot({ path: testInfo.outputPath(`msa-${viewport.name}.png`) });

        const pageOrigin = new URL(page.url()).origin;
        const externalGenomeRequests: string[] = [];
        const genomeConsoleErrors: string[] = [];
        page.on('request', (request) => {
          const target = new URL(request.url());
          if ((target.protocol === 'http:' || target.protocol === 'https:') && target.origin !== pageOrigin) {
            externalGenomeRequests.push(request.url());
          }
        });
        page.on('console', (message) => {
          if (
            message.type() === 'error' &&
            /content security policy|refused to connect|failed to initialize igv|error initializing (?:default|backup) genomes/iu.test(
              message.text()
            )
          ) {
            genomeConsoleErrors.push(message.text());
          }
        });
        const hg38ResponsePromise = page.waitForResponse(
          (response) => new URL(response.url()).pathname === '/genomes/ucsc/hg38.chrom.sizes'
        );
        await openScientificArtifact(page, genome.artifactId);
        const hg38Response = await hg38ResponsePromise;
        expect(hg38Response.status()).toBe(200);
        expect(new URL(hg38Response.url()).origin).toBe(pageOrigin);
        expect(await hg38Response.text()).toContain('chr1\t248956422');
        const genomeRegion = page.getByRole('region', { name: '基因组浏览器' });
        await expect(genomeRegion).toBeVisible();
        await expect(genomeRegion.getByRole('combobox', { name: '参考基因组' })).toHaveValue('hg38');
        const locusInput = genomeRegion.getByRole('textbox', { name: '基因组位置' });
        await locusInput.fill('chr1:999900-1000100');
        const locusButton = genomeRegion.getByRole('button', { name: '跳转到基因组位置' });
        await expect(locusButton).toBeEnabled();
        await locusButton.click();
        const igvHost = genomeRegion.getByTestId('synon-biomed-igv-host');
        await expect(igvHost).toBeVisible();
        await expect(igvHost.getByText(genome.filename, { exact: true })).toBeVisible();
        const variantViewport = igvHost.locator('[data-track-type="variant"]');
        await expect(variantViewport).toHaveCount(1);
        await expect.poll(async () => (await inspectRenderedPixels(variantViewport)).chromatic).toBeGreaterThan(10);
        await expect.poll(async () => (await inspectRenderedPixels(igvHost)).nonWhite).toBeGreaterThan(1_000);
        if (viewport.name === 'desktop') {
          const mm10ResponsePromise = page.waitForResponse(
            (response) => new URL(response.url()).pathname === '/genomes/ucsc/mm10.chrom.sizes'
          );
          await genomeRegion.getByRole('combobox', { name: '参考基因组' }).selectOption('mm10');
          const mm10Response = await mm10ResponsePromise;
          expect(mm10Response.status()).toBe(200);
          expect(new URL(mm10Response.url()).origin).toBe(pageOrigin);
          await expect(locusButton).toBeEnabled();
          await expect(igvHost.getByText(genome.filename, { exact: true })).toBeVisible();
        }
        expect(externalGenomeRequests).toEqual([]);
        expect(genomeConsoleErrors).toEqual([]);
        await page.screenshot({ path: testInfo.outputPath(`genome-${viewport.name}.png`) });
      } finally {
        await removeScientificWorkspace(page, workspace);
      }
    });
  });
}
