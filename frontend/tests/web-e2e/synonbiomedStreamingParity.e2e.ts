import { Buffer } from 'node:buffer';
import { expect, test } from './officialChromeTest';
import {
  createScientificWorkspace,
  loginToScientificWorkbench,
  removeScientificWorkspace,
  uploadScientificArtifact,
} from './synonBiomedScientificFixture';
import {
  beginSynonGoStreamingParity,
  beginSynonGoStreamingRecovery,
  beginSynonGoTranscriptStream,
  completeSynonGoStreamingParity,
  completeSynonGoStreamingRecovery,
} from './synonGoFrameFixture';

const PNG_FIXTURE = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=',
  'base64'
);
const STREAMING_COPY = {
  thinking: '正在核对实验依据、工具结果与最终文件的精确版本。',
  search: '检索 CRBN 结构证据',
  compute: '计算候选化合物评分',
  final:
    '分析完成。结果文件已按生成顺序放在本条回答之后。\n\n![CRBN binding overview](data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=)',
};
const RECOVERY_COPY = {
  intro: '正在执行可恢复分析。',
  first: '初次分析',
  recovery: '初次尝试失败；保留失败证据并改用独立重试。',
  second: '恢复分析',
  final: '恢复完成，初次失败证据仍保留。',
};

const viewports = [
  { name: 'desktop', width: 1440, height: 900, dark: false, reducedMotion: false },
  { name: 'narrow', width: 390, height: 844, dark: false, reducedMotion: false },
  { name: 'desktop-dark-reduced', width: 1440, height: 900, dark: true, reducedMotion: true },
] as const;

