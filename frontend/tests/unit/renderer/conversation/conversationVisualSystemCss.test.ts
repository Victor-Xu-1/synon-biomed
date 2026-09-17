import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

import { describe, expect, it } from 'vitest';

const workspaceThemeCssPath = fileURLToPath(
  new URL('../../../../packages/desktop/src/renderer/styles/workspace-theme.css', import.meta.url)
);
const shadowViewPath = fileURLToPath(
  new URL('../../../../packages/desktop/src/renderer/components/Markdown/ShadowView.tsx', import.meta.url)
);
const sendboxCssPath = fileURLToPath(
  new URL('../../../../packages/desktop/src/renderer/components/chat/SendBox/sendbox.css', import.meta.url)
);
const layoutCssPath = fileURLToPath(
  new URL('../../../../packages/desktop/src/renderer/styles/layout.css', import.meta.url)
);
const messagesCssPath = fileURLToPath(
  new URL('../../../../packages/desktop/src/renderer/pages/conversation/Messages/messages.css', import.meta.url)
);
const conversationQueueDockCssPath = fileURLToPath(
  new URL(
    '../../../../packages/desktop/src/renderer/pages/conversation/platforms/acp/conversationQueueDock.css',
    import.meta.url
  )
);
const userMessageActionsCssPath = fileURLToPath(
  new URL(
    '../../../../packages/desktop/src/renderer/pages/conversation/Messages/components/SynonBiomedUserMessageActions.css',
    import.meta.url
  )
);
const messageTextPath = fileURLToPath(
  new URL(
    '../../../../packages/desktop/src/renderer/pages/conversation/Messages/components/MessageText.tsx',
    import.meta.url
  )
);

