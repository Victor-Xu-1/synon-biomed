/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { createMolstarStructureEngine } from './molstarStructureEngine';
import { loadStructureContent, resolveStructureFormat } from './structureSource';

const THUMBNAIL_WIDTH = 304;
const THUMBNAIL_HEIGHT = 184;
const THUMBNAIL_CACHE_NAME = 'synon-biomed-structure-thumbnails-v1';
const THUMBNAIL_CACHE_PATH = '/__synon-biomed-cache__/structure-thumbnail';
const THUMBNAIL_RENDER_VERSION = 'molstar-304x184-v1';
const MAX_MEMORY_THUMBNAILS = 48;
const MAX_PERSISTENT_THUMBNAILS = 96;

type ThumbnailRenderJob = {
  controller: AbortController;
  consumers: Set<symbol>;
  promise: Promise<string>;
  settled: boolean;
};

const memoryThumbnailCache = new Map<string, string>();
const thumbnailRenderJobs = new Map<string, ThumbnailRenderJob>();

export async function renderStructureThumbnail({
  contentUrl,
  filename,
  signal,
}: {
  contentUrl: string;
  filename: string;
  signal: AbortSignal;
}): Promise<string> {
  if (signal.aborted) throw createAbortError();
  const cacheKey = resolveThumbnailCacheKey(contentUrl, filename);
  const memoryThumbnail = memoryThumbnailCache.get(cacheKey);
  if (memoryThumbnail) {
    rememberThumbnail(cacheKey, memoryThumbnail);
    return memoryThumbnail;
  }

  let job = thumbnailRenderJobs.get(cacheKey);
  if (!job) {
    job = createThumbnailRenderJob(cacheKey, contentUrl, filename);
    thumbnailRenderJobs.set(cacheKey, job);
  }
  return subscribeToThumbnailJob(job, signal);
}

function createThumbnailRenderJob(cacheKey: string, contentUrl: string, filename: string): ThumbnailRenderJob {
  const controller = new AbortController();
  const promise = (async () => {
    const cached = await readPersistentThumbnail(cacheKey);
    if (controller.signal.aborted) throw createAbortError();
    if (cached) {
      rememberThumbnail(cacheKey, cached);
      return cached;
    }

    const imageUrl = await renderStructureThumbnailUncached({
      contentUrl,
      filename,
      signal: controller.signal,
    });
    if (controller.signal.aborted) throw createAbortError();
    rememberThumbnail(cacheKey, imageUrl);
    await writePersistentThumbnail(cacheKey, imageUrl);
    return imageUrl;
  })();
  const job: ThumbnailRenderJob = {
    controller,
    consumers: new Set<symbol>(),
    promise,
    settled: false,
  };
  const settle = () => {
    job.settled = true;
    if (thumbnailRenderJobs.get(cacheKey) === job) thumbnailRenderJobs.delete(cacheKey);
  };
  void job.promise.then(settle, settle);
  return job;
}

function subscribeToThumbnailJob(job: ThumbnailRenderJob, signal: AbortSignal): Promise<string> {
  if (signal.aborted) return Promise.reject(createAbortError());
  const consumer = Symbol('thumbnail-consumer');
  job.consumers.add(consumer);

  return new Promise<string>((resolve, reject) => {
    let finished = false;
    const release = () => {
      if (finished) return;
      finished = true;
      signal.removeEventListener('abort', handleAbort);
      job.consumers.delete(consumer);
      if (job.consumers.size === 0 && !job.settled) job.controller.abort();
    };
    const handleAbort = () => {
      release();
      reject(createAbortError());
    };
    signal.addEventListener('abort', handleAbort, { once: true });
    void job.promise.then(
      (imageUrl) => {
        release();
        resolve(imageUrl);
      },
      (error: unknown) => {
        release();
        reject(error);
      }
    );
  });
}

async function renderStructureThumbnailUncached({
  contentUrl,
  filename,
  signal,
}: {
  contentUrl: string;
  filename: string;
  signal: AbortSignal;
}): Promise<string> {
  const host = createThumbnailHost();
  let engine: Awaited<ReturnType<typeof createMolstarStructureEngine>> | undefined;
  try {
    const format = resolveStructureFormat(filename);
    const [createdEngine, source] = await Promise.all([
      createMolstarStructureEngine(host, { mode: 'thumbnail' }),
      loadStructureContent({ contentUrl, filename, format, signal }),
    ]);
    engine = createdEngine;
    if (signal.aborted) throw createAbortError();
    await engine.load(source, filename, format);
    if (signal.aborted) throw createAbortError();
    return await engine.captureImage({ width: THUMBNAIL_WIDTH, height: THUMBNAIL_HEIGHT });
  } finally {
    engine?.dispose();
    host.remove();
  }
}

function resolveThumbnailCacheKey(contentUrl: string, filename: string): string {
  const sourceUrl = new URL(contentUrl, window.location.href).href;
  return `${THUMBNAIL_RENDER_VERSION}\n${filename}\n${sourceUrl}`;
}

function resolveThumbnailCacheRequest(cacheKey: string): string {
  const requestUrl = new URL(THUMBNAIL_CACHE_PATH, window.location.origin);
  requestUrl.searchParams.set('key', cacheKey);
  return requestUrl.href;
}

function rememberThumbnail(cacheKey: string, imageUrl: string): void {
  memoryThumbnailCache.delete(cacheKey);
  memoryThumbnailCache.set(cacheKey, imageUrl);
  while (memoryThumbnailCache.size > MAX_MEMORY_THUMBNAILS) {
    const oldestKey = memoryThumbnailCache.keys().next().value;
    if (typeof oldestKey !== 'string') break;
    memoryThumbnailCache.delete(oldestKey);
  }
}

async function readPersistentThumbnail(cacheKey: string): Promise<string | undefined> {
  if (typeof caches === 'undefined') return undefined;
  try {
    const cache = await caches.open(THUMBNAIL_CACHE_NAME);
    const response = await cache.match(resolveThumbnailCacheRequest(cacheKey));
    if (!response?.ok) return undefined;
    const imageUrl = await response.text();
    return imageUrl.startsWith('data:image/png') ? imageUrl : undefined;
  } catch {
    return undefined;
  }
}

async function writePersistentThumbnail(cacheKey: string, imageUrl: string): Promise<void> {
  if (typeof caches === 'undefined') return;
  try {
    const cache = await caches.open(THUMBNAIL_CACHE_NAME);
    await cache.put(
      resolveThumbnailCacheRequest(cacheKey),
      new Response(imageUrl, {
        headers: { 'content-type': 'text/plain; charset=utf-8' },
      })
    );
    const keys = await cache.keys();
    const overflow = keys.length - MAX_PERSISTENT_THUMBNAILS;
    if (overflow > 0) await Promise.all(keys.slice(0, overflow).map((request) => cache.delete(request)));
  } catch {
    // Persistent caching is an optimization. Rendering remains authoritative
    // when storage is unavailable, full, or disabled by the browser.
  }
}

function createThumbnailHost(): HTMLDivElement {
  const host = document.createElement('div');
  host.setAttribute('aria-hidden', 'true');
  Object.assign(host.style, {
    position: 'fixed',
    inset: '0 auto auto -10000px',
    width: '152px',
    height: '92px',
    overflow: 'hidden',
    pointerEvents: 'none',
    visibility: 'hidden',
  });
  document.body.appendChild(host);
  return host;
}

function createAbortError(): DOMException {
  return new DOMException('The structure thumbnail render was aborted.', 'AbortError');
}
