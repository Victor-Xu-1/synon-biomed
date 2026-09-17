/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

// The interactive viewer must stay at native canvas density. A reduced scale
// makes labels, bonds, and pocket geometry visibly soft when the panel is
// enlarged; thumbnails retain their separate bounded performance profile.
export const MOLSTAR_VIEWPORT_PIXEL_SCALE = 1;
export const MOLSTAR_THUMBNAIL_PIXEL_SCALE = 0.6;

type DisposableMolstarPlugin = { dispose: () => void };
type UnmountableReactRoot = { unmount: () => void };
type MicrotaskScheduler = (operation: () => void) => void;

export function disposeMolstarUiResources(
  plugin: DisposableMolstarPlugin,
  reactRoot: UnmountableReactRoot | undefined,
  schedule: MicrotaskScheduler = queueMicrotask
): void {
  // Mol* owns its canvas/context lifecycle; the nested React root owns only
  // its UI tree. Dispose the engine now, then unmount that nested root after
  // the parent React commit finishes. Synchronous nested-root unmounting from
  // the parent cleanup races React's render phase, while manually removing the
  // host children gives React stale DOM ownership and a removeChild failure.
  plugin.dispose();
  if (reactRoot) schedule(() => reactRoot.unmount());
}

type AnimationFrameHost = Pick<Window, 'requestAnimationFrame' | 'cancelAnimationFrame'>;

export type AnimationFrameCoalescer = {
  schedule: () => void;
  cancel: () => void;
};

export function createAnimationFrameCoalescer(
  view: AnimationFrameHost,
  operation: () => void
): AnimationFrameCoalescer {
  let frame: number | null = null;
  return {
    schedule: () => {
      if (frame !== null) return;
      frame = view.requestAnimationFrame(() => {
        frame = null;
        operation();
      });
    },
    cancel: () => {
      if (frame === null) return;
      view.cancelAnimationFrame(frame);
      frame = null;
    },
  };
}

export type MolstarViewportResizeScheduler = {
  notify: (width: number, height: number) => void;
  dispose: () => void;
};

export function createMolstarViewportResizeScheduler(
  view: AnimationFrameHost,
  resize: () => void
): MolstarViewportResizeScheduler {
  let pendingWidth = -1;
  let pendingHeight = -1;
  let appliedWidth = -1;
  let appliedHeight = -1;
  const frame = createAnimationFrameCoalescer(view, () => {
    if (pendingWidth === appliedWidth && pendingHeight === appliedHeight) return;
    appliedWidth = pendingWidth;
    appliedHeight = pendingHeight;
    resize();
  });

  return {
    notify: (width, height) => {
      const nextWidth = Math.round(width);
      const nextHeight = Math.round(height);
      if (nextWidth <= 0 || nextHeight <= 0) return;
      if (nextWidth === pendingWidth && nextHeight === pendingHeight) return;
      pendingWidth = nextWidth;
      pendingHeight = nextHeight;
      frame.schedule();
    },
    dispose: frame.cancel,
  };
}
