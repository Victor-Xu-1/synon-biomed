import {
  createScientificWorkspace,
  inspectRenderedPixels,
  loginToScientificWorkbench,
  openScientificArtifact,
  removeScientificWorkspace,
  uploadScientificArtifact,
} from './synonBiomedScientificFixture';
import { expect, test } from './officialChromeTest';

const productionCsp =
  "default-src 'self'; base-uri 'self'; connect-src 'self' ws: wss:; font-src 'self' data:; form-action 'self'; frame-ancestors 'none'; frame-src 'self' http://mcp-app.localhost:* https://mcp-app.localhost:*; img-src 'self' data: blob:; object-src 'none'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; worker-src 'self' blob:;";
const workerCsp = "default-src 'none'; script-src 'self' 'unsafe-eval' 'wasm-unsafe-eval'; connect-src 'self';";
const smilesFixture = ['CCO ethanol', 'CC(=O)Oc1ccccc1C(=O)O aspirin', 'not_a_smiles invalid', ''].join('\n');
const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'narrow', width: 390, height: 844 },
] as const;

for (const viewport of viewports) {
  test.describe(viewport.name, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test('renders a real SMILES artifact through the isolated RDKit worker under the production CSP', async ({
      page,
    }) => {
      const pageResponse = await page.goto('/#/login', { waitUntil: 'domcontentloaded' });
      expect(pageResponse?.headers()['content-security-policy']).toBe(productionCsp);
      expect(productionCsp).not.toContain("'unsafe-eval'");
      await loginToScientificWorkbench(page);

      const pageErrors: string[] = [];
      const rdkitConsoleErrors: string[] = [];
      const rdkitRequestFailures: string[] = [];
      page.on('pageerror', (error) => pageErrors.push(error.message));
      page.on('console', (message) => {
        if (message.type() === 'error' && /content security policy|rdkit/i.test(message.text())) {
          rdkitConsoleErrors.push(message.text());
        }
      });
      page.on('requestfailed', (request) => {
        if (new URL(request.url()).pathname.startsWith('/rdkit/')) {
          rdkitRequestFailures.push(`${request.url()}:${request.failure()?.errorText ?? 'unknown'}`);
        }
      });

      const workspace = await createScientificWorkspace(page, `rdkit-${viewport.name}`);
      try {
        const artifact = await uploadScientificArtifact(page, workspace, {
          filename: `molecules-${viewport.name}.smi`,
          contentType: 'chemical/x-daylight-smiles',
          source: smilesFixture,
        });
        const workerResponse = page.waitForResponse(
          (candidate) => new URL(candidate.url()).pathname === '/rdkit/rdkit-worker.js'
        );
        const loaderResponse = page.waitForResponse(
          (candidate) => new URL(candidate.url()).pathname === '/rdkit/RDKit_minimal.js'
        );
        const wasmResponse = page.waitForResponse(
          (candidate) => new URL(candidate.url()).pathname === '/rdkit/RDKit_minimal.wasm'
        );

        await openScientificArtifact(page, artifact.artifactId);
        const region = page.getByRole('region', { name: '分子预览' });
        await expect(region).toBeVisible();
        await expect(region.getByText('RDKit 二维结构 · 2 个有效分子')).toBeVisible();
        const ethanol = region.getByRole('img', { name: 'ethanol 分子结构' });
        const aspirin = region.getByRole('img', { name: 'aspirin 分子结构' });
        await expect(ethanol).toBeVisible();
        await expect(aspirin).toBeVisible();
        await expect(region.getByRole('img', { name: 'invalid 分子结构' })).toHaveCount(0);
        await expect.poll(async () => (await inspectRenderedPixels(ethanol)).nonWhite).toBeGreaterThan(20);
        await expect.poll(async () => (await inspectRenderedPixels(aspirin)).nonWhite).toBeGreaterThan(20);
        expect(
          await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)
        ).toBe(true);

        const [loadedWorker, loadedLoader, loadedWasm] = await Promise.all([
          workerResponse,
          loaderResponse,
          wasmResponse,
        ]);
        for (const response of [loadedWorker, loadedLoader, loadedWasm]) {
          expect(response.status()).toBe(200);
          expect(response.headers()['cache-control']).toBe('no-cache, must-revalidate');
        }
        expect(loadedWorker.headers()['content-security-policy']).toBe(workerCsp);
        expect(loadedWorker.headers()['cross-origin-resource-policy']).toBe('same-origin');
        expect(loadedWasm.headers()['content-type']).toContain('application/wasm');
        expect(pageErrors).toEqual([]);
        expect(rdkitConsoleErrors).toEqual([]);
        expect(rdkitRequestFailures).toEqual([]);
      } finally {
        await removeScientificWorkspace(page, workspace);
      }
    });
  });
}
