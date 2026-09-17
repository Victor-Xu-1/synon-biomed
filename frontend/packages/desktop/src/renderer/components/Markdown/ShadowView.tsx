/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { theme } from '@office-ai/platform';
import React, { useState } from 'react';
import ReactDOM from 'react-dom';
import { addImportantToAll } from '@renderer/utils/theme/customCssProcessor';
import { useLayoutContext } from '@/renderer/hooks/context/LayoutContext';
import { useThemeContext } from '@/renderer/hooks/context/ThemeContext';

/**
 * Create the base style element for Shadow DOM with CSS variables, theme styles, and optional custom CSS.
 */
const createInitStyleText = (
  currentTheme = 'light',
  cssVars?: Record<string, string>,
  customCss?: string,
  isMobile?: boolean
) => {
  // Inject external CSS variables into Shadow DOM for dark mode support
  const cssVarsDeclaration = cssVars
    ? Object.entries(cssVars)
        .map(([key, value]) => `${key}: ${value};`)
        .join('\n    ')
    : '';

  const lineHeight = isMobile ? 'var(--chat-line-height, 19.6px)' : 'var(--chat-line-height, 24px)';
  const fontSize = isMobile ? 'var(--chat-font-size, 14px)' : 'var(--chat-font-size, 15px)';
  // Desktop paragraph spacing trimmed from 16px to 12px (~0.85em) for a more
  // compact reply; mobile spacing is left untouched (tuned separately).
  const paragraphMargin = isMobile ? 'var(--chat-paragraph-margin, 16px)' : 'var(--chat-paragraph-margin, 12px)';
  const listItemMargin = isMobile ? 'var(--chat-list-item-margin, 6px)' : 'var(--chat-list-item-margin, 6px)';

  return `
  /* Shadow DOM CSS variable definitions */
  :host {
    ${cssVarsDeclaration}
  }

  * {
    line-height:${lineHeight};
    font-size:${fontSize};
    color: inherit;
  }

  .markdown-shadow-body {
    word-break: break-word;
    overflow-wrap: anywhere;
    color: var(--text-primary);
    max-width: 100%;
  }
  .markdown-shadow-body p,
  .markdown-shadow-body li {
    font-size: ${fontSize} !important;
    line-height: ${lineHeight} !important;
  }
  .markdown-shadow-body>p:first-child
  {
    margin-top:0px;
  }
  h1,h2,h3,h4,h5,h6{
    margin-block-start:0px;
    margin-block-end:0px;
  }
  .markdown-shadow-body p {
    margin-block-start: ${paragraphMargin};
    margin-block-end: ${paragraphMargin};
  }
  .markdown-shadow-body li {
    margin-block-start: ${listItemMargin};
    margin-block-end: ${listItemMargin};
  }
  a {
    color: var(--conversation-stream-accent, ${theme.Color.PrimaryColor});
    text-decoration: none;
    cursor: pointer;
    word-break: break-all;
    overflow-wrap: anywhere;
  }
  a:hover,
  a:focus-visible {
    color: var(--conversation-stream-accent, ${theme.Color.PrimaryColor});
    text-decoration: underline;
    text-decoration-thickness: 1px;
    text-underline-offset: 2px;
  }
  a:focus-visible {
    outline: 2px solid var(--conversation-stream-accent, ${theme.Color.PrimaryColor});
    outline-offset: 2px;
    border-radius: 3px;
  }
  .markdown-local-file-link {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    max-width: 100%;
    min-height: 22px;
    padding: 2px 6px;
    background: var(--bg-2);
    color: var(--text-primary);
    border: 1px solid transparent;
    border-radius: 6px;
    box-shadow: none;
    font: inherit;
    line-height: inherit;
    vertical-align: baseline;
    cursor: pointer;
    transition:
      background-color 0.15s ease,
      border-color 0.15s ease;
  }
  span.markdown-local-file-link {
    cursor: default;
  }
  .markdown-local-file-link:hover {
    background: var(--bg-3);
    color: var(--text-primary);
    text-decoration: none;
  }
  .markdown-local-file-link .truncate {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .markdown-local-file-line {
    padding: 0 4px;
    border-radius: 4px;
    background: var(--bg-3);
    color: var(--text-secondary);
  }
  .markdown-local-file-copy {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    width: 20px;
    height: 20px;
    min-width: 20px;
    padding: 1px;
    border: 0;
    border-radius: 4px;
    background: transparent;
    color: var(--text-secondary);
    cursor: pointer;
  }
  .markdown-local-file-copy:hover {
    background: var(--bg-3);
    color: var(--text-primary);
  }
  .markdown-shadow-body > :is(h1, h2, h3, h4, h5, h6):first-child {
    margin-top: 0;
  }
  h1,
  h2,
  h3,
  h4,
  h5,
  h6 {
    margin-top: 20px;
    margin-bottom: 12px;
  }
  h1 {
    font-size: var(--chat-heading-1-font-size, 22px);
    line-height: var(--chat-heading-1-line-height, 30px);
    font-weight: 700;
    letter-spacing: -0.012em;
  }
  h2 {
    font-size: var(--chat-heading-2-font-size, 18px);
    line-height: var(--chat-heading-2-line-height, 26px);
    font-weight: 650;
    letter-spacing: -0.008em;
  }
  h3 {
    font-size: var(--chat-heading-3-font-size, 16px);
    line-height: var(--chat-heading-3-line-height, 24px);
    font-weight: 650;
  }
  h4,
  h5,
  h6 {
    font-size: var(--chat-heading-4-font-size, 15px);
    line-height: var(--chat-heading-4-line-height, 22px);
    font-weight: 600;
  }
  code span {
    font-size: var(--chat-code-font-size, var(--code-font-size, 12px));
    line-height: var(--chat-code-line-height, 20px);
    font-family: var(--font-mono);
  }

  .markdown-shadow-body>p:last-child{
    margin-bottom:0px;
  }
  ol, ul {
    padding-inline-start:24px;
  }
  hr {
    border: none;
    border-top: 1px solid var(--bg-3);
    margin: 28px 0;
  }
  strong {
    font-weight: 600;
    color: var(--text-primary);
  }
  .markdown-shadow-body code:not(pre code) {
    background: var(--conversation-code-surface, var(--bg-3));
    color: var(--text-primary);
    padding: 1px 6px;
    border: 1px solid var(--conversation-stream-border, var(--bg-3));
    border-radius: 5px;
    font-size: 0.875em;
    font-family: var(--font-mono);
  }
  blockquote {
    border-left: 3px solid var(--conversation-stream-accent, var(--bg-3));
    padding: 8px 14px;
    background: var(--conversation-stream-surface, var(--bg-2));
    color: var(--text-secondary, var(--text-primary));
    font-size: var(--chat-quote-font-size, 15px);
    line-height: var(--chat-quote-line-height, 24px);
    margin: 16px 0;
    border-radius: 8px;
  }
  pre {
    max-width: 100%;
    overflow-x: auto;
    margin-block-start: 8px;
    margin-block-end: 8px;
    border-radius: 10px;
  }
  .markdown-shadow-body pre,
  .markdown-shadow-body pre *,
  .markdown-shadow-body .hljs,
  .markdown-shadow-body .hljs * {
    font-family: var(--font-mono) !important;
    font-size: var(--chat-code-font-size, var(--code-font-size, 12px)) !important;
    line-height: var(--chat-code-line-height, 20px) !important;
  }
  /* Code block horizontal scrollbar — blends with bg-2 */
  pre,
  .hljs {
    scrollbar-width: thin;
    scrollbar-color: ${currentTheme === 'dark' ? 'rgba(255, 255, 255, 0.18)' : 'rgba(0, 0, 0, 0.1)'} transparent;
  }
  pre::-webkit-scrollbar,
  .hljs::-webkit-scrollbar {
    height: 6px;
    background: transparent;
  }
  pre::-webkit-scrollbar-track,
  .hljs::-webkit-scrollbar-track,
  pre::-webkit-scrollbar-corner,
  .hljs::-webkit-scrollbar-corner {
    background: transparent;
  }
  pre::-webkit-scrollbar-thumb,
  .hljs::-webkit-scrollbar-thumb {
    background-color: ${currentTheme === 'dark' ? 'rgba(255, 255, 255, 0.18)' : 'rgba(0, 0, 0, 0.1)'};
    border-radius: 3px;
  }
  pre::-webkit-scrollbar-thumb:hover,
  .hljs::-webkit-scrollbar-thumb:hover {
    background-color: ${currentTheme === 'dark' ? 'rgba(255, 255, 255, 0.28)' : 'rgba(0, 0, 0, 0.2)'};
  }
  img {
    max-width: 100%;
    height: auto;
  }
   /* Table border styles */
  table {
    border-collapse: separate;
    border-spacing: 0;
    max-width: 100%;
    min-width: 0;
    width: 100%;
    display: block;
    overflow-x: auto;
    border: 1px solid var(--conversation-stream-border, var(--bg-3));
    border-radius: 10px;
    font-size: var(--chat-table-font-size, 14px);
    line-height: var(--chat-table-line-height, 22px);
  }
  table th,
  table td {
    padding: 8px 10px;
    border: 1px solid var(--conversation-stream-border, var(--bg-3));
    min-width: 120px;
    vertical-align: top;
    font-size: var(--chat-table-font-size, 14px) !important;
    line-height: var(--chat-table-line-height, 22px) !important;
  }
  table th {
    background: var(--conversation-stream-accent-soft, var(--bg-1));
    color: var(--text-primary);
    font-weight: 650;
    text-align: left;
  }
  table tr:nth-child(even) td {
    background: color-mix(in srgb, var(--conversation-stream-surface, var(--bg-1)) 58%, transparent);
  }
  /* Inline code should wrap on small screens to avoid horizontal overflow */
  .markdown-shadow-body code {
    word-break: break-word;
    overflow-wrap: anywhere;
    max-width: 100%;
  }
  /* Allow KaTeX to use its own line-height for proper fraction/superscript rendering */
  .katex,
  .katex * {
    line-height: normal;
  }

  /* Display math: only scroll horizontally when formula exceeds container width */
  .katex-display {
    overflow-x: auto;
    overflow-y: hidden;
    padding: 0.5em 0;
  }

  .loading {
    animation: loading 1s linear infinite;
  }


  @keyframes loading {
    0% {
      transform: rotate(0deg);
    }
    100% {
      transform: rotate(360deg);
    }
  }

  /* User Custom CSS (injected into Shadow DOM) */
  ${customCss || ''}

  /* Conversation contract: keep the requested type scale stable across appearance presets. */
  .markdown-shadow-body p,
  .markdown-shadow-body li {
    font-size: ${fontSize} !important;
    line-height: ${lineHeight} !important;
  }
  .markdown-shadow-body h1 {
    font-size: var(--chat-heading-1-font-size, 22px) !important;
    line-height: var(--chat-heading-1-line-height, 30px) !important;
  }
  .markdown-shadow-body h2 {
    font-size: var(--chat-heading-2-font-size, 18px) !important;
    line-height: var(--chat-heading-2-line-height, 26px) !important;
  }
  .markdown-shadow-body h3 {
    font-size: var(--chat-heading-3-font-size, 16px) !important;
    line-height: var(--chat-heading-3-line-height, 24px) !important;
  }
  .markdown-shadow-body h4,
  .markdown-shadow-body h5,
  .markdown-shadow-body h6 {
    font-size: var(--chat-heading-4-font-size, 15px) !important;
    line-height: var(--chat-heading-4-line-height, 22px) !important;
  }
  .markdown-shadow-body table {
    border-collapse: separate !important;
    border-spacing: 0 !important;
    width: 100% !important;
    max-width: 100% !important;
    min-width: 0 !important;
    display: block !important;
    overflow-x: auto !important;
    border: 1px solid var(--conversation-stream-border, var(--bg-3)) !important;
    border-radius: 10px !important;
    font-size: var(--chat-table-font-size, 14px) !important;
    line-height: var(--chat-table-line-height, 22px) !important;
  }
  .markdown-shadow-body table th,
  .markdown-shadow-body table td {
    padding: 8px 10px !important;
    font-size: var(--chat-table-font-size, 14px) !important;
    line-height: var(--chat-table-line-height, 22px) !important;
  }
  `;
};

