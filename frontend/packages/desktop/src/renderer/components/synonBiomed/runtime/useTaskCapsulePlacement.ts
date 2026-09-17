/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type CSSProperties,
  type MouseEvent as ReactMouseEvent,
  type PointerEvent as ReactPointerEvent,
} from 'react';

type Point = {
  x: number;
  y: number;
};

type StoredPresentation = {
  position?: Point | null;
  minimized?: boolean;
};

type ScopedPresentation = Required<StoredPresentation> & {
  storageKey: string | null;
};

type DragSession = {
  pointerId: number;
  startClientX: number;
  startClientY: number;
  moved: boolean;
};

const STORAGE_KEY_PREFIX = 'synon-biomed.task-status.presentation.v5';
const VIEWPORT_MARGIN = 8;
const DRAG_THRESHOLD = 4;
const MINIMIZED_DIAMETER = 30;
const ACTION_SELECTOR = '[data-task-capsule-action="true"]';

const containsPoint = (bounds: DOMRect, point: Point): boolean =>
  bounds.width > 0 &&
  bounds.height > 0 &&
  point.x >= bounds.left &&
  point.x <= bounds.right &&
  point.y >= bounds.top &&
  point.y <= bounds.bottom;

const clamp = (value: number, minimum: number, maximum: number): number =>
  Math.min(Math.max(value, minimum), Math.max(minimum, maximum));

const viewportBounds = () => ({
  width: document.documentElement.clientWidth || window.innerWidth,
  height: document.documentElement.clientHeight || window.innerHeight,
});

const presentationStorageKey = (scope: string | null | undefined): string | null => {
  const normalized = scope?.trim();
  return normalized ? `${STORAGE_KEY_PREFIX}.${encodeURIComponent(normalized)}` : null;
};

const loadPresentation = (storageKey: string | null): ScopedPresentation => {
  const fallback: ScopedPresentation = { storageKey, position: null, minimized: false };
  if (!storageKey) return fallback;
  try {
    const raw = window.localStorage.getItem(storageKey);
    if (!raw) return fallback;
    const parsed = JSON.parse(raw) as StoredPresentation;
    const x = Number(parsed.position?.x);
    const y = Number(parsed.position?.y);
    return {
      storageKey,
      position: Number.isFinite(x) && Number.isFinite(y) ? { x, y } : null,
      minimized: parsed.minimized === true,
    };
  } catch {
    return fallback;
  }
};

const savePresentation = ({ storageKey, position, minimized }: ScopedPresentation) => {
  if (!storageKey) return;
  try {
    window.localStorage.setItem(storageKey, JSON.stringify({ position, minimized }));
  } catch {
    // Browser privacy modes may disable storage. The current interaction still works.
  }
};

