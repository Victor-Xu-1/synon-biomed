import { expect, test, type Route } from '@playwright/test';

test('serves the SPA entry consistently from the immutable renderer snapshot', async ({ request }) => {
  const responses = await Promise.all(Array.from({ length: 50 }, () => request.get('/')));
  const bodies = await Promise.all(responses.map((response) => response.text()));

  responses.forEach((response) => expect(response.status()).toBe(200));
  bodies.forEach((body) => {
    expect(body).toContain('<title>Synon Biomed</title>');
    expect(body).not.toContain('Index of renderer');
  });
});

test('boots the packaged entry without blocked inline scripts', async ({ page }) => {
  const policyViolations: string[] = [];
  const modulePattern = /\/assets\/index-[^/?]+\.js(?:\?.*)?$/;
  let releaseModule!: () => void;
  let observeModuleRequest!: () => void;
  const moduleRelease = new Promise<void>((resolve) => {
    releaseModule = resolve;
  });
  const moduleRequested = new Promise<void>((resolve) => {
    observeModuleRequest = resolve;
  });
  const holdApplicationModule = async (route: Route) => {
    observeModuleRequest();
    await moduleRelease;
    await route.continue();
  };
  page.on('console', (message) => {
    if (message.type() === 'error' && /Content Security Policy|violates.*script-src/i.test(message.text())) {
      policyViolations.push(message.text());
    }
  });
  await page.addInitScript(() => {
    localStorage.setItem('__synon-ai_theme', 'dark');
    document.addEventListener('securitypolicyviolation', (event) => {
      (window as Window & { __synonPolicyViolations?: string[] }).__synonPolicyViolations ??= [];
      (window as Window & { __synonPolicyViolations: string[] }).__synonPolicyViolations.push(
        `${event.violatedDirective}:${event.blockedURI}`
      );
    });
  });

  await page.route(modulePattern, holdApplicationModule);
  const navigation = page.goto('/', { waitUntil: 'domcontentloaded' });
  await moduleRequested;
  try {
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
    await expect(page.locator('body')).toHaveAttribute('arco-theme', 'dark');
  } finally {
    releaseModule();
  }
  await navigation;
  await page.unroute(modulePattern, holdApplicationModule);
  await expect(page.locator('#root > *').first()).toBeVisible();

  expect(policyViolations).toEqual([]);
  expect(
    await page.evaluate(() => (window as Window & { __synonPolicyViolations?: string[] }).__synonPolicyViolations ?? [])
  ).toEqual([]);

  await page.reload({ waitUntil: 'domcontentloaded' });
  await expect(page.locator('#root > *').first()).toBeVisible();
  expect(policyViolations).toEqual([]);
  expect(
    await page.evaluate(() => (window as Window & { __synonPolicyViolations?: string[] }).__synonPolicyViolations ?? [])
  ).toEqual([]);
});