type SharedMarkdownStyleSheet = {
  cssText: string;
  sheet: CSSStyleSheet | null;
};

const sharedMarkdownStyleSheets: SharedMarkdownStyleSheet[] = [];
const MAX_SHARED_MARKDOWN_STYLE_SHEETS = 4;

const getSharedMarkdownStyleSheet = (cssText: string): CSSStyleSheet | null => {
  const cached = sharedMarkdownStyleSheets.find((entry) => entry.cssText === cssText);
  if (cached) return cached.sheet;

  let sheet: CSSStyleSheet | null = null;
  try {
    if (typeof CSSStyleSheet !== 'undefined' && typeof CSSStyleSheet.prototype.replaceSync === 'function') {
      sheet = new CSSStyleSheet();
      sheet.replaceSync(cssText);
    }
  } catch {
    sheet = null;
  }
  sharedMarkdownStyleSheets.push({ cssText, sheet });
  if (sharedMarkdownStyleSheets.length > MAX_SHARED_MARKDOWN_STYLE_SHEETS) sharedMarkdownStyleSheets.shift();
  return sheet;
};

let cachedMarkdownCssVariableKey = '';
let cachedMarkdownCssVariables: Record<string, string> | null = null;

let documentStyleGeneration = 0;
let documentStyleObserver: MutationObserver | null = null;
const documentStyleListeners = new Set<() => void>();

