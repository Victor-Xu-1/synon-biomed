import { expect, test as base } from '@playwright/test';
import {
  assertOfficialGoogleChrome,
  requireOfficialChromeEndpoint,
  requireOfficialChromeProfileId,
} from './officialChromeContract';

const endpoint = requireOfficialChromeEndpoint(process.env.SYNON_GO_OFFICIAL_CHROME_CDP);
const profileId = requireOfficialChromeProfileId(process.env.SYNON_GO_OFFICIAL_CHROME_PROFILE_ID);

type OfficialChromeFixtures = {
  officialChromeSnapshotSuffix: void;
};

export const test = base.extend<OfficialChromeFixtures>({
  officialChromeSnapshotSuffix: [
    async ({ browser: _browser }, use, testInfo) => {
      // Playwright runs in Linux while the strict acceptance browser is
      // native Windows Chrome. Apply this as an automatic fixture so every
      // importing spec gets the correct raster baseline even when multiple
      // files share one worker and the helper module is cached.
      void _browser;
      testInfo.snapshotSuffix = 'official-windows-chrome';
      await use(undefined);
    },
    { auto: true },
  ],
  browser: [
    async ({ playwright }, use) => {
      const browser = await playwright.chromium.connectOverCDP(endpoint);
      try {
        const context = await browser.newContext();
        try {
          const page = await context.newPage();
          const identity = await page.evaluate(() => ({
            brands:
              (
                navigator as Navigator & {
                  userAgentData?: { brands?: Array<{ brand?: string }> };
                }
              ).userAgentData?.brands?.flatMap((entry) => (entry.brand ? [entry.brand] : [])) ?? [],
          }));
          const session = await context.newCDPSession(page);
          const [version, commandLine] = await Promise.all([
            session.send('Browser.getVersion'),
            session.send('Browser.getBrowserCommandLine'),
          ]);
          assertOfficialGoogleChrome(
            {
              brands: identity.brands,
              commandLine: commandLine.arguments,
              product: version.product,
            },
            profileId
          );
        } finally {
          await context.close();
        }
        await use(browser);
      } finally {
        await browser.close();
      }
    },
    { scope: 'worker' },
  ],
});

export { expect };
