/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { useCallback, useEffect, useRef, useState, type RefCallback } from 'react';

const DEFAULT_ROOT_MARGIN = '256px';

type ObserverPool = {
  observer: IntersectionObserver;
  listeners: WeakMap<Element, Set<() => void>>;
  targets: Set<Element>;
};

const observerPools = new Map<string, ObserverPool>();

function releasePool(rootMargin: string, pool: ObserverPool): void {
  if (pool.targets.size > 0) return;
  pool.observer.disconnect();
  if (observerPools.get(rootMargin) === pool) observerPools.delete(rootMargin);
}

function getObserverPool(rootMargin: string): ObserverPool {
  const existing = observerPools.get(rootMargin);
  if (existing) return existing;

  let pool!: ObserverPool;
  const observer = new IntersectionObserver(
    (entries) => {
      for (const entry of entries) {
        if (!entry.isIntersecting && entry.intersectionRatio <= 0) continue;
        const listeners = pool.listeners.get(entry.target);
        pool.listeners.delete(entry.target);
        pool.targets.delete(entry.target);
        pool.observer.unobserve(entry.target);
        listeners?.forEach((listener) => listener());
      }
      releasePool(rootMargin, pool);
    },
    { rootMargin }
  );
  pool = { observer, listeners: new WeakMap(), targets: new Set() };
  observerPools.set(rootMargin, pool);
  return pool;
}

function observeNearViewport(element: Element, rootMargin: string, listener: () => void): () => void {
  let pool: ObserverPool;
  try {
    pool = getObserverPool(rootMargin);
  } catch {
    listener();
    return () => {};
  }
  let listeners = pool.listeners.get(element);
  if (!listeners) {
    listeners = new Set();
    pool.listeners.set(element, listeners);
    pool.targets.add(element);
    pool.observer.observe(element);
  }
  listeners.add(listener);

  return () => {
    const current = pool.listeners.get(element);
    current?.delete(listener);
    if (current?.size) return;
    pool.listeners.delete(element);
    pool.targets.delete(element);
    pool.observer.unobserve(element);
    releasePool(rootMargin, pool);
  };
}

export function useNearViewport<T extends Element = HTMLElement>(
  rootMargin = DEFAULT_ROOT_MARGIN
): { ref: RefCallback<T>; isNearViewport: boolean } {
  const [isNearViewport, setIsNearViewport] = useState(() => typeof IntersectionObserver === 'undefined');
  const cleanupRef = useRef<(() => void) | null>(null);

  useEffect(
    () => () => {
      cleanupRef.current?.();
      cleanupRef.current = null;
    },
    []
  );

  const ref = useCallback<RefCallback<T>>(
    (element) => {
      cleanupRef.current?.();
      cleanupRef.current = null;
      if (!element || isNearViewport) return;
      if (typeof IntersectionObserver === 'undefined') {
        setIsNearViewport(true);
        return;
      }
      cleanupRef.current = observeNearViewport(element, rootMargin, () => {
        cleanupRef.current = null;
        setIsNearViewport(true);
      });
    },
    [isNearViewport, rootMargin]
  );

  return { ref, isNearViewport };
}
