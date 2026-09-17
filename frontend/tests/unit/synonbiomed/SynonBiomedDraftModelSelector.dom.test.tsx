/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { fireEvent, render, screen } from '@testing-library/react';
import React from 'react';
import { describe, expect, it, vi } from 'vitest';
import SynonBiomedDraftModelSelector from '@/renderer/components/synonBiomed/runtime/SynonBiomedDraftModelSelector';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) =>
      ({
        'conversation.synonRuntime.modelSelector.title': 'Select model',
        'conversation.synonRuntime.modelSelector.effort': 'Effort',
        'conversation.synonRuntime.modelSelector.effortDescription': 'Higher effort means more thorough responses.',
        'conversation.synonRuntime.modelSelector.moreModels': 'More models',
        'common.model': 'Model',
        'common.defaultModel': 'Default',
      })[key] || key,
  }),
}));

vi.mock('@arco-design/web-react', () => ({
  Button: ({ children, ...props }: React.ButtonHTMLAttributes<HTMLButtonElement>) => (
    <button type='button' {...props}>
      {children}
    </button>
  ),
  Dropdown: ({ children, droplist }: { children?: React.ReactNode; droplist?: React.ReactNode }) => (
    <div>
      {children}
      {droplist}
    </div>
  ),
}));

const modelInfo = {
  current_model_id: 'mimo-v2.5',
  current_model_label: 'Mimo v2.5',
  available_models: [
    { id: 'mimo-v2.5', label: 'Mimo v2.5', description: 'Fast everyday model' },
    { id: 'research', label: 'Deep Research', description: 'For complex research tasks' },
  ],
};

const thoughtLevelOption = {
  id: 'thought_level',
  category: 'thought_level',
  currentValue: 'medium',
  options: [
    { value: 'low', label: 'Low', description: 'Faster responses' },
    { value: 'medium', label: 'Medium', description: 'Balanced' },
    { value: 'high', label: 'High', description: 'More thorough' },
  ],
};

describe('SynonBiomedDraftModelSelector', () => {
  it('renders authoritative models and selects a model from the primary menu', () => {
    const onSelectModel = vi.fn();

    render(
      <SynonBiomedDraftModelSelector
        modelInfo={modelInfo}
        selectedModelId='mimo-v2.5'
        onSelectModel={onSelectModel}
        thoughtLevelOption={thoughtLevelOption}
        selectedThoughtLevel='medium'
        onSelectThoughtLevel={vi.fn()}
      />
    );

    expect(screen.getByTestId('synonbiomed-draft-model-selector')).toHaveTextContent('Mimo v2.5Medium');
    const modelMenu = screen.getByRole('menu', { name: 'Select model' });
    expect(modelMenu).toHaveClass('app-overlay-menu');
    expect(modelMenu).toHaveStyle({
      maxWidth: 'calc(100vw - 24px)',
      maxHeight: 'calc(100vh - 24px)',
    });
    expect(screen.getByTestId('synonbiomed-model-option-mimo-v2.5')).toHaveAttribute('aria-checked', 'true');
    expect(screen.getByTestId('synonbiomed-effort-selector')).toHaveAttribute('role', 'menuitem');
    expect(screen.getByText('For complex research tasks')).toBeInTheDocument();

    fireEvent.click(screen.getByTestId('synonbiomed-model-option-research'));
    expect(onSelectModel).toHaveBeenCalledWith('research');
  });

  it('shows only runtime-supported effort values and forwards the selected value', () => {
    const onSelectThoughtLevel = vi.fn();

    render(
      <SynonBiomedDraftModelSelector
        modelInfo={modelInfo}
        selectedModelId='mimo-v2.5'
        onSelectModel={vi.fn()}
        thoughtLevelOption={thoughtLevelOption}
        selectedThoughtLevel='medium'
        onSelectThoughtLevel={onSelectThoughtLevel}
      />
    );

    fireEvent.click(screen.getByTestId('synonbiomed-effort-selector'));
    const effortMenu = screen.getByTestId('synonbiomed-effort-menu');
    expect(effortMenu).toHaveClass('app-overlay-menu');
    expect(effortMenu).toHaveStyle({
      maxWidth: 'calc(100vw - 24px)',
      maxHeight: 'calc(100vh - 24px)',
    });
    expect(screen.getByTestId('synonbiomed-effort-option-low')).toBeInTheDocument();
    expect(screen.getByTestId('synonbiomed-effort-option-medium')).toHaveAttribute('aria-checked', 'true');
    expect(screen.getByTestId('synonbiomed-effort-option-medium')).toHaveAttribute('role', 'menuitemradio');
    expect(screen.getByTestId('synonbiomed-effort-option-high')).toBeInTheDocument();
    expect(screen.queryByText('Extra')).not.toBeInTheDocument();
    expect(screen.queryByText('Max')).not.toBeInTheDocument();

    fireEvent.click(screen.getByTestId('synonbiomed-effort-option-high'));
    expect(onSelectThoughtLevel).toHaveBeenCalledWith('high');
  });

  it('keeps the menu available with one model and opens model settings', () => {
    const onOpenModelSettings = vi.fn();

    render(
      <SynonBiomedDraftModelSelector
        modelInfo={{ ...modelInfo, available_models: [modelInfo.available_models[0]] }}
        selectedModelId='mimo-v2.5'
        onSelectModel={vi.fn()}
        thoughtLevelOption={thoughtLevelOption}
        selectedThoughtLevel='low'
        onSelectThoughtLevel={vi.fn()}
        onOpenModelSettings={onOpenModelSettings}
      />
    );

    expect(screen.getByTestId('synonbiomed-effort-selector')).toBeInTheDocument();
    fireEvent.click(screen.getByTestId('synonbiomed-more-models'));
    expect(onOpenModelSettings).toHaveBeenCalledOnce();
  });
});
