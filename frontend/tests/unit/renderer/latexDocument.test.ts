// @vitest-environment jsdom
import { describe, expect, it } from 'vitest';
import {
  convertLatexDocumentToHtml,
  normalizeLatexResourceKey,
  resolveLatexArtifactResources,
} from '@/renderer/pages/artifact/latexDocument';

describe('LaTeX document conversion', () => {
  it('converts document structure, lists and math into semantic HTML placeholders', () => {
    const result = convertLatexDocumentToHtml(String.raw`
\documentclass{article}
\begin{document}
\section{Results}
The response follows $E = mc^2$.
\begin{itemize}
\item First observation
\item Second observation
\end{itemize}
\[
\sum_{i=1}^{n} i
\]
\end{document}`);

    expect(result.html).toContain('<h3>Results</h3>');
    expect(result.html).toContain('<ul class="itemize">');
    expect(result.html).toContain('<li><p>First observation</p></li>');
    expect(result.html).toContain('class="inline-math"');
    expect(result.html).toContain('class="display-math"');
    expect(result.warnings).toEqual([]);
  });

  it('resolves project images while blocking unsafe sources', () => {
    const html = resolveLatexArtifactResources(
      '<img src="figures/result"><img src="https://example.test/leak.png"><img src="../secret.png">',
      {
        'figures/result.png': '/api/artifacts/image-1',
        'result.png': '/api/artifacts/image-1',
        result: '/api/artifacts/image-1',
      }
    );
    const document = new DOMParser().parseFromString(html, 'text/html');
    const images = [...document.querySelectorAll('img')];
    expect(images[0]?.getAttribute('src')).toBe('/api/artifacts/image-1');
    expect(images[0]?.getAttribute('loading')).toBe('lazy');
    for (const image of images.slice(1)) {
      expect(image.hasAttribute('src')).toBe(false);
      expect(image.classList.contains('latex-unresolved-resource')).toBe(true);
    }
  });

  it('normalizes safe relative resource keys and rejects unsafe paths', () => {
    expect(normalizeLatexResourceKey('./Figures\\Result.PNG')).toBe('figures/result.png');
    expect(normalizeLatexResourceKey('%2e%2e/private.png')).toBeNull();
    expect(normalizeLatexResourceKey('javascript:alert')).toBeNull();
  });
});
