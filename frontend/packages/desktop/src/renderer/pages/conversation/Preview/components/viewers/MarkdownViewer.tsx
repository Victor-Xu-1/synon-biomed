/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { joinPath } from '@/common/chat/chatLib';
import { ipcBridge } from '@/common';
import LocalFileLink from '@/renderer/components/Markdown/LocalFileLink';
import { resolveLocalFileLinkReference } from '@/renderer/components/Markdown/markdownUtils';
import { useTextSelection, type SelectionPosition } from '@/renderer/hooks/ui/useTextSelection';
import 'katex/dist/katex.min.css';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import rehypeKatex from 'rehype-katex';
import remarkBreaks from 'remark-breaks';
import { Streamdown, defaultRehypePlugins, defaultRemarkPlugins } from 'streamdown';
import { useContainerScroll, useContainerScrollTarget } from '../../hooks/useScrollSyncHelpers';
import { useLocalFilePreview, useThemeDetection } from '../../hooks';
import { getMarkdownShikiThemes, getMermaidTheme } from '../../theme';
import { convertLatexDelimiters } from '@/renderer/utils/chat/latexDelimiters';
import {
  createMarkdownArtifactReferenceResolverPlugin,
  isSameOriginArtifactVersionUrl,
  resolveMarkdownArtifactVersionReferences,
} from './markdownArtifactReferences';
import { useConversationArtifactIndex } from '@/renderer/pages/conversation/Messages/artifacts';
import { getSynonBiomedRelativeArtifactFilename } from '@/renderer/services/synonBiomedArtifactReferences';

const rehypeKatexWithStaticStyles = () => rehypeKatex();

interface MarkdownPreviewProps {
  content: string; // Markdown 内容 / Markdown content
  containerRef?: React.RefObject<HTMLDivElement>; // 容器引用，用于滚动同步 / Container ref for scroll sync
  onScroll?: (scrollTop: number, scrollHeight: number, clientHeight: number) => void; // 滚动回调 / Scroll callback
  file_path?: string; // 当前 Markdown 文件的绝对路径 / Absolute file path of current markdown
  workspace?: string;
  companionArtifactUrls?: Readonly<Record<string, string>>;
  resolveArtifactReference?: (reference: string) => string | null;
  onTextSelection?: (selection: MarkdownTextSelection | null) => void;
}

export interface MarkdownTextSelection {
  text: string;
  position: SelectionPosition;
}

const isDataOrRemoteUrl = (value?: string): boolean => {
  if (!value) return false;
  return /^(https?:|data:|blob:|file:)/i.test(value);
};

const isAbsoluteLocalPath = (value?: string): boolean => {
  if (!value) return false;
  return /^([a-zA-Z]:\\|\\\\|\/)/.test(value);
};

interface MarkdownImageProps extends React.ImgHTMLAttributes<HTMLImageElement> {
  baseDir?: string;
  workspace?: string;
  resolveArtifactReference?: (reference: string) => string | null;
}

const useImageResolverCache = () => {
  const cacheRef = useRef(new Map<string, string>());
  const inflightRef = useRef(new Map<string, Promise<string>>());

  const resolve = useCallback((key: string, loader: () => Promise<string>): Promise<string> => {
    const cache = cacheRef.current;
    if (cache.has(key)) {
      return Promise.resolve(cache.get(key)!);
    }

    const inflight = inflightRef.current;
    if (inflight.has(key)) {
      return inflight.get(key)!;
    }

    const promise = loader()
      .then((result) => {
        cache.set(key, result);
        return result;
      })
      .finally(() => {
        inflight.delete(key);
      });

    inflight.set(key, promise);
    return promise;
  }, []);

  return resolve;
};

