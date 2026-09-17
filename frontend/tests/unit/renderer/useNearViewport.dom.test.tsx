import React from 'react';
import { act, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { useNearViewport } from '@/renderer/hooks/ui/useNearViewport';

type ObserverCallback = IntersectionObserverCallback;

class TestIntersectionObserver {
  static instances: TestIntersectionObserver[] = [];
  readonly root = null;
  readonly rootMargin: string;
  readonly thresholds = [0];
  readonly observe = vi.fn();
  readonly unobserve = vi.fn();
  readonly disconnect = vi.fn();
  readonly takeRecords = () => [];

  constructor(
    private readonly callback: ObserverCallback,
    options?: IntersectionObserverInit
  ) {
    this.rootMargin = options?.rootMargin ?? '0px';
    TestIntersectionObserver.instances.push(this);
  }

  trigger(target: Element): void {
    this.callback([{ target, isIntersecting: true, intersectionRatio: 1 } as IntersectionObserverEntry], this);
  }
}

const Probe: React.FC<{ name: string }> = ({ name }) => {
  const { ref, isNearViewport } = useNearViewport<HTMLDivElement>();
  return <div ref={ref} data-testid={name} data-near={String(isNearViewport)} />;
};

describe('useNearViewport', () => {
  afterEach(() => {
    TestIntersectionObserver.instances = [];
    vi.unstubAllGlobals();
  });

  it('shares one observer and releases each target after its first near-viewport entry', () => {
    vi.stubGlobal('IntersectionObserver', TestIntersectionObserver);
    const rendered = render(
      <>
        <Probe name='first' />
        <Probe name='second' />
      </>
    );

    expect(TestIntersectionObserver.instances).toHaveLength(1);
    const observer = TestIntersectionObserver.instances[0];
    expect(observer.rootMargin).toBe('256px');
    expect(observer.observe).toHaveBeenCalledTimes(2);

    act(() => observer.trigger(screen.getByTestId('first')));
    expect(screen.getByTestId('first')).toHaveAttribute('data-near', 'true');
    expect(screen.getByTestId('second')).toHaveAttribute('data-near', 'false');
    expect(observer.unobserve).toHaveBeenCalledWith(screen.getByTestId('first'));

    const secondElement = screen.getByTestId('second');
    rendered.unmount();
    expect(observer.unobserve).toHaveBeenCalledWith(secondElement);
    expect(observer.disconnect).toHaveBeenCalledTimes(1);
  });

  it('loads immediately when IntersectionObserver is unavailable', () => {
    vi.stubGlobal('IntersectionObserver', undefined);
    render(<Probe name='fallback' />);
    expect(screen.getByTestId('fallback')).toHaveAttribute('data-near', 'true');
  });
});
