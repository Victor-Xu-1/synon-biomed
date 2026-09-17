import SendBox from '@/renderer/components/chat/SendBox';
import { cleanup, screen } from '@testing-library/react';
import React from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../../i18nTestUtils';

const previewContext = {
  setSendBoxHandler: vi.fn(),
  domSnippets: [],
  removeDomSnippet: vi.fn(),
  clearDomSnippets: vi.fn(),
};

vi.mock('@/renderer/pages/conversation/Preview/context/PreviewContext', () => ({
  usePreviewContext: () => previewContext,
}));

vi.mock('@/common/config/configService', () => ({
  configService: {
    get: vi.fn(),
    set: vi.fn().mockResolvedValue(undefined),
    setLocal: vi.fn(),
    whenReady: vi.fn().mockResolvedValue(undefined),
    subscribe: vi.fn(() => vi.fn()),
  },
}));

afterEach(cleanup);

describe('SendBox right-side tools', () => {
  it('keeps the model/action slot visible in the single-line layout', async () => {
    await renderWithI18n(
      <SendBox
        value='Draft remains editable'
        onChange={vi.fn()}
        onSend={vi.fn().mockResolvedValue(undefined)}
        rightTools={<button type='button'>Current model</button>}
      />
    );

    expect(screen.getByRole('button', { name: 'Current model' })).toBeVisible();
    expect(screen.getByTestId('sendbox-input')).toHaveValue('Draft remains editable');
  });
});
