/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import ReactMarkdown, { defaultUrlTransform } from 'react-markdown';

import rehypeKatex from 'rehype-katex';
import rehypeRaw from 'rehype-raw';
import remarkBreaks from 'remark-breaks';
import remarkGfm from 'remark-gfm';
import remarkMath from 'remark-math';

// Import KaTeX CSS to make it available in the document
import 'katex/dist/katex.min.css';

import { openExternalUrl } from '@/renderer/utils/platform';
import classNames from 'classnames';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { convertLatexDelimiters } from '@renderer/utils/chat/latexDelimiters';
import LocalImageView from '@renderer/components/media/LocalImageView';
import { useNearViewport } from '@renderer/hooks/ui/useNearViewport';
import CodeBlock from './CodeBlock';
import LocalFileLink from './LocalFileLink';
import ShadowView from './ShadowView';
import { isSafeInlineMarkdownImageUrl } from './markdownImageUrlPolicy';
import { resolveLocalFileLinkPath, resolveLocalFileLinkReference } from './markdownUtils';
import type { LocalFileLinkReference } from './markdownUtils';

const REMARK_PLUGINS = [remarkGfm, remarkMath, remarkBreaks];

const isLocalFilePath = (src: string): boolean => {
  if (src.startsWith('http://') || src.startsWith('https://')) return false;
  if (src.startsWith('data:')) return false;
  return true;
};

const isSynonBiomedArtifactReference = (value: string): boolean => {
  try {
    return /^\{\{artifact:[^}]+\}\}$/.test(decodeURIComponent(value));
  } catch {
    return false;
  }
};

type MarkdownViewProps = {
  children: string;
  hiddenCodeCopyButton?: boolean;
  codeStyle?: React.CSSProperties;
  className?: string;
  onRef?: (el?: HTMLDivElement | null) => void;
  onLocalFileLink?: (path: string, reference?: LocalFileLinkReference) => void | Promise<void>;
  onLink?: (href: string) => boolean | Promise<boolean>;
  resolveLinkHref?: (href: string) => Promise<string | null>;
  resolveImageSrc?: (src: string) => Promise<string | null>;
  /** Enable raw HTML rendering in markdown content. Use with caution — only for trusted sources. */
  allowHtml?: boolean;
};

