import { render, screen } from '@testing-library/react';
import React from 'react';
import { describe, expect, it } from 'vitest';
import { SkillFilePreview } from '@/renderer/pages/settings/skills/SkillFilePreview';

describe('Skill file preview', () => {
  it('renders Markdown prose without frontmatter or raw HTML comments', () => {
    render(
      <SkillFilePreview
        path='SKILL.md'
        content={'---\nname: demo\n---\n<!-- metadata -->\n# Workflow\n\nUseful instructions.'}
      />
    );
    expect(screen.getByRole('heading', { name: 'Workflow' })).toBeInTheDocument();
    expect(screen.getByTestId('skill-markdown')).toHaveTextContent('Useful instructions.');
    expect(screen.getByTestId('skill-markdown')).not.toHaveTextContent('metadata');
    expect(screen.getByTestId('skill-markdown')).not.toHaveTextContent('name: demo');
  });

  it('shows source files verbatim rather than treating code comments as headings', () => {
    const content = '# Python comment\nprint("<img src=x onerror=alert(1)>")';
    render(<SkillFilePreview path='scripts/run.py' content={content} />);
    expect(screen.getByTestId('skill-source-preview').textContent).toBe(content);
    expect(screen.queryByRole('heading')).not.toBeInTheDocument();
    expect(screen.queryByRole('img')).not.toBeInTheDocument();
  });
});
