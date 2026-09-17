/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { MolstarInteractionStrengthRecord } from './molstarInteractionStrengthLabels';

export type StructureInteractionReportResponse = {
  ok: boolean;
  status: string;
  svg?: string;
  png_base64?: string;
  report: {
    engine: string;
    engine_release: string;
    ligand_label: string;
    pose_label: string;
    hydrogen_bond_count: number;
    salt_bridge_count: number;
    width: number;
    height: number;
    png_dpi: number;
    interactions?: MolstarInteractionStrengthRecord[];
  };
};

export type StructureInteractionDiagramResponse = StructureInteractionReportResponse & {
  svg: string;
  png_base64: string;
};

// Keeps report-only and publication-diagram requests independently bounded.
// A full diagram can satisfy a later 3D report consumer, while a 3D-only view
// never pays the SVG/PNG rendering cost. A generation fence prevents late work
// from repopulating the cache after the structure source changes.
export class StructureInteractionReportCache {
  private readonly reports = new Map<string, StructureInteractionReportResponse>();
  private readonly diagrams = new Map<string, StructureInteractionDiagramResponse>();
  private readonly reportInflight = new Map<string, Promise<StructureInteractionReportResponse>>();
  private readonly diagramInflight = new Map<string, Promise<StructureInteractionDiagramResponse>>();
  private readonly lru = new Map<string, undefined>();
  private generation = 0;

  constructor(private readonly maxEntries = 24) {}

  loadReport(
    key: string,
    loader: () => Promise<StructureInteractionReportResponse>
  ): Promise<StructureInteractionReportResponse> {
    const diagram = this.diagrams.get(key);
    if (diagram) {
      this.touch('diagram', key);
      return Promise.resolve(diagram);
    }
    const cached = this.reports.get(key);
    if (cached) {
      this.touch('report', key);
      return Promise.resolve(cached);
    }
    const running = this.reportInflight.get(key);
    if (running) return running;
    const fullRunning = this.diagramInflight.get(key);
    if (fullRunning) return fullRunning;

    const generation = this.generation;
    const request = loader()
      .then((response) => {
        if (generation !== this.generation) return response;
        if (!this.diagrams.has(key)) {
          this.reports.set(key, response);
          this.touch('report', key);
        }
        return response;
      })
      .finally(() => {
        if (this.reportInflight.get(key) === request) this.reportInflight.delete(key);
      });
    this.reportInflight.set(key, request);
    return request;
  }

  loadDiagram(
    key: string,
    loader: () => Promise<StructureInteractionDiagramResponse>
  ): Promise<StructureInteractionDiagramResponse> {
    const cached = this.diagrams.get(key);
    if (cached) {
      this.touch('diagram', key);
      return Promise.resolve(cached);
    }
    const running = this.diagramInflight.get(key);
    if (running) return running;

    const generation = this.generation;
    const request = loader()
      .then((response) => {
        if (generation !== this.generation) return response;
        this.reports.delete(key);
        this.lru.delete(this.lruKey('report', key));
        this.diagrams.set(key, response);
        this.touch('diagram', key);
        return response;
      })
      .finally(() => {
        if (this.diagramInflight.get(key) === request) this.diagramInflight.delete(key);
      });
    this.diagramInflight.set(key, request);
    return request;
  }

  clear(): void {
    this.generation += 1;
    this.reports.clear();
    this.diagrams.clear();
    this.reportInflight.clear();
    this.diagramInflight.clear();
    this.lru.clear();
  }

  private touch(kind: 'report' | 'diagram', key: string): void {
    const lruKey = this.lruKey(kind, key);
    this.lru.delete(lruKey);
    this.lru.set(lruKey, undefined);
    while (this.lru.size > Math.max(1, this.maxEntries)) {
      const oldest = this.lru.keys().next().value;
      if (oldest === undefined) break;
      this.lru.delete(oldest);
      const separator = oldest.indexOf(':');
      const oldestKind = oldest.slice(0, separator);
      const oldestKey = oldest.slice(separator + 1);
      if (oldestKind === 'diagram') this.diagrams.delete(oldestKey);
      else this.reports.delete(oldestKey);
    }
  }

  private lruKey(kind: 'report' | 'diagram', key: string): string {
    return `${kind}:${key}`;
  }
}
