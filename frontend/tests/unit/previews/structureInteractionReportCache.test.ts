import { describe, expect, it, vi } from 'vitest';
import {
  StructureInteractionReportCache,
  type StructureInteractionDiagramResponse,
  type StructureInteractionReportResponse,
} from '@/renderer/pages/conversation/Preview/components/viewers/structureInteractionReportCache';

const report = (label: string): StructureInteractionReportResponse => ({
  ok: true,
  status: 'completed',
  report: {
    engine: 'Synon 2D Interaction Engine',
    engine_release: '1.0.0',
    ligand_label: label,
    pose_label: 'Pose 1',
    hydrogen_bond_count: 1,
    salt_bridge_count: 0,
    width: 1200,
    height: 1600,
    png_dpi: 300,
    interactions: [],
  },
});

const diagram = (label: string): StructureInteractionDiagramResponse => ({
  ...report(label),
  svg: `<svg><text>${label}</text></svg>`,
  png_base64: 'iVBORw0KGgo=',
});

describe('StructureInteractionReportCache', () => {
  it('shares report-only work between 3D consumers without starting publication rendering', async () => {
    const cache = new StructureInteractionReportCache();
    let resolve!: (value: StructureInteractionReportResponse) => void;
    const reportLoader = vi.fn(
      () =>
        new Promise<StructureInteractionReportResponse>((done) => {
          resolve = done;
        })
    );

    const first = cache.loadReport('frame:pose-1', reportLoader);
    const second = cache.loadReport('frame:pose-1', reportLoader);
    expect(first).toBe(second);
    expect(reportLoader).toHaveBeenCalledTimes(1);
    resolve(report('TYK2-026'));
    await expect(first).resolves.toMatchObject({ report: { ligand_label: 'TYK2-026' } });
    await cache.loadReport('frame:pose-1', reportLoader);
    expect(reportLoader).toHaveBeenCalledTimes(1);
  });

  it('loads one full diagram after a report-only result and lets later 3D consumers reuse it', async () => {
    const cache = new StructureInteractionReportCache();
    const reportLoader = vi.fn(async () => report('TYK2-026'));
    const diagramLoader = vi.fn(async () => diagram('TYK2-026'));

    await cache.loadReport('pose-1', reportLoader);
    await cache.loadDiagram('pose-1', diagramLoader);
    await cache.loadDiagram('pose-1', diagramLoader);
    await expect(cache.loadReport('pose-1', reportLoader)).resolves.toMatchObject({
      svg: expect.stringContaining('<svg>'),
    });

    expect(reportLoader).toHaveBeenCalledTimes(1);
    expect(diagramLoader).toHaveBeenCalledTimes(1);
  });

  it('lets a 3D consumer join an in-flight full diagram instead of starting report-only work', async () => {
    const cache = new StructureInteractionReportCache();
    let resolve!: (value: StructureInteractionDiagramResponse) => void;
    const diagramLoader = vi.fn(
      () =>
        new Promise<StructureInteractionDiagramResponse>((done) => {
          resolve = done;
        })
    );
    const reportLoader = vi.fn(async () => report('unexpected'));

    const full = cache.loadDiagram('pose-1', diagramLoader);
    const strength = cache.loadReport('pose-1', reportLoader);
    resolve(diagram('TYK2-026'));
    await expect(strength).resolves.toMatchObject({ svg: expect.stringContaining('<svg>') });
    await full;
    expect(reportLoader).not.toHaveBeenCalled();
  });

  it('does not cache failures and fences late results after the source is cleared', async () => {
    const cache = new StructureInteractionReportCache();
    const failed = vi.fn(async () => {
      throw new Error('unavailable');
    });
    await expect(cache.loadReport('pose-1', failed)).rejects.toThrow('unavailable');
    await expect(cache.loadReport('pose-1', failed)).rejects.toThrow('unavailable');
    expect(failed).toHaveBeenCalledTimes(2);

    let resolve!: (value: StructureInteractionReportResponse) => void;
    const stale = cache.loadReport(
      'pose-2',
      () =>
        new Promise((done) => {
          resolve = done;
        })
    );
    cache.clear();
    resolve(report('stale'));
    await stale;
    const currentLoader = vi.fn(async () => report('current'));
    await expect(cache.loadReport('pose-2', currentLoader)).resolves.toMatchObject({
      report: { ligand_label: 'current' },
    });
    expect(currentLoader).toHaveBeenCalledTimes(1);
  });

  it('evicts the least recently used completed result at the configured bound', async () => {
    const cache = new StructureInteractionReportCache(2);
    const loaderA = vi.fn(async () => report('a'));
    const loaderB = vi.fn(async () => report('b'));
    const loaderC = vi.fn(async () => report('c'));
    await cache.loadReport('a', loaderA);
    await cache.loadReport('b', loaderB);
    await cache.loadReport('a', loaderA);
    await cache.loadReport('c', loaderC);
    await cache.loadReport('b', loaderB);
    expect(loaderA).toHaveBeenCalledTimes(1);
    expect(loaderB).toHaveBeenCalledTimes(2);
  });
});
