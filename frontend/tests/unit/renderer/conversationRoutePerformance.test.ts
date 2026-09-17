// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

type ObserverCallback = (list: { getEntries: () => PerformanceEntry[] }) => void;

class TestPerformanceObserver {
  static callback: ObserverCallback | null = null;
  readonly observe = vi.fn();
  readonly disconnect = vi.fn();

  constructor(callback: ObserverCallback) {
    TestPerformanceObserver.callback = callback;
  }

  static emit(entries: PerformanceEntry[]): void {
    TestPerformanceObserver.callback?.({ getEntries: () => entries });
  }
}

const animationFrames: FrameRequestCallback[] = [];

describe('conversation route performance measurements', () => {
  beforeEach(async () => {
    vi.resetModules();
    localStorage.clear();
    animationFrames.length = 0;
    TestPerformanceObserver.callback = null;
    vi.stubGlobal('PerformanceObserver', TestPerformanceObserver);
    vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => {
      animationFrames.push(callback);
      return animationFrames.length;
    });
    vi.stubGlobal('cancelAnimationFrame', vi.fn());
    vi.spyOn(performance, 'mark').mockImplementation(() => ({}) as PerformanceMark);
    vi.spyOn(performance, 'measure').mockImplementation(
      (name) => ({ name, duration: 12, entryType: 'measure', startTime: 0, toJSON: () => ({}) }) as PerformanceMeasure
    );
    vi.spyOn(performance, 'clearMarks').mockImplementation(() => undefined);
    vi.spyOn(performance, 'clearMeasures').mockImplementation(() => undefined);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it('is disabled by default and emits no marks', async () => {
    const performanceModule = await import('@/renderer/pages/conversation/conversationRoutePerformance');

    expect(performanceModule.beginConversationRoutePerformance()).toBeNull();
    expect(performance.mark).not.toHaveBeenCalled();
  });

  it('records only fixed stage names with bounded local samples', async () => {
    const performanceModule = await import('@/renderer/pages/conversation/conversationRoutePerformance');
    performanceModule.setConversationRoutePerformanceEnabled(true);
    const token = performanceModule.beginConversationRoutePerformance();
    expect(token).toBeTypeOf('number');

    performanceModule.scheduleConversationRoutePerformanceStage('shell');
    animationFrames.shift()?.(0);
    expect(performance.measure).toHaveBeenCalledWith(
      `synon:conversation-route:shell:${token}`,
      expect.objectContaining({ start: `synon:conversation-route:click:${token}` })
    );

    TestPerformanceObserver.emit([
      {
        name: `synon:conversation-route:shell:${token}`,
        duration: 12,
        entryType: 'measure',
        startTime: 0,
        toJSON: () => ({}),
      } as PerformanceEntry,
    ]);
    expect(performanceModule.getConversationRoutePerformanceSamples()).toEqual([
      { navigation: token, stage: 'shell', durationMs: 12 },
    ]);
    expect(JSON.stringify(performanceModule.getConversationRoutePerformanceSamples())).not.toContain('conversation-');
  });

  it('drops a scheduled stage after a newer navigation begins', async () => {
    const performanceModule = await import('@/renderer/pages/conversation/conversationRoutePerformance');
    performanceModule.setConversationRoutePerformanceEnabled(true);
    performanceModule.beginConversationRoutePerformance();
    performanceModule.scheduleConversationRoutePerformanceStage('shell');
    performanceModule.beginConversationRoutePerformance();

    animationFrames.shift()?.(0);
    expect(performance.measure).not.toHaveBeenCalled();
  });
});
