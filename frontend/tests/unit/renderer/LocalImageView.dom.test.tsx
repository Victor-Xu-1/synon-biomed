import React from 'react';
import { act, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const imageMocks = vi.hoisted(() => ({ invoke: vi.fn() }));

vi.mock('@/common', () => ({
  ipcBridge: { fs: { getImageBase64: { invoke: imageMocks.invoke } } },
}));
vi.mock('@icon-park/react', () => ({ LoadingTwo: () => <span data-testid='loading-icon' /> }));

import LocalImageView from '@/renderer/components/media/LocalImageView';

let observerCallback: IntersectionObserverCallback | undefined;
let observedTarget: Element | undefined;

describe('LocalImageView', () => {
  beforeEach(() => {
    imageMocks.invoke.mockReset();
    observerCallback = undefined;
    observedTarget = undefined;
    vi.stubGlobal(
      'IntersectionObserver',
      class {
        constructor(callback: IntersectionObserverCallback) {
          observerCallback = callback;
        }
        observe(target: Element) {
          observedTarget = target;
        }
        unobserve() {}
        disconnect() {}
      }
    );
  });

  afterEach(() => vi.unstubAllGlobals());

  it('does not read or encode a local image until it is near the viewport', async () => {
    imageMocks.invoke.mockResolvedValue('data:image/png;base64,AAAA');
    render(<LocalImageView src='figure.png' alt='Figure' />);

    expect(imageMocks.invoke).not.toHaveBeenCalled();
    if (!observedTarget) throw new Error('local image was not observed');
    act(() =>
      observerCallback?.(
        [{ target: observedTarget, isIntersecting: true, intersectionRatio: 1 } as IntersectionObserverEntry],
        {} as IntersectionObserver
      )
    );

    expect(await screen.findByRole('img', { name: 'Figure' })).toHaveAttribute('src', 'data:image/png;base64,AAAA');
    expect(imageMocks.invoke).toHaveBeenCalledTimes(1);
  });

  it('ignores a late image result after the requested source changes', async () => {
    const first = deferred<string>();
    const second = deferred<string>();
    imageMocks.invoke.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    const rendered = render(<LocalImageView src='first.png' alt='Figure' />);
    if (!observedTarget) throw new Error('local image was not observed');
    act(() =>
      observerCallback?.(
        [{ target: observedTarget, isIntersecting: true, intersectionRatio: 1 } as IntersectionObserverEntry],
        {} as IntersectionObserver
      )
    );
    rendered.rerender(<LocalImageView src='second.png' alt='Figure' />);

    await act(async () => first.resolve('data:image/png;base64,FIRST'));
    expect(screen.queryByRole('img', { name: 'Figure' })).toBeNull();
    await act(async () => second.resolve('data:image/png;base64,SECOND'));
    expect(await screen.findByRole('img', { name: 'Figure' })).toHaveAttribute('src', 'data:image/png;base64,SECOND');
  });
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}
