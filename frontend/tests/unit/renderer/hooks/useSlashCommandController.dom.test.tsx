/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { describe, expect, it, vi } from 'vitest';
import { act, render, renderHook } from '@testing-library/react';
import React from 'react';
import SlashCommandMenu from '@/renderer/components/chat/SlashCommandMenu';
import {
  filterSlashCommands,
  getFuzzyMatchIndices,
  useSlashCommandController,
} from '@/renderer/hooks/chat/useSlashCommandController';
import type { SlashCommandItem } from '@/common/chat/slash/types';

const command = (name: string): SlashCommandItem => ({
  name,
  description: name,
  kind: 'template',
  source: 'acp',
});

describe('slash command fuzzy matching', () => {
  it('matches command names by contiguous substring anywhere in the name', () => {
    expect(getFuzzyMatchIndices('review', 'ev')).toEqual([1, 2]);
    expect(getFuzzyMatchIndices('review', 'vie')).toEqual([2, 3, 4]);
    expect(getFuzzyMatchIndices('review', 'rv')).toBeNull();
  });

  it('filters commands with fuzzy matching while preserving command order', () => {
    const commands = [command('open'), command('review'), command('copy-last-output')];

    expect(filterSlashCommands(commands, 'vie').map((item) => item.name)).toEqual(['review']);
    expect(filterSlashCommands(commands, 'rv').map((item) => item.name)).toEqual([]);
    expect(filterSlashCommands(commands, 'co').map((item) => item.name)).toEqual(['copy-last-output']);
  });

  it('returns the selected command identity so Skill commands can use structured context', () => {
    const skillCommand: SlashCommandItem = {
      name: 'single-cell-analysis',
      description: 'Single-cell analysis',
      kind: 'template',
      source: 'skill',
      selectionBehavior: 'insert',
    };
    const onSelectTemplate = vi.fn();
    const { result } = renderHook(() =>
      useSlashCommandController({
        input: '/single',
        commands: [skillCommand],
        onSelectTemplate,
      })
    );

    act(() => {
      expect(result.current.onSelectByIndex(0)).toBe(true);
    });
    expect(onSelectTemplate).toHaveBeenCalledWith(skillCommand);
  });
});

describe('SlashCommandMenu highlights', () => {
  it('marks matched label characters as highlighted', () => {
    const { container } = render(
      <SlashCommandMenu
        title='Commands'
        items={[
          {
            key: 'review',
            label: '/review',
            description: 'Review',
            highlightIndices: [2, 3],
          },
        ]}
        activeIndex={0}
        onHoverItem={vi.fn()}
        onSelectItem={vi.fn()}
        emptyText='No commands found'
      />
    );

    const highlighted = Array.from(container.querySelectorAll('[data-slash-highlight="true"]'));
    expect(highlighted).toHaveLength(1);
    expect(highlighted.map((node) => node.textContent).join('')).toBe('ev');
    expect(highlighted.every((node) => node.classList.contains('bg-aou-2'))).toBe(true);
  });
});