describe('conversation visual system CSS', () => {
  it('keeps shared theme tokens separate from component-owned transcript and decision styles', () => {
    const css = readFileSync(workspaceThemeCssPath, 'utf8');
    const transcriptCss = readFileSync(messagesCssPath, 'utf8');
    const askUserCss = readFileSync(
      new URL(
        '../../../../packages/desktop/src/renderer/components/synonBiomed/runtime/AskUserCard.css',
        import.meta.url
      ),
      'utf8'
    );

    expect(css).toContain('--conversation-font-size: 15px');
    expect(css).toContain('--conversation-stream-accent: #6f6b65');
    expect(css).toContain('--conversation-stream-accent-soft: #efede9');
    expect(css).toContain('--conversation-stream-accent: #f2f2f2');
    expect(css).toContain('--conversation-stream-accent-soft: #303030');
    expect(css).toContain('--workspace-ui-font-size: 13px');
    expect(css).toContain('.arco-form-item-help');
    expect(css).toContain('.arco-spin-icon');
    expect(css).toContain('.arco-btn-primary.arco-btn-disabled');
    expect(css).toContain('color: var(--workspace-text-tertiary) !important');
    expect(css).toContain('button:not(.arco-btn):not(.text-10px)');
    expect(css).toContain('.settings-page-wrapper');
    expect(css).toContain('button.text-13px');
    expect(css).toContain('.settings-page-content h2');
    expect(css).toContain('.settings-page-content h3');
    expect(css).toContain('.settings-page-content h4');
    expect(css).not.toContain('.assistant-transcript-text');
    expect(transcriptCss).toMatch(
      /\.assistant-transcript-text\s*\{[^}]*--chat-line-height:\s*25px;[^}]*font-family:\s*var\(--conversation-font-sans, system-ui\);/s
    );
    expect(css).toContain('--conversation-user-surface: #efeeeb');
    expect(css).toContain('max-width: min(100%, var(--conversation-content-max-width, 56rem)) !important');
    expect(css).not.toContain('max-width: min(85%, 56rem) !important');
    expect(transcriptCss).toContain('font-size: var(--chat-font-size, 15px);');
    expect(css).toContain("[data-testid='synon-biomed-ask-user-card']");
    for (const selector of [
      '.synon-ask-user-card__composer',
      '.synon-ask-user-card__input',
      '.synon-ask-user-card__action--quiet',
    ]) {
      expect(css).not.toContain(selector);
      expect(askUserCss).toContain(selector);
    }
    expect(css).toContain('.synon-approval-card');
    expect(css).toContain('.runtime-operations-banner');
    expect(css).toContain(".app-overlay-menu [role='menuitem']");
    expect(css).toContain("[role='dialog']:has([data-testid^='mobile-action-sheet-'])");
    expect(css).toContain('.accessible-content-dialog__body');
  });

  it('keeps the edit action attached to a naturally sized user bubble', () => {
    const css = readFileSync(userMessageActionsCssPath, 'utf8');
    const messageText = readFileSync(messageTextPath, 'utf8');

    expect(css).toMatch(/\.synon-biomed-user-message__row\s*\{[^}]*gap:\s*8px/s);
    expect(css).toMatch(
      /\.synon-biomed-user-message__content\s*\{[^}]*width:\s*fit-content[^}]*max-width:\s*calc\(100% - 32px\)/s
    );
    expect(messageText).toContain('message-text-layout--user');
    expect(messageText).toContain('message-text-bubble--user');
  });

  it('keeps the desktop conversation measure on the shared 56rem outer rail', () => {
    const css = readFileSync(messagesCssPath, 'utf8');

    expect(css).toContain('--conversation-content-max-width: 56rem;');
    expect(css).toContain('--conversation-reading-inline-inset: 40px;');
    expect(css).toContain('--conversation-composer-inline-inset: 24px;');
    expect(css).toMatch(/\.chat-surface-fluid\s*\{[^}]*max-width:\s*var\(--conversation-content-max-width\)/s);
    expect(css).toContain('padding-right: var(--conversation-reading-inline-inset) !important;');
    expect(css).toContain('padding-left: var(--conversation-reading-inline-inset) !important;');
    expect(css).toMatch(
      /--conversation-composer-inline-size:\s*calc\(\s*100% - var\(--conversation-composer-inline-inset\) - var\(--conversation-composer-inline-inset\)\s*\)/s
    );
    expect(css).toContain('border-radius: 16px !important;');
    expect(css).toContain('padding: 8px 12px !important;');
    expect(css).not.toContain('max-width: 980px;');
  });

  it('keeps the command queue docked to the unchanged composer panel rail', () => {
    const css = readFileSync(conversationQueueDockCssPath, 'utf8');

    expect(css).toMatch(
      /\.conversation-composer\s*>\s*\.conversation-command-queue\s*\{[^}]*box-sizing:\s*border-box[^}]*width:\s*var\(--conversation-composer-inline-size,\s*100%\)[^}]*max-width:\s*var\(--conversation-composer-inline-size,\s*100%\)[^}]*margin-inline:\s*auto/s
    );
    expect(css).not.toMatch(/width:\s*calc\(90%/);
  });

  it('keeps rich Markdown inside Shadow DOM on the same visual scale', () => {
    const shadowView = readFileSync(shadowViewPath, 'utf8');

    expect(shadowView).toContain('var(--chat-heading-2-font-size, 18px)');
    expect(shadowView).toContain('var(--chat-code-font-size, var(--code-font-size, 12px))');
    expect(shadowView).toContain('var(--conversation-stream-accent, ${theme.Color.PrimaryColor})');
    expect(shadowView).toContain('var(--chat-line-height, 19.6px)');
    expect(shadowView).toContain('overflow-x: auto');
    expect(shadowView).toContain('table th,');
    expect(shadowView).toContain('color-mix(in srgb');
  });

  it('renders send and more as separate quiet template actions', () => {
    const css = readFileSync(sendboxCssPath, 'utf8');

    expect(css).toMatch(
      /\.sendbox-joined-actions\s*\{[^}]*gap:\s*6px[^}]*background:\s*transparent[^}]*box-shadow:\s*none/s
    );
    expect(css).toMatch(/\.sendbox-joined-actions__divider\s*\{[^}]*display:\s*none/s);
    expect(css).toMatch(
      /\.sendbox-joined-actions \.send-button-custom,[\s\S]*?display:\s*inline-flex[^}]*width:\s*32px[^}]*height:\s*32px[^}]*align-items:\s*center[^}]*justify-content:\s*center[^}]*border-radius:\s*10px[^}]*background-color:\s*var\(--workspace-accent\)/
    );
    expect(css).toMatch(
      /\.sendbox-joined-actions \.synon-biomed-more-send-options\s*\{[^}]*display:\s*inline-flex[^}]*width:\s*32px[^}]*height:\s*32px[^}]*align-items:\s*center[^}]*justify-content:\s*center[^}]*border-radius:\s*10px[^}]*background:\s*var\(--workspace-subtle\)[^}]*box-shadow:\s*none/s
    );
    expect(css).toMatch(
      /\.sendbox-joined-actions \.sendbox-action-icon\s*\{[^}]*display:\s*inline-flex[^}]*width:\s*16px[^}]*height:\s*16px[^}]*align-items:\s*center[^}]*justify-content:\s*center[^}]*line-height:\s*0/s
    );
    expect(css).toMatch(
      /\.sendbox-joined-actions \.sendbox-action-icon svg\s*\{[^}]*display:\s*block[^}]*width:\s*16px[^}]*height:\s*16px/s
    );
    expect(css).not.toMatch(/\.sendbox-joined-actions\s*\{[^}]*linear-gradient/s);
  });

  it('centers the new-chat glyph and label on one symmetric row', () => {
    const css = readFileSync(layoutCssPath, 'utf8');

    expect(css).toMatch(
      /\.synon-sidebar-new-chat > span:first-child\s*\{[^}]*box-sizing:\s*border-box[^}]*width:\s*28px[^}]*height:\s*28px[^}]*border:\s*0[^}]*border-radius:\s*8px[^}]*background:\s*var\(--synon-sidebar-hover\)/s
    );
    expect(css).toMatch(
      /\.synon-sidebar-new-chat \.i-icon\s*\{[^}]*display:\s*inline-flex[^}]*align-items:\s*center[^}]*justify-content:\s*center[^}]*line-height:\s*0/s
    );
    expect(css).toMatch(/\.synon-sidebar-new-chat \.i-icon svg\s*\{[^}]*display:\s*block/s);
    expect(css).toMatch(
      /\.synon-sidebar-new-chat > span:last-child\s*\{[^}]*display:\s*flex[^}]*align-items:\s*center[^}]*min-height:\s*24px[^}]*line-height:\s*24px/s
    );
  });
});