const subscribeDocumentStyle = (listener: () => void) => {
  documentStyleListeners.add(listener);
  if (!documentStyleObserver && typeof MutationObserver !== 'undefined') {
    documentStyleObserver = new MutationObserver(() => {
      documentStyleGeneration += 1;
      for (const notify of documentStyleListeners) notify();
    });
    documentStyleObserver.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['class', 'data-theme', 'style'],
    });
  }
  return () => {
    documentStyleListeners.delete(listener);
    if (documentStyleListeners.size === 0) {
      documentStyleObserver?.disconnect();
      documentStyleObserver = null;
    }
  };
};

const getDocumentStyleGeneration = () => documentStyleGeneration;

const readMarkdownCssVariables = (cacheKey: string): Record<string, string> => {
  if (cachedMarkdownCssVariables && cachedMarkdownCssVariableKey === cacheKey) return cachedMarkdownCssVariables;
  const computedStyle = getComputedStyle(document.documentElement);
  cachedMarkdownCssVariables = {
    '--bg-1': computedStyle.getPropertyValue('--bg-1'),
    '--bg-2': computedStyle.getPropertyValue('--bg-2'),
    '--bg-3': computedStyle.getPropertyValue('--bg-3'),
    '--color-text-1': computedStyle.getPropertyValue('--color-text-1'),
    '--color-text-2': computedStyle.getPropertyValue('--color-text-2'),
    '--color-text-3': computedStyle.getPropertyValue('--color-text-3'),
    '--text-primary': computedStyle.getPropertyValue('--text-primary'),
    '--text-secondary': computedStyle.getPropertyValue('--text-secondary'),
    '--chat-font-size': computedStyle.getPropertyValue('--chat-font-size'),
    '--code-font-size': computedStyle.getPropertyValue('--code-font-size'),
  };
  cachedMarkdownCssVariableKey = cacheKey;
  return cachedMarkdownCssVariables;
};