export const MarkdownImage: React.FC<MarkdownImageProps> = ({
  src,
  alt,
  baseDir,
  workspace,
  resolveArtifactReference,
  ...props
}) => {
  const [resolvedSrc, setResolvedSrc] = useState<string | undefined>(undefined);
  const resolveImage = useImageResolverCache();

  useEffect(() => {
    let cancelled = false;

    const loadImage = () => {
      if (!src) {
        setResolvedSrc(undefined);
        return;
      }

      const artifactUrl = resolveArtifactReference?.(src);
      if (artifactUrl) {
        setResolvedSrc(artifactUrl);
        return;
      }

      if (isSameOriginArtifactVersionUrl(src)) {
        setResolvedSrc(src);
        return;
      }

      if (isDataOrRemoteUrl(src)) {
        if (/^https?:/i.test(src)) {
          resolveImage(src, () => ipcBridge.fs.fetchRemoteImage.invoke({ url: src }))
            .then((dataUrl) => {
              if (!cancelled) {
                setResolvedSrc(dataUrl);
              }
            })
            .catch((error) => {
              console.error('[MarkdownPreview] Failed to fetch remote image:', src, error);
              if (!cancelled) {
                setResolvedSrc(src);
              }
            });
          return;
        }
        setResolvedSrc(src);
        return;
      }

      const normalizedBase = baseDir ? baseDir.replace(/\\/g, '/') : undefined;
      const cleanedSrc = src.replace(/\\/g, '/');
      const absolutePath = isAbsoluteLocalPath(cleanedSrc)
        ? cleanedSrc
        : normalizedBase
          ? joinPath(normalizedBase, cleanedSrc)
          : cleanedSrc;

      if (!absolutePath) {
        setResolvedSrc(src);
        return;
      }

      resolveImage(absolutePath, async () => {
        const dataUrl = await ipcBridge.fs.getImageBase64.invoke({ path: absolutePath, workspace });
        return dataUrl ?? src;
      })
        .then((dataUrl) => {
          if (!cancelled) {
            setResolvedSrc(dataUrl);
          }
        })
        .catch((error) => {
          console.error('[MarkdownPreview] Failed to load local image:', { src, absolutePath, error });
          if (!cancelled) {
            setResolvedSrc(src);
          }
        });
    };

    loadImage();

    return () => {
      cancelled = true;
    };
  }, [src, baseDir, resolveArtifactReference, resolveImage, workspace]);

  if (!resolvedSrc) {
    return alt ? <span>{alt}</span> : null;
  }

  return (
    <img
      src={resolvedSrc}
      alt={alt}
      referrerPolicy='no-referrer'
      crossOrigin='anonymous'
      style={{ maxWidth: '100%', width: 'auto', height: 'auto', display: 'block', objectFit: 'contain' }}
      {...props}
    />
  );
};

