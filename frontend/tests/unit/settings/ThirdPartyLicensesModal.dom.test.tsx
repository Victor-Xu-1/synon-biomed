import { fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import ThirdPartyLicensesModal, {
  parseThirdPartyLicenseSections,
} from '@/renderer/pages/settings/components/ThirdPartyLicensesModal';
import { renderWithI18n } from '../i18nTestUtils';

const mocks = vi.hoisted(() => ({
  load: vi.fn(),
  copy: vi.fn(),
  messageSuccess: vi.fn(),
  messageError: vi.fn(),
}));

vi.mock('@arco-design/web-react', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@arco-design/web-react')>();
  return {
    ...actual,
    Message: {
      ...actual.Message,
      success: mocks.messageSuccess,
      error: mocks.messageError,
    },
  };
});

vi.mock('@/renderer/services/synonBiomedLicenses', () => ({
  loadSynonBiomedThirdPartyLicenses: mocks.load,
}));

vi.mock('@/renderer/utils/ui/clipboard', () => ({
  copyText: mocks.copy,
}));

const content = `# Third-Party Licenses

Deployment attribution overview.

### Ketcher

Ketcher 3.12.0 uses Apache License 2.0.

### RDKit

RDKit uses the BSD 3-Clause license.
`;

describe('ThirdPartyLicensesModal', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.load.mockResolvedValue({
      title: 'Third-Party Licenses',
      source: 'runtime/assets/skills/THIRD_PARTY_LICENSES.md',
      bytes: content.length,
      sha256: 'b'.repeat(64),
      content,
    });
    mocks.copy.mockResolvedValue(undefined);
  });

  it('parses the auditable markdown inventory into complete navigable sections', () => {
    expect(parseThirdPartyLicenseSections(content)).toEqual([
      { id: 'overview', title: 'Overview', content: 'Deployment attribution overview.' },
      {
        id: 'section-1',
        title: 'Ketcher',
        content: '### Ketcher\n\nKetcher 3.12.0 uses Apache License 2.0.',
      },
      {
        id: 'section-2',
        title: 'RDKit',
        content: '### RDKit\n\nRDKit uses the BSD 3-Clause license.',
      },
    ]);
  });

  it('loads the full inventory, filters sections, toggles wrapping and copies the selected legal text', async () => {
    await renderWithI18n(<ThirdPartyLicensesModal visible onClose={vi.fn()} />, 'zh-CN');

    const contentPane = await screen.findByTestId('third-party-license-content');
    expect(contentPane).toHaveTextContent('Ketcher 3.12.0');
    expect(contentPane).toHaveTextContent('RDKit uses the BSD 3-Clause license');
    expect(screen.getByText(/3 个章节/)).toBeInTheDocument();
    expect(screen.getByText('runtime/assets/skills/THIRD_PARTY_LICENSES.md')).toBeInTheDocument();

    fireEvent.change(screen.getByRole('textbox', { name: '搜索第三方许可证' }), {
      target: { value: 'BSD' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'RDKit' }));
    expect(contentPane).toHaveTextContent('RDKit uses the BSD 3-Clause license');
    expect(contentPane).not.toHaveTextContent('Ketcher 3.12.0');

    fireEvent.click(screen.getByRole('switch', { name: '自动换行' }));
    expect(contentPane).toHaveClass('whitespace-pre');
    fireEvent.click(screen.getByRole('button', { name: '复制当前许可证章节' }));
    await waitFor(() => expect(mocks.copy).toHaveBeenCalledWith(expect.stringContaining('BSD 3-Clause')));
  });

  it('shows a stable failure with an explicit retry that can recover', async () => {
    mocks.load.mockRejectedValueOnce(new Error('private path should not be shown'));
    await renderWithI18n(<ThirdPartyLicensesModal visible onClose={vi.fn()} />, 'zh-CN');

    expect(await screen.findByRole('alert')).toHaveTextContent('无法加载第三方许可证清单');
    expect(screen.queryByText('private path should not be shown')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '重试' }));
    expect(await screen.findByTestId('third-party-license-content')).toHaveTextContent('Ketcher 3.12.0');
    await waitFor(() => expect(mocks.load).toHaveBeenCalledTimes(2));
  });
});