let cachedCustomCssSource = '';
let cachedCustomCss = '';

const normalizeMarkdownCustomCss = (css?: string): string => {
  const source = css || '';
  if (source === cachedCustomCssSource) return cachedCustomCss;
  cachedCustomCssSource = source;
  cachedCustomCss = source ? addImportantToAll(source) : '';
  return cachedCustomCss;
};

// Cache for KaTeX stylesheet to share across Shadow DOM instances
let katexStyleSheet: CSSStyleSheet | null = null;

/**
 * Get or create a shared KaTeX CSSStyleSheet for Shadow DOM adoption.
 * This extracts KaTeX styles from the document and creates a constructable stylesheet.
 */
const getKatexStyleSheet = (): CSSStyleSheet | null => {
  if (katexStyleSheet) return katexStyleSheet;

  try {
    // Find the KaTeX stylesheet in the document
    const katexSheet = [...document.styleSheets].find(
      (sheet) => sheet.href?.includes('katex') || (sheet.ownerNode as HTMLElement)?.dataset?.katex
    );

    if (katexSheet) {
      const cssRules = [...katexSheet.cssRules].map((rule) => rule.cssText).join('\n');
      katexStyleSheet = new CSSStyleSheet();
      katexStyleSheet.replaceSync(cssRules);
      return katexStyleSheet;
    }

    // Fallback: try to find KaTeX styles by checking style tags
    const styleSheets = [...document.styleSheets];
    for (const sheet of styleSheets) {
      try {
        const rules = [...sheet.cssRules];
        // Check if this stylesheet contains KaTeX rules
        const hasKatexRules = rules.some((rule) => rule.cssText.includes('.katex'));
        if (hasKatexRules) {
          const cssRules = rules.map((rule) => rule.cssText).join('\n');
          katexStyleSheet = new CSSStyleSheet();
          katexStyleSheet.replaceSync(cssRules);
          return katexStyleSheet;
        }
      } catch {
        // CORS may block access to cssRules for external stylesheets
        continue;
      }
    }
  } catch (error) {
    console.warn('Failed to create KaTeX stylesheet for Shadow DOM:', error);
  }

  return null;
};

type ShadowDivElement = HTMLDivElement & { __init__shadow?: boolean };

