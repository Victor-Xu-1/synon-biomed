import { expect, test } from '@playwright/test';
import {
  createScientificWorkspace,
  loginToScientificWorkbench,
  removeScientificWorkspace,
  showArtifactReferenceAssistantMessage,
  uploadScientificArtifact,
} from './synonBiomedScientificFixture';

test.use({ viewport: { width: 1440, height: 900 } });

test('keeps the conversation visible beside a native PDB preview', async ({ page }) => {
  await loginToScientificWorkbench(page);
  const workspace = await createScientificWorkspace(page, 'conversation-preview-layout');

  try {
    const artifact = await uploadScientificArtifact(page, workspace, {
      filename: 'layout-check.pdb',
      contentType: 'chemical/x-pdb',
      source: `HEADER    SYNON BIOMED CONVERSATION PREVIEW LAYOUT\nATOM      1  CA  GLY A   1       0.000   0.000   0.000  1.00 20.00           C\nEND\n`,
    });

    await showArtifactReferenceAssistantMessage(page, workspace, [
      {
        versionId: artifact.versionId,
        label: `Open PDB preview — ${'这段对话必须完整换行显示，'.repeat(12)}`,
      },
    ]);

    const artifactPreviewButton = page.getByRole('button', {
      name: '预览 layout-check.pdb',
    });
    await expect(artifactPreviewButton).toBeVisible();
    await artifactPreviewButton.click();

    await expect(page).toHaveURL(new RegExp(`#\\/conversation\\/${workspace.conversationId}$`));
    const previewRegion = page.getByRole('region', {
      name: /3D 结构预览|3D structure preview/u,
    });
    await expect(previewRegion).toBeVisible();
    const transcriptText = page.getByText('Open PDB preview', { exact: false }).first();
    await expect(transcriptText).toBeVisible();
    const transcriptGeometry = await transcriptText.evaluate((element) => {
      const rect = element.getBoundingClientRect();
      return {
        right: rect.right,
        scrollWidth: element.scrollWidth,
        clientWidth: element.clientWidth,
      };
    });
    const layoutGeometry = await page.evaluate(() => {
      const readRect = (selector: string) => {
        const element = document.querySelector<HTMLElement>(selector);
        if (!element) return null;
        const rect = element.getBoundingClientRect();
        return { left: rect.left, right: rect.right };
      };
      const preview = readRect('[data-testid="chat-preview-panel"]');
      const chat = readRect('[data-testid="chat-preview-pane"]');
      const handle = readRect('.chat-preview-resize-handle');
      const scroller = readRect('[data-testid="message-list-scroller"]');
      const messageList = readRect('[data-testid="message-list-content"]');
      if (!preview || !chat || !handle || !scroller || !messageList) {
        throw new Error('Conversation/preview layout geometry is unavailable');
      }
      return {
        panelGap: preview.left - chat.right,
        handleLeft: handle.left,
        scrollerRight: scroller.right,
        messageListRight: messageList.right,
      };
    });
    expect(layoutGeometry.panelGap).toBeGreaterThanOrEqual(23);
    expect(layoutGeometry.handleLeft - layoutGeometry.scrollerRight).toBeGreaterThanOrEqual(2);
    expect(layoutGeometry.messageListRight - layoutGeometry.scrollerRight).toBeLessThanOrEqual(1);
    expect(transcriptGeometry.scrollWidth - transcriptGeometry.clientWidth).toBeLessThanOrEqual(1);
  } finally {
    await removeScientificWorkspace(page, workspace);
  }
});
