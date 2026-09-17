import { screen } from '@testing-library/react';
import React from 'react';
import { describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

vi.mock('@/renderer/hooks/context/LayoutContext', () => ({
  useLayoutContext: () => ({ isMobile: false }),
}));

import PlansUsageSettings from '@/renderer/pages/settings/PlansUsageSettings';

describe('PlansUsageSettings', () => {
  it('shows the first credit-rule draft, model rates, bills, and invoices for the local edition', async () => {
    await renderWithI18n(<PlansUsageSettings />, 'zh-CN');

    expect(screen.getByRole('heading', { name: '套餐与用量', level: 1 })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: '套餐用量', level: 2 })).toBeInTheDocument();
    expect(screen.getByText('本地免费版')).toBeInTheDocument();
    expect(screen.getByText('规则草稿')).toBeInTheDocument();
    expect(screen.getByText('剩余积分')).toBeInTheDocument();
    expect(screen.getAllByText('10,000')).toHaveLength(2);
    expect(screen.getByRole('heading', { name: '模型折算（草稿）', level: 2 })).toBeInTheDocument();
    expect(screen.getByTestId('synon-model-rate-light')).toHaveTextContent('轻量模型');
    expect(screen.getByTestId('synon-model-rate-standard')).toHaveTextContent('标准模型');
    expect(screen.getByTestId('synon-model-rate-reasoning')).toHaveTextContent('高阶推理模型');
    expect(screen.getByText(/积分消耗/)).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: '账单与发票', level: 2 })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: '账单', level: 3 })).toBeInTheDocument();
    expect(screen.getByText('暂无账单')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: '发票', level: 3 })).toBeInTheDocument();
    expect(screen.getByText('暂无发票')).toBeInTheDocument();

    expect(screen.queryByText('成长计划')).not.toBeInTheDocument();
    expect(screen.queryByText('API 管理')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /升级|购买|兑换/ })).not.toBeInTheDocument();
  });
});
