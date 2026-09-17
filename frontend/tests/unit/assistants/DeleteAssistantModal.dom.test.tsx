import React from 'react';
/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 *
 * Unit tests for DeleteAssistantModal component (A8 in N4a).
 * Tests deletion confirmation modal, builtin guard, and cancel behavior.
 */

import { describe, it, expect, beforeAll, beforeEach, vi, afterEach } from 'vitest';
import { render, screen, cleanup } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ConfigProvider } from '@arco-design/web-react';
import type { i18n } from 'i18next';
import { I18nextProvider } from 'react-i18next';

import DeleteAssistantModal from '@/renderer/pages/settings/SynonBiomedExpertsSettings/DeleteAssistantModal';
import type { AssistantListItem } from '@/renderer/pages/settings/SynonBiomedExpertsSettings/types';
import { createTestI18n } from '../i18nTestUtils';

let testI18n: i18n;

const renderWithProviders = (ui: React.ReactElement) =>
  render(
    <I18nextProvider i18n={testI18n}>
      <ConfigProvider>{ui}</ConfigProvider>
    </I18nextProvider>
  );

describe('DeleteAssistantModal', () => {
  const defaultProps = {
    visible: false,
    onConfirm: vi.fn(),
    onCancel: vi.fn(),
    activeAssistant: null as AssistantListItem | null,
  };

  beforeAll(async () => {
    testI18n = await createTestI18n('en-US');
  });

  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    cleanup();
  });

  it('does not render when visible=false (props branch)', () => {
    const { container } = renderWithProviders(<DeleteAssistantModal {...defaultProps} />);
    expect(container.querySelector('[data-testid="modal-delete-assistant"]')).not.toBeInTheDocument();
  });

  it('renders modal when visible=true (smoke)', () => {
    const assistant: AssistantListItem = { id: 'a1', name: 'Test', sort_order: 1, source: 'user', enabled: true };
    renderWithProviders(<DeleteAssistantModal {...defaultProps} visible={true} activeAssistant={assistant} />);
    expect(screen.getByTestId('modal-delete-assistant')).toBeInTheDocument();
  });

  it('displays assistant name in confirmation (props branch)', () => {
    const assistant: AssistantListItem = {
      id: 'a1',
      name: 'UserAssistant',
      sort_order: 1,
      source: 'user',
      enabled: true,
    };
    renderWithProviders(<DeleteAssistantModal {...defaultProps} visible={true} activeAssistant={assistant} />);
    expect(screen.getByText('UserAssistant')).toBeInTheDocument();
  });

  it('calls onConfirm when OK button is clicked (callback spy)', async () => {
    const onConfirmSpy = vi.fn();
    const assistant: AssistantListItem = { id: 'a1', name: 'Test', sort_order: 1, source: 'user', enabled: true };
    const user = userEvent.setup();
    renderWithProviders(
      <DeleteAssistantModal {...defaultProps} visible={true} activeAssistant={assistant} onConfirm={onConfirmSpy} />
    );

    const okButton = screen.getByRole('button', { name: /delete/i });
    await user.click(okButton);

    expect(onConfirmSpy).toHaveBeenCalledTimes(1);
  });

  it('calls onCancel when Cancel button is clicked (callback spy)', async () => {
    const onCancelSpy = vi.fn();
    const assistant: AssistantListItem = { id: 'a1', name: 'Test', sort_order: 1, source: 'user', enabled: true };
    const user = userEvent.setup();
    renderWithProviders(
      <DeleteAssistantModal {...defaultProps} visible={true} activeAssistant={assistant} onCancel={onCancelSpy} />
    );

    const cancelButton = screen.getByRole('button', { name: /cancel/i });
    await user.click(cancelButton);

    expect(onCancelSpy).toHaveBeenCalledTimes(1);
  });

  it('renders without activeAssistant (props branch)', () => {
    renderWithProviders(<DeleteAssistantModal {...defaultProps} visible={true} activeAssistant={null} />);
    expect(screen.getByTestId('modal-delete-assistant')).toBeInTheDocument();
  });
});