const ShadowView = ({ children }: { children: React.ReactNode }) => {
  const [root, setRoot] = useState<ShadowRoot | null>(null);
  const styleRef = React.useRef<HTMLStyleElement | null>(null);
  const adoptedStyleSheetRef = React.useRef<CSSStyleSheet | null>(null);
  const layout = useLayoutContext();
  const isMobile = layout?.isMobile ?? false;
  const { theme: themeAppearance, activeTheme, fontScale, fontSizes } = useThemeContext();
  const styleGeneration = React.useSyncExternalStore(
    subscribeDocumentStyle,
    getDocumentStyleGeneration,
    getDocumentStyleGeneration
  );
  const currentTheme = activeTheme?.appearance ?? themeAppearance;
  const customCss = normalizeMarkdownCustomCss(activeTheme?.css);
  const documentStyleKey = [
    activeTheme?.id ?? currentTheme,
    activeTheme?.updated_at ?? 0,
    fontScale,
    JSON.stringify(fontSizes),
    document.documentElement.getAttribute('data-theme') ?? '',
    document.documentElement.className,
    document.documentElement.style.cssText,
    styleGeneration,
  ].join('|');
  const cssVars = readMarkdownCssVariables(documentStyleKey);
  const styleText = React.useMemo(
    () => createInitStyleText(currentTheme, cssVars, customCss, isMobile),
    [cssVars, currentTheme, customCss, isMobile]
  );
  const sharedStyleSheet = React.useMemo(() => getSharedMarkdownStyleSheet(styleText), [styleText]);

  // Update CSS variables and custom styles in Shadow DOM
  const updateStyles = React.useCallback(
    (shadowRoot: ShadowRoot) => {
      if (styleRef.current) {
        styleRef.current.remove();
        styleRef.current = null;
      }
      const katexSheet = getKatexStyleSheet();
      if (sharedStyleSheet) {
        try {
          const adoptedStyleSheets = shadowRoot.adoptedStyleSheets;
          const retained = adoptedStyleSheets.filter(
            (sheet) => sheet !== adoptedStyleSheetRef.current && sheet !== katexSheet
          );
          shadowRoot.adoptedStyleSheets = [...retained, sharedStyleSheet, ...(katexSheet ? [katexSheet] : [])];
          adoptedStyleSheetRef.current = sharedStyleSheet;
          return;
        } catch {
          adoptedStyleSheetRef.current = null;
        }
      }
      if (adoptedStyleSheetRef.current) {
        try {
          shadowRoot.adoptedStyleSheets = shadowRoot.adoptedStyleSheets.filter(
            (sheet) => sheet !== adoptedStyleSheetRef.current
          );
        } catch {
          // A partially implemented constructable stylesheet API must not prevent Markdown rendering.
        }
        adoptedStyleSheetRef.current = null;
      }
      const newStyle = document.createElement('style');
      newStyle.textContent = styleText;
      styleRef.current = newStyle;
      shadowRoot.appendChild(newStyle);

      // Inject KaTeX styles into Shadow DOM using adoptedStyleSheets
      // This allows math expressions to render correctly
      if (katexSheet) {
        try {
          if (!shadowRoot.adoptedStyleSheets.includes(katexSheet)) {
            shadowRoot.adoptedStyleSheets = [...shadowRoot.adoptedStyleSheets, katexSheet];
          }
        } catch {
          // Base Markdown CSS is already present through the style-element fallback.
        }
      }
    },
    [sharedStyleSheet, styleText]
  );

  React.useEffect(() => {
    if (!root) return;

    updateStyles(root);
  }, [root, updateStyles]);

  return (
    <div
      ref={(el: ShadowDivElement | null) => {
        if (!el || el.__init__shadow) return;
        el.__init__shadow = true;
        const shadowRoot = el.attachShadow({ mode: 'open' });
        updateStyles(shadowRoot);
        // React needs one follow-up render before it can portal into a newly
        // attached ShadowRoot. Project the initial light-DOM children through
        // a slot during that gap so the first streaming token can paint in the
        // same frame instead of waiting for the provider's next delta.
        shadowRoot.append(document.createElement('slot'));
        setRoot(shadowRoot);
      }}
      className='markdown-shadow'
      // The shadow root is attached during the ref commit and the portal is
      // mounted on the following render. Keep the host measurable in that
      // one-frame gap so virtualized transcript rows never report a zero size.
      style={{ width: '100%', flex: '1 1 auto', minWidth: 0, minHeight: 1 }}
    >
      {root ? ReactDOM.createPortal(children, root) : children}
    </div>
  );
};

export default ShadowView;