export const useTaskCapsulePlacement = (storageScope: string | null | undefined, onDragStart?: () => void) => {
  const storageKey = presentationStorageKey(storageScope);
  const capsuleRef = useRef<HTMLDivElement | null>(null);
  const dragSessionRef = useRef<DragSession | null>(null);
  const onDragStartRef = useRef(onDragStart);
  const suppressClickRef = useRef(false);
  const clickSuppressionTimerRef = useRef<number | null>(null);
  const [storedPresentation, setStoredPresentation] = useState<ScopedPresentation>(() => loadPresentation(storageKey));
  const [dragging, setDragging] = useState(false);
  const presentation = storedPresentation.storageKey === storageKey ? storedPresentation : loadPresentation(storageKey);
  const { position, minimized } = presentation;

  useEffect(() => {
    onDragStartRef.current = onDragStart;
  }, [onDragStart]);

  const clampPosition = useCallback((requested: Point, size?: Pick<DOMRect, 'width' | 'height'>): Point => {
    const bounds = size ?? capsuleRef.current?.getBoundingClientRect();
    if (!bounds || bounds.width <= 0 || bounds.height <= 0) return requested;
    const viewport = viewportBounds();
    return {
      x: clamp(requested.x, VIEWPORT_MARGIN, viewport.width - bounds.width - VIEWPORT_MARGIN),
      y: clamp(requested.y, VIEWPORT_MARGIN, viewport.height - bounds.height - VIEWPORT_MARGIN),
    };
  }, []);

  useLayoutEffect(() => {
    dragSessionRef.current = null;
    suppressClickRef.current = false;
    setDragging(false);
    if (clickSuppressionTimerRef.current !== null) {
      window.clearTimeout(clickSuppressionTimerRef.current);
      clickSuppressionTimerRef.current = null;
    }
    setStoredPresentation((current) => (current.storageKey === storageKey ? current : loadPresentation(storageKey)));
  }, [storageKey]);

  const persist = useCallback((nextPresentation: ScopedPresentation) => {
    savePresentation(nextPresentation);
  }, []);

  const setMinimized = useCallback(
    (nextMinimized: boolean) => {
      setStoredPresentation((current) => {
        if (current.storageKey !== storageKey) return current;
        const next = { ...current, minimized: nextMinimized };
        persist(next);
        return next;
      });
    },
    [persist, storageKey]
  );

  const onPointerDown = useCallback((event: ReactPointerEvent<HTMLDivElement>) => {
    if (event.button !== 0 || !event.isPrimary) return;
    const target = event.target;
    if (target instanceof Element && target.closest(ACTION_SELECTOR)) return;
    if (!capsuleRef.current) return;
    dragSessionRef.current = {
      pointerId: event.pointerId,
      startClientX: event.clientX,
      startClientY: event.clientY,
      moved: false,
    };
  }, []);

  const onPointerMove = useCallback(
    (event: PointerEvent) => {
      const session = dragSessionRef.current;
      if (!session || session.pointerId !== event.pointerId) return;
      const deltaX = event.clientX - session.startClientX;
      const deltaY = event.clientY - session.startClientY;
      if (!session.moved && Math.hypot(deltaX, deltaY) < DRAG_THRESHOLD) return;
      if (!session.moved) {
        session.moved = true;
        setDragging(true);
        onDragStartRef.current?.();
      }
      const nextPosition = clampPosition(
        { x: event.clientX - MINIMIZED_DIAMETER / 2, y: event.clientY - MINIMIZED_DIAMETER / 2 },
        { width: MINIMIZED_DIAMETER, height: MINIMIZED_DIAMETER }
      );
      setStoredPresentation((current) =>
        current.storageKey === storageKey ? { ...current, position: nextPosition, minimized: true } : current
      );
      event.preventDefault();
    },
    [clampPosition, storageKey]
  );

  const finishDrag = useCallback(
    (event: PointerEvent) => {
      const session = dragSessionRef.current;
      if (!session || session.pointerId !== event.pointerId) return;
      dragSessionRef.current = null;
      if (session.moved) {
        const dockBounds = capsuleRef.current?.parentElement?.getBoundingClientRect();
        const droppedInDock = dockBounds ? containsPoint(dockBounds, { x: event.clientX, y: event.clientY }) : false;
        suppressClickRef.current = true;
        if (clickSuppressionTimerRef.current !== null) window.clearTimeout(clickSuppressionTimerRef.current);
        clickSuppressionTimerRef.current = window.setTimeout(() => {
          suppressClickRef.current = false;
          clickSuppressionTimerRef.current = null;
        }, 0);
        setDragging(false);
        setStoredPresentation((current) => {
          if (current.storageKey !== storageKey) return current;
          const next = droppedInDock
            ? { ...current, position: null, minimized: false }
            : { ...current, minimized: true };
          persist(next);
          return next;
        });
      }
    },
    [persist, storageKey]
  );

  const onClickCapture = useCallback((event: ReactMouseEvent<HTMLDivElement>) => {
    if (!suppressClickRef.current) return;
    suppressClickRef.current = false;
    if (clickSuppressionTimerRef.current !== null) {
      window.clearTimeout(clickSuppressionTimerRef.current);
      clickSuppressionTimerRef.current = null;
    }
    event.preventDefault();
    event.stopPropagation();
  }, []);

  const reconcilePlacement = useCallback(() => {
    setStoredPresentation((current) => {
      if (current.storageKey !== storageKey || current.position === null) return current;
      const nextPosition = clampPosition(current.position);
      if (nextPosition.x === current.position.x && nextPosition.y === current.position.y) return current;
      const next = { ...current, position: nextPosition };
      persist(next);
      return next;
    });
  }, [clampPosition, persist, storageKey]);

  useLayoutEffect(reconcilePlacement, [minimized, reconcilePlacement]);

  useEffect(() => {
    window.addEventListener('pointermove', onPointerMove, { passive: false });
    window.addEventListener('pointerup', finishDrag);
    window.addEventListener('pointercancel', finishDrag);
    return () => {
      window.removeEventListener('pointermove', onPointerMove);
      window.removeEventListener('pointerup', finishDrag);
      window.removeEventListener('pointercancel', finishDrag);
    };
  }, [finishDrag, onPointerMove]);

  useEffect(() => {
    window.addEventListener('resize', reconcilePlacement);
    return () => {
      window.removeEventListener('resize', reconcilePlacement);
    };
  }, [reconcilePlacement]);

  useEffect(
    () => () => {
      dragSessionRef.current = null;
      if (clickSuppressionTimerRef.current !== null) window.clearTimeout(clickSuppressionTimerRef.current);
    },
    []
  );

  const capsuleStyle: CSSProperties | undefined = position
    ? { position: 'fixed', left: `${position.x}px`, top: `${position.y}px` }
    : undefined;

  return {
    capsuleRef,
    capsuleStyle,
    minimized,
    setMinimized,
    dragging,
    detached: position !== null,
    capsulePointerProps: {
      onPointerDown,
      onClickCapture,
    },
  };
};
