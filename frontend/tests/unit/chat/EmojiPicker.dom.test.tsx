import React from 'react';
import type { i18n } from 'i18next';
import { I18nextProvider } from 'react-i18next';
import { beforeAll, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { ConfigProvider } from '@arco-design/web-react';
import EmojiPicker from '@/renderer/components/chat/EmojiPicker';
import { createTestI18n } from '../i18nTestUtils';

let testI18n: i18n;

describe('EmojiPicker', () => {
  beforeAll(async () => {
    testI18n = await createTestI18n('en-US');
  });

  it('renders the builtin avatar tab first with iconized labels and returns the selected builtin avatar route', async () => {
    const onChange = vi.fn();

    render(
      <I18nextProvider i18n={testI18n}>
        <ConfigProvider>
          <EmojiPicker
            builtinAvatars={[
              {
                id: 'dashboard-creator',
                label: 'Dashboard Creator',
                src: '/api/assistants/dashboard-creator/avatar',
              },
            ]}
            onChange={onChange}
          >
            <button type='button'>Open picker</button>
          </EmojiPicker>
        </ConfigProvider>
      </I18nextProvider>
    );

    fireEvent.click(screen.getByText('Open picker'));
    const tabs = screen.getAllByRole('tab');
    const tabTitles = tabs.map((tab) => tab.textContent);
    expect(tabTitles).toEqual(['👤 Built-in', '🙂 Emoji']);

    fireEvent.click(tabs[1]!);
    expect(screen.getByRole('button', { name: 'Smileys' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Animals' })).toBeInTheDocument();
    fireEvent.click(tabs[0]!);
    fireEvent.click(screen.getByAltText('Dashboard Creator'));

    await waitFor(() => {
      expect(onChange).toHaveBeenCalledWith('/api/assistants/dashboard-creator/avatar');
    });
  });

  it('localizes emoji category controls in Chinese', async () => {
    const zhI18n = await createTestI18n('zh-CN');
    render(
      <I18nextProvider i18n={zhI18n}>
        <ConfigProvider>
          <EmojiPicker>
            <button type='button'>打开表情选择器</button>
          </EmojiPicker>
        </ConfigProvider>
      </I18nextProvider>
    );

    fireEvent.click(screen.getByText('打开表情选择器'));
    expect(screen.getByRole('button', { name: '表情与人物' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '动物与自然' })).toBeInTheDocument();
  });
});
