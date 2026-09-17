/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

export const CONVERSATION_ROUTE_PERFORMANCE_STORAGE_KEY = 'synon.performance.conversation-route';
const PERFORMANCE_PREFIX = 'synon:conversation-route';
const MAX_PERFORMANCE_SAMPLES = 32;

export type ConversationRoutePerformanceStage = 'shell' | 'message-first-paint' | 'settled';

export type ConversationRoutePerformanceSample = {
  navigation: number;
  stage: ConversationRoutePerformanceStage;
  durationMs: number;
};

type ActiveMeasurement = {
  navigation: number;
  stages: Set<ConversationRoutePerformanceStage>;
};

let navigationSequence = 0;
let activeMeasurement: ActiveMeasurement | null = null;
let observer: PerformanceObserver | null = null;
let samples: ConversationRoutePerformanceSample[] = [];

const markName = (navigation: number, stage: 'click' | ConversationRoutePerformanceStage) =>
  `${PERFORMANCE_PREFIX}:${stage}:${navigation}`;

const measurementEnabled = (): boolean => {
  if (typeof window === 'undefined') return false;
  try {
    return window.localStorage.getItem(CONVERSATION_ROUTE_PERFORMANCE_STORAGE_KEY) === '1';
  } catch {
    return false;
  }
};

const parseMeasure = (entry: PerformanceEntry): ConversationRoutePerformanceSample | null => {
  const match = /^synon:conversation-route:(shell|message-first-paint|settled):(\d+)$/.exec(entry.name);
  if (!match) return null;
  const navigation = Number(match[2]);
  if (!Number.isSafeInteger(navigation) || navigation <= 0 || !Number.isFinite(entry.duration)) return null;
  return {
    navigation,
    stage: match[1] as ConversationRoutePerformanceStage,
    durationMs: Math.max(0, entry.duration),
  };
};

const ensureObserver = (): void => {
  if (observer || typeof PerformanceObserver === 'undefined') return;
  try {
    observer = new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) {
        const sample = parseMeasure(entry);
        if (!sample) continue;
        samples = [...samples.slice(-(MAX_PERFORMANCE_SAMPLES - 1)), sample];
      }
    });
    observer.observe({ type: 'measure', buffered: false });
  } catch {
    observer?.disconnect();
    observer = null;
  }
};

const clearMeasurementEntries = (measurement: ActiveMeasurement | null): void => {
  if (!measurement || typeof performance === 'undefined') return;
  performance.clearMarks(markName(measurement.navigation, 'click'));
  for (const stage of measurement.stages) {
    performance.clearMarks(markName(measurement.navigation, stage));
    performance.clearMeasures(markName(measurement.navigation, stage));
  }
};

export function setConversationRoutePerformanceEnabled(enabled: boolean): void {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(CONVERSATION_ROUTE_PERFORMANCE_STORAGE_KEY, enabled ? '1' : '0');
  } catch {
    return;
  }
  if (!enabled) {
    clearMeasurementEntries(activeMeasurement);
    activeMeasurement = null;
    observer?.disconnect();
    observer = null;
  }
}

export function beginConversationRoutePerformance(): number | null {
  if (!measurementEnabled() || typeof performance === 'undefined') return null;
  ensureObserver();
  clearMeasurementEntries(activeMeasurement);
  const navigation = ++navigationSequence;
  activeMeasurement = { navigation, stages: new Set() };
  performance.mark(markName(navigation, 'click'));
  return navigation;
}

const markConversationRoutePerformanceStage = (navigation: number, stage: ConversationRoutePerformanceStage): void => {
  const active = activeMeasurement;
  if (!measurementEnabled() || !active || active.navigation !== navigation || active.stages.has(stage)) return;
  const end = markName(navigation, stage);
  performance.mark(end);
  performance.measure(end, { start: markName(navigation, 'click'), end });
  active.stages.add(stage);
};

export function scheduleConversationRoutePerformanceStage(
  stage: ConversationRoutePerformanceStage,
  frameCount = 1
): () => void {
  const navigation = activeMeasurement?.navigation;
  if (!navigation || !measurementEnabled() || typeof requestAnimationFrame === 'undefined') return () => {};
  let frame = 0;
  let frameId = 0;
  let cancelled = false;
  const advance = () => {
    if (cancelled || activeMeasurement?.navigation !== navigation) return;
    frame += 1;
    if (frame >= Math.max(1, Math.floor(frameCount))) {
      markConversationRoutePerformanceStage(navigation, stage);
      return;
    }
    frameId = requestAnimationFrame(advance);
  };
  frameId = requestAnimationFrame(advance);
  return () => {
    cancelled = true;
    cancelAnimationFrame(frameId);
  };
}

export function getConversationRoutePerformanceSamples(): ConversationRoutePerformanceSample[] {
  return samples.map((sample) => ({
    navigation: sample.navigation,
    stage: sample.stage,
    durationMs: sample.durationMs,
  }));
}