for (const viewport of viewports) {
  test.describe(viewport.name, () => {
    test.use({
      viewport: { width: viewport.width, height: viewport.height },
      colorScheme: viewport.dark ? 'dark' : 'light',
      reducedMotion: viewport.reducedMotion ? 'reduce' : 'no-preference',
    });

    test('renders one localized thinking, tool, image, and exact final-artifact stream', async ({ page }, testInfo) => {
      if (viewport.dark || viewport.reducedMotion) {
        await page.emulateMedia({
          colorScheme: viewport.dark ? 'dark' : 'light',
          reducedMotion: viewport.reducedMotion ? 'reduce' : 'no-preference',
        });
      }
      await loginToScientificWorkbench(page);
      const workspace = await createScientificWorkspace(page, `streaming-parity-${viewport.name}`);

      try {
        const report = await uploadScientificArtifact(page, workspace, {
          filename: `final-report-${viewport.name}.md`,
          contentType: 'text/markdown',
          source: '# CRBN final report\n\nValidated.',
        });
        const image = await uploadScientificArtifact(page, workspace, {
          filename: `binding-overview-${viewport.name}.png`,
          contentType: 'image/png',
          source: PNG_FIXTURE,
        });
        const table = await uploadScientificArtifact(page, workspace, {
          filename: `candidate-scores-${viewport.name}.csv`,
          contentType: 'text/csv',
          source: 'candidate,score\nA,0.91\nB,0.82\n',
        });

        const stream = beginSynonGoTranscriptStream(workspace.conversationId);
        await page.goto(`/#/conversation/${workspace.conversationId}`, {
          waitUntil: 'domcontentloaded',
        });
        if (viewport.dark) {
          await page.evaluate(() => {
            document.documentElement.dataset.theme = 'dark';
            document.body.setAttribute('arco-theme', 'dark');
          });
          await expect(page.locator('body')).toHaveAttribute('arco-theme', 'dark');
        }
        await expect(page.getByTestId('message-list-scroller')).toBeVisible();
        beginSynonGoStreamingParity(stream, STREAMING_COPY);

        const thinking = page.getByTestId('thinking-block');
        await expect(thinking).toBeVisible();
        const thinkingToggle = thinking.getByRole('button', { name: /思考/ });
        await expect(thinkingToggle).toHaveAttribute('aria-expanded', 'false');
        await thinkingToggle.click();
        await expect(thinking.getByTestId('thinking-body')).toHaveAttribute('aria-hidden', 'false');
        const thinkingSurface = thinking.getByTestId('thinking-content');
        await expect(thinkingSurface).toBeVisible();
        expect(
          await thinkingSurface.evaluate((element) => {
            const style = window.getComputedStyle(element);
            return {
              backgroundColor: style.backgroundColor,
              backgroundImage: style.backgroundImage,
            };
          })
        ).toEqual({
          backgroundColor: 'rgba(101, 84, 233, 0.05)',
          backgroundImage: 'none',
        });

        const toolGroups = page.locator('.tool-group-summary');
        await expect(toolGroups).toHaveCount(2);
        await expect(page.getByRole('button', { name: /2 步/ })).toHaveCount(0);
        const searchTool = page.getByRole('button', { name: /搜索资料/ });
        await expect(searchTool).toBeVisible();
        await expect(toolGroups.filter({ has: searchTool })).toHaveClass(/tool-group-summary--single/);
        const computeTool = page.getByRole('button', { name: /开展分析/ });
        await expect(computeTool).toBeVisible();
        const computeGroup = toolGroups.filter({ has: computeTool });
        await expect(computeGroup).toHaveClass(/tool-group-summary--single/);
        if (viewport.dark) {
          await expect
            .poll(() => computeGroup.evaluate((element) => getComputedStyle(element).backgroundColor))
            .toBe('rgba(255, 255, 255, 0.04)');
        }
        if (viewport.reducedMotion) {
          expect(await page.evaluate(() => matchMedia('(prefers-reduced-motion: reduce)').matches)).toBe(true);
          expect(await computeTool.evaluate((element) => getComputedStyle(element).transitionDuration)).toBe('0s');
          expect(
            await thinkingToggle
              .getByText('思考', { exact: true })
              .evaluate((element) => getComputedStyle(element).animationName)
          ).toBe('none');
        }
        const computeStep = computeTool.locator('..');
        await expect(computeStep).toHaveClass(/tool-step--running/);
        await expect(computeStep.getByTestId('tool-public-output')).toHaveCount(0);

        completeSynonGoStreamingParity(
          stream,
          STREAMING_COPY,
          [report, image, table].map((artifact) => ({
            artifactId: artifact.artifactId,
            versionId: artifact.versionId,
          }))
        );
        await expect(thinking).toHaveAttribute('data-active', 'false');
        await expect(computeStep).toHaveClass(/tool-step--completed/);
        await expect(computeStep.getByTestId('tool-public-output')).toContainText('candidate 3/3');

        const finalText = page.getByText('分析完成。结果文件已按生成顺序放在本条回答之后。', {
          exact: true,
        });
        await expect(finalText).toBeVisible({ timeout: 10_000 });
        await expect(page.getByRole('img', { name: 'CRBN binding overview' })).toBeVisible();

        await page.getByTestId('message-list-scroller').evaluate((scroller) => {
          scroller.scrollTop = scroller.scrollHeight;
          scroller.dispatchEvent(new Event('scroll', { bubbles: true }));
        });
        const fileTray = page.getByTestId('message-artifact-references');
        await expect(fileTray).toBeVisible();
        await expect(fileTray.getByText('已生成 · 3')).toBeVisible();
        const cards = fileTray.getByRole('button', { name: /^预览/ });
        await expect(cards).toHaveCount(3);
        const firstCard = cards.nth(0);
        await expect(firstCard).toHaveAttribute('aria-label', `预览 ${report.filename}`);
        const box = await firstCard.boundingBox();
        expect(box?.width).toBe(126);
        expect(box?.height).toBe(79);
        if (viewport.dark) {
          expect(
            await firstCard.evaluate((card) => {
              const metadata = card.querySelector<HTMLElement>('.message-scientific-files__metadata');
              const name = card.querySelector<HTMLElement>('.message-scientific-files__name');
              if (!metadata || !name) throw new Error('Artifact card metadata is missing');
              return {
                background: getComputedStyle(metadata).backgroundColor,
                text: getComputedStyle(name).color,
              };
            })
          ).toEqual({
            background: 'rgb(31, 30, 28)',
            text: 'rgb(241, 239, 235)',
          });
        }

        await page.screenshot({
          path: testInfo.outputPath(`streaming-parity-${viewport.name}.png`),
        });
      } finally {
        await removeScientificWorkspace(page, workspace);
      }
    });
  });
}