const encodeHtmlAttribute = (value: string) => value.replace(/&(?!#?[a-z0-9]+;)/gi, '&amp;');

const rewriteExternalMediaUrls = (markdown: string): string => {
  const githubWikiRegex = /https:\/\/github\.com\/([^/]+)\/([^/]+)\/wiki\/([^\s)"'>]+)/gi;
  const rewriteWiki = markdown.replace(githubWikiRegex, (_match, owner, repo, rest) => {
    return `https://raw.githubusercontent.com/wiki/${owner}/${repo}/${rest}`;
  });
  return rewriteWiki.replace(/<(img|a)\b[^>]*>/gi, (tag) => {
    return tag.replace(/(src|href)\s*=\s*(["'])([^"']*)(\2)/gi, (match, attr, quote, value, closingQuote) => {
      return `${attr}=${quote}${encodeHtmlAttribute(value)}${closingQuote}`;
    });
  });
};

const normalizeLocalFileSchemeLinks = (markdown: string): string => {
  return markdown.replace(/file:\/\//gi, '');
};

/**
 * Markdown 预览组件
 * Markdown preview component
 *
 * 使用 Streamdown 原生渲染 Markdown（Shiki 代码高亮、Mermaid、KaTeX）。
 * Editing is owned exclusively by DocumentWysiwygEditor.
 */
const MarkdownPreview: React.FC<MarkdownPreviewProps> = ({
  content,
  containerRef: externalContainerRef,
  onScroll: externalOnScroll,
  file_path,
  workspace,
  companionArtifactUrls,
  resolveArtifactReference,
  onTextSelection,
}) => {
  const internalContainerRef = useRef<HTMLDivElement>(null);
  const containerRef = externalContainerRef || internalContainerRef; // 使用外部 ref 或内部 ref / Use external ref or internal ref
  const currentTheme = useThemeDetection();
  const handleLocalFileLink = useLocalFilePreview(workspace);
  const artifactIndex = useConversationArtifactIndex();
  const resolvePreviewArtifactReference = useCallback(
    (reference: string): string | null => {
      const explicitlyResolved = resolveArtifactReference?.(reference);
      if (explicitlyResolved) return explicitlyResolved;
      if (!workspace?.startsWith('synonbiomed://')) return null;
      const filename = getSynonBiomedRelativeArtifactFilename(reference);
      if (!filename) return null;
      const companionUrl = companionArtifactUrls?.[filename.toLocaleLowerCase()];
      if (companionUrl) return companionUrl;
      return artifactIndex.byFilename.get(filename.toLocaleLowerCase())?.content_url ?? null;
    },
    [artifactIndex, companionArtifactUrls, resolveArtifactReference, workspace]
  );
  const artifactReferenceRemarkPlugin = useMemo(
    () => createMarkdownArtifactReferenceResolverPlugin(resolvePreviewArtifactReference),
    [resolvePreviewArtifactReference]
  );

  // 使用滚动同步 Hooks / Use scroll sync hooks
  useContainerScroll(containerRef, externalOnScroll);
  useContainerScrollTarget(containerRef);

  // 预览源：转换 LaTeX 分隔符并重写外部媒体 URL / Preview source: convert LaTeX delimiters and rewrite external media URLs
  const previewSource = useMemo(
    () =>
      resolveMarkdownArtifactVersionReferences(
        convertLatexDelimiters(normalizeLocalFileSchemeLinks(rewriteExternalMediaUrls(content)))
      ),
    [content]
  );

  // 监听文本选择 / Monitor text selection
  const { selectedText, selectionPosition } = useTextSelection(containerRef);

  useEffect(() => {
    onTextSelection?.(selectedText && selectionPosition ? { text: selectedText, position: selectionPosition } : null);
  }, [onTextSelection, selectedText, selectionPosition]);

  const baseDir = useMemo(() => {
    if (!file_path) return undefined;
    const normalized = file_path.replace(/\\/g, '/');
    const lastSlash = normalized.lastIndexOf('/');
    if (lastSlash === -1) return undefined;
    return normalized.slice(0, lastSlash);
  }, [file_path]);

  return (
    <div className='preview-markdown flex flex-col w-full h-full overflow-hidden'>
      {/* 内容区域 / Content area */}
      <div
        ref={containerRef}
        className='preview-content-scroll preview-markdown__scroll flex-1 overflow-auto text-t-primary'
        style={{ minWidth: 0 }}
      >
        <div
          className='synon-ai-markdown preview-markdown__document'
          style={{
            wordWrap: 'break-word',
            overflowWrap: 'break-word',
            width: '100%',
            maxWidth: '100%',
            minWidth: 0,
            boxSizing: 'border-box',
          }}
        >
          <Streamdown
            isAnimating={false}
            shikiTheme={getMarkdownShikiThemes()}
            mermaidConfig={{ theme: getMermaidTheme(currentTheme) }}
            controls={{ table: false, mermaid: false }}
            remarkPlugins={[...Object.values(defaultRemarkPlugins), remarkBreaks, artifactReferenceRemarkPlugin]}
            rehypePlugins={[defaultRehypePlugins.raw, defaultRehypePlugins.harden, rehypeKatexWithStaticStyles]}
            components={{
              a({ href, children, ...props }: React.AnchorHTMLAttributes<HTMLAnchorElement>) {
                if (isSameOriginArtifactVersionUrl(href)) {
                  return (
                    <a href={href} target='_blank' rel='noreferrer' {...props}>
                      {children}
                    </a>
                  );
                }
                const artifactHref = typeof href === 'string' ? resolvePreviewArtifactReference(href) : null;
                if (artifactHref) {
                  return (
                    <a href={artifactHref} target='_blank' rel='noreferrer' {...props}>
                      {children}
                    </a>
                  );
                }
                const localFileReference = resolveLocalFileLinkReference(typeof href === 'string' ? href : '');
                if (localFileReference) {
                  return (
                    <LocalFileLink reference={localFileReference} onOpen={handleLocalFileLink}>
                      {children}
                    </LocalFileLink>
                  );
                }
                return (
                  <a href={href} target='_blank' rel='noreferrer' {...props}>
                    {children}
                  </a>
                );
              },
              img({ src, alt, ...props }: React.ImgHTMLAttributes<HTMLImageElement>) {
                return (
                  <MarkdownImage
                    src={src}
                    alt={alt}
                    baseDir={baseDir}
                    workspace={workspace}
                    resolveArtifactReference={resolvePreviewArtifactReference}
                    {...props}
                  />
                );
              },
            }}
          >
            {previewSource}
          </Streamdown>
        </div>
      </div>
    </div>
  );
};

export default MarkdownPreview;