const MarkdownView: React.FC<MarkdownViewProps> = React.memo(
  ({
    hiddenCodeCopyButton,
    codeStyle,
    className,
    onRef,
    onLocalFileLink,
    onLink,
    resolveLinkHref,
    resolveImageSrc,
    allowHtml,
    children: childrenProp,
  }) => {
    const { t } = useTranslation();
    const translationRef = useRef(t);
    const onLinkRef = useRef(onLink);
    const onLocalFileLinkRef = useRef(onLocalFileLink);
    const resolveLinkHrefRef = useRef(resolveLinkHref);
    const resolveImageSrcRef = useRef(resolveImageSrc);
    translationRef.current = t;
    onLinkRef.current = onLink;
    onLocalFileLinkRef.current = onLocalFileLink;
    resolveLinkHrefRef.current = resolveLinkHref;
    resolveImageSrcRef.current = resolveImageSrc;
    const stableLocalFileLink = useCallback(
      (path: string, reference?: LocalFileLinkReference) => onLocalFileLinkRef.current?.(path, reference),
      []
    );
    const localFileOpenHandler = onLocalFileLink ? stableLocalFileLink : undefined;
    const stableResolveLinkHref = useCallback(
      (href: string) => resolveLinkHrefRef.current?.(href) ?? Promise.resolve(null),
      []
    );
    const stableResolveImageSrc = useCallback(
      (src: string) => resolveImageSrcRef.current?.(src) ?? Promise.resolve(null),
      []
    );

    const normalizedChildren = useMemo(() => {
      if (typeof childrenProp === 'string') {
        let text = childrenProp.replace(/file:\/\//g, '');
        text = convertLatexDelimiters(text);
        return text;
      }
      return childrenProp;
    }, [childrenProp]);

    const handleLinkClick = useCallback(async (event: React.MouseEvent<HTMLAnchorElement>) => {
      event.preventDefault();
      event.stopPropagation();
      const originalHref = event.currentTarget.dataset.originalHref ?? '';
      const renderedHref = event.currentTarget.getAttribute('href') ?? '';
      const rawHref = originalHref || renderedHref;
      const resolvedHref = event.currentTarget.href;
      if (!rawHref && !resolvedHref) return;
      const currentOnLink = onLinkRef.current;
      if (currentOnLink && (await currentOnLink(rawHref || resolvedHref))) return;
      if (isSynonBiomedArtifactReference(rawHref) && renderedHref === '#') return;
      openExternalUrl(resolvedHref || rawHref).catch((error: unknown) => {
        console.error(translationRef.current('messages.openLinkFailed'), error);
      });
    }, []);

    // Memoize components so React preserves component identity across re-renders.
    // Without this, every streaming update creates new function references → React
    // unmounts/remounts all custom components → hooks & DOM state are lost.
    const components = useMemo(
      () => ({
        span: ({ node: _node, className: cn, children: ch, ...rest }: Record<string, unknown>) => (
          <span {...(rest as React.HTMLAttributes<HTMLSpanElement>)} className={cn as string}>
            {ch as React.ReactNode}
          </span>
        ),
        code: (props: Record<string, unknown>) => (
          <CodeBlock
            {...(props as Parameters<typeof CodeBlock>[0])}
            codeStyle={codeStyle}
            hiddenCodeCopyButton={hiddenCodeCopyButton}
          />
        ),
        a: ({ node: _node, ...rest }: Record<string, unknown>) => {
          const anchorProps = rest as React.AnchorHTMLAttributes<HTMLAnchorElement>;
          const rawHref = typeof anchorProps.href === 'string' ? anchorProps.href : '';
          const localFileReference = resolveLocalFileLinkReference(rawHref);
          if (localFileReference) {
            return (
              <LocalFileLink reference={localFileReference} onOpen={localFileOpenHandler}>
                {anchorProps.children}
              </LocalFileLink>
            );
          }
          if (resolveLinkHrefRef.current && isSynonBiomedArtifactReference(rawHref)) {
            return (
              <ResolvedArtifactLink
                {...anchorProps}
                originalHref={rawHref}
                resolve={stableResolveLinkHref}
                onClick={handleLinkClick}
              />
            );
          }
          return (
            <a {...anchorProps} href={anchorProps.href} target='_blank' rel='noreferrer' onClick={handleLinkClick} />
          );
        },
        table: ({ node: _node, ...rest }: Record<string, unknown>) => (
          <div style={{ overflowX: 'auto', maxWidth: '100%' }}>
            <table
              {...(rest as React.TableHTMLAttributes<HTMLTableElement>)}
              style={{
                ...(rest as { style?: React.CSSProperties }).style,
                borderCollapse: 'collapse',
                border: '1px solid var(--bg-3)',
                minWidth: '100%',
              }}
            />
          </div>
        ),
        td: ({ node: _node, ...rest }: Record<string, unknown>) => (
          <td
            {...(rest as React.TdHTMLAttributes<HTMLTableCellElement>)}
            style={{
              ...(rest as { style?: React.CSSProperties }).style,
              padding: '8px',
              border: '1px solid var(--bg-3)',
              minWidth: '120px',
            }}
          />
        ),
        img: ({ node: _node, ...rest }: Record<string, unknown>) => {
          const imgProps = rest as React.ImgHTMLAttributes<HTMLImageElement>;
          if (isLocalFilePath(imgProps.src || '')) {
            const src = decodeURIComponent(imgProps.src || '');
            if (resolveImageSrcRef.current) {
              return (
                <ResolvedArtifactImage
                  src={src}
                  alt={imgProps.alt || ''}
                  className={imgProps.className}
                  resolve={stableResolveImageSrc}
                />
              );
            }
            return <LocalImageView src={src} alt={imgProps.alt || ''} className={imgProps.className} />;
          }
          return <NearViewportImage {...imgProps} alt={imgProps.alt || ''} />;
        },
      }),
      [
        codeStyle,
        hiddenCodeCopyButton,
        handleLinkClick,
        localFileOpenHandler,
        stableResolveImageSrc,
        stableResolveLinkHref,
      ]
    );

    const rehypePlugins = useMemo(() => (allowHtml ? [rehypeRaw, rehypeKatex] : [rehypeKatex]), [allowHtml]);

    return (
      <div className={classNames('relative w-full', className)}>
        <ShadowView>
          <div ref={onRef} className='markdown-shadow-body'>
            <ReactMarkdown
              remarkPlugins={REMARK_PLUGINS}
              rehypePlugins={rehypePlugins}
              components={components}
              urlTransform={(url) =>
                isSynonBiomedArtifactReference(url) ||
                resolveLocalFileLinkPath(url) ||
                isSafeInlineMarkdownImageUrl(url)
                  ? url
                  : defaultUrlTransform(url)
              }
            >
              {normalizedChildren}
            </ReactMarkdown>
          </div>
        </ShadowView>
      </div>
    );
  }
);

type ResolvedArtifactLinkProps = Omit<React.AnchorHTMLAttributes<HTMLAnchorElement>, 'href'> & {
  originalHref: string;
  resolve: (href: string) => Promise<string | null>;
};

const ResolvedArtifactLink: React.FC<ResolvedArtifactLinkProps> = ({ originalHref, resolve, ...anchorProps }) => {
  const resolveRef = useRef(resolve);
  resolveRef.current = resolve;
  const [resolution, setResolution] = useState<{ key: string; value: string | null }>({
    key: '',
    value: null,
  });
  const resolvedHref = resolution.key === originalHref ? resolution.value : undefined;

  useEffect(() => {
    let active = true;
    void resolveRef
      .current(originalHref)
      .then((nextHref) => {
        if (active) setResolution({ key: originalHref, value: nextHref });
      })
      .catch((error) => {
        console.warn('[MarkdownView] Failed to resolve Synon Biomed artifact link:', error);
        if (active) setResolution({ key: originalHref, value: null });
      });
    return () => {
      active = false;
    };
  }, [originalHref]);

  return (
    <a
      {...anchorProps}
      className={classNames(anchorProps.className, 'synon-artifact-link')}
      href={resolvedHref ?? '#'}
      data-original-href={originalHref}
      data-synon-artifact-link='true'
      data-artifact-link-resolution={resolvedHref === undefined ? 'pending' : resolvedHref ? 'resolved' : 'unavailable'}
      aria-busy={resolvedHref === undefined ? true : undefined}
      target='_blank'
      rel='noreferrer'
    />
  );
};

const ResolvedArtifactImage: React.FC<{
  src: string;
  alt: string;
  className?: string;
  resolve: (src: string) => Promise<string | null>;
}> = ({ src, alt, className, resolve }) => {
  const resolveRef = useRef(resolve);
  resolveRef.current = resolve;
  const [resolution, setResolution] = useState<{ key: string; value: string | null }>({
    key: '',
    value: null,
  });
  const resolvedSrc = resolution.key === src ? resolution.value : undefined;
  const { ref, isNearViewport } = useNearViewport<HTMLSpanElement>();

  useEffect(() => {
    if (!isNearViewport) return;
    let active = true;
    void resolveRef
      .current(src)
      .then((nextSrc) => {
        if (active) setResolution({ key: src, value: nextSrc });
      })
      .catch((error) => {
        console.warn('[MarkdownView] Failed to resolve Synon Biomed artifact image:', error);
        if (active) setResolution({ key: src, value: null });
      });
    return () => {
      active = false;
    };
  }, [isNearViewport, src]);

  return (
    <span ref={ref}>
      {resolvedSrc === undefined ? (
        <span className='text-12px text-t-tertiary'>{alt}</span>
      ) : resolvedSrc ? (
        <img src={resolvedSrc} alt={alt} className={className} loading='lazy' decoding='async' fetchPriority='low' />
      ) : (
        <span className='text-12px text-t-tertiary'>{alt || src}</span>
      )}
    </span>
  );
};

const NearViewportImage: React.FC<React.ImgHTMLAttributes<HTMLImageElement>> = ({ src, alt = '', ...props }) => {
  const { ref, isNearViewport } = useNearViewport<HTMLSpanElement>();
  return (
    <span ref={ref}>
      {isNearViewport && src ? (
        <img {...props} src={src} alt={alt} loading='lazy' decoding='async' fetchPriority='low' />
      ) : (
        <span className='text-12px text-t-tertiary'>{alt}</span>
      )}
    </span>
  );
};

MarkdownView.displayName = 'MarkdownView';

export default MarkdownView;
