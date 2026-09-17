import type { Page } from '@playwright/test';
import { expect, test } from './officialChromeTest';
import {
  createScientificWorkspace,
  loginToScientificWorkbench,
  removeScientificWorkspace,
  showArtifactReferenceAssistantMessage,
  uploadScientificArtifact,
} from './synonBiomedScientificFixture';

const REPORT_FIXTURE = `# P5 Self-contained Artifact Report

This report was created, linked, previewed, and deleted by the same browser test.

## Result

The artifact-reference bridge opened the native preview without a new browser tab.
`;

const CSV_FIXTURE = `name,score,status
STAT6,0.91,selected
JAK1,0.73,review
`;

const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'narrow', width: 390, height: 844 },
] as const;

for (const viewport of viewports) {
  test.describe(viewport.name, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });

    test('opens self-contained artifact-reference links in the native right preview', async ({ page }) => {
      await loginToScientificWorkbench(page);
      const workspace = await createScientificWorkspace(page, `artifact-links-${viewport.name}`);
      try {
        const report = await uploadScientificArtifact(page, workspace, {
          filename: `linked-report-${viewport.name}.md`,
          contentType: 'text/markdown',
          source: REPORT_FIXTURE,
        });
        const csv = await uploadScientificArtifact(page, workspace, {
          filename: `linked-table-${viewport.name}.csv`,
          contentType: 'text/csv',
          source: CSV_FIXTURE,
        });
        await showArtifactReferenceAssistantMessage(page, workspace, [
          { versionId: report.versionId, label: 'Open report artifact' },
          { versionId: csv.versionId, label: 'Open table artifact' },
        ]);

        const pageCount = page.context().pages().length;

        const reportLink = artifactReferenceLink(page, 'Open report artifact');
        await expect(reportLink).toHaveCount(1);
        await expect(reportLink).toBeVisible();
        await reportLink.click();
        await expect(page).toHaveURL(new RegExp(`#/conversation/${workspace.conversationId}$`));
        expect(page.context().pages()).toHaveLength(pageCount);
        await expect(page.getByRole('heading', { name: 'P5 Self-contained Artifact Report' })).toBeVisible();

        const closePreview = page.getByRole('button', { name: '关闭预览' });
        await expect(closePreview).toBeVisible();
        await closePreview.click();
        const csvLink = artifactReferenceLink(page, 'Open table artifact');
        await expect(csvLink).toHaveCount(1);
        await expect(csvLink).toBeVisible();
        await csvLink.click();
        await expect(page).toHaveURL(new RegExp(`#/conversation/${workspace.conversationId}$`));
        expect(page.context().pages()).toHaveLength(pageCount);
        const tablePreview = page.getByRole('region', { name: '数据表格预览' });
        await expect(tablePreview).toBeVisible();
        await expect(tablePreview.getByRole('columnheader', { name: 'name' })).toBeVisible();
        await expect(tablePreview.getByRole('cell', { name: 'STAT6' })).toBeVisible();
      } finally {
        await removeScientificWorkspace(page, workspace);
      }
    });
  });
}

function artifactReferenceLink(page: Page, label: string) {
  return page.getByRole('link', { name: label, exact: true });
}