test.describe('desktop recovery', () => {
  test.use({ viewport: { width: 1440, height: 900 }, colorScheme: 'light', reducedMotion: 'no-preference' });

  test('retains a failed operation while the recovery operation completes across refresh', async ({ page }) => {
    await loginToScientificWorkbench(page);
    const workspace = await createScientificWorkspace(page, 'streaming-recovery');

    try {
      const stream = beginSynonGoTranscriptStream(workspace.conversationId);
      await page.goto(`/#/conversation/${workspace.conversationId}`, { waitUntil: 'domcontentloaded' });
      await expect(page.getByTestId('message-list-scroller')).toBeVisible();

      beginSynonGoStreamingRecovery(stream, RECOVERY_COPY);
      await expect(page.getByText(RECOVERY_COPY.intro, { exact: true })).toBeVisible();
      await expect(page.getByText(RECOVERY_COPY.recovery, { exact: true })).toBeVisible();

      const toolGroups = page.locator('.tool-group-summary');
      await expect(toolGroups).toHaveCount(2);
      const firstStep = toolGroups.nth(0).locator('.tool-step');
      const secondStep = toolGroups.nth(1).locator('.tool-step');
      await expect(firstStep).toHaveClass(/tool-step--error/);
      await firstStep.getByTestId('tool-chip').click();
      await firstStep.getByRole('button', { name: /显示输出/ }).click();
      await expect(firstStep).toContainText('upstream timeout');
      await expect(secondStep).toHaveClass(/tool-step--running/);

      completeSynonGoStreamingRecovery(stream, RECOVERY_COPY);
      await expect(secondStep).toHaveClass(/tool-step--completed/);
      await expect(secondStep).toContainText('candidate 3/3');
      await expect(page.getByText(RECOVERY_COPY.final, { exact: true })).toBeVisible();
      await expect(firstStep).toHaveClass(/tool-step--error/);
      await expect(firstStep).toContainText('candidate 1/3 retained');

      await page.reload({ waitUntil: 'domcontentloaded' });
      await expect(page.getByTestId('message-list-scroller')).toBeVisible();
      const replayedGroups = page.locator('.tool-group-summary');
      await expect(replayedGroups).toHaveCount(2);
      const replayedFirst = replayedGroups.nth(0).locator('.tool-step');
      const replayedSecond = replayedGroups.nth(1).locator('.tool-step');
      await expect(replayedFirst).toHaveClass(/tool-step--error/);
      if ((await replayedFirst.getByTestId('tool-chip').getAttribute('aria-expanded')) !== 'true') {
        await replayedFirst.getByTestId('tool-chip').click();
      }
      const replayedFailureOutput = replayedFirst.getByRole('button', { name: /显示输出/ });
      if ((await replayedFailureOutput.getAttribute('aria-expanded')) !== 'true') {
        await replayedFailureOutput.click();
      }
      await expect(replayedFirst).toContainText('upstream timeout');
      await expect(replayedSecond).toHaveClass(/tool-step--completed/);
      if ((await replayedSecond.getByTestId('tool-chip').getAttribute('aria-expanded')) !== 'true') {
        await replayedSecond.getByTestId('tool-chip').click();
      }
      const replayedSuccessOutput = replayedSecond.getByRole('button', { name: /显示输出/ });
      if ((await replayedSuccessOutput.getAttribute('aria-expanded')) !== 'true') {
        await replayedSuccessOutput.click();
      }
      await expect(replayedSecond).toContainText('candidate 3/3');
      await expect(replayedGroups.nth(0).getByTestId('tool-chip')).toHaveCount(1);
      await expect(replayedGroups.nth(1).getByTestId('tool-chip')).toHaveCount(1);
    } finally {
      await removeScientificWorkspace(page, workspace);
    }
  });
});
