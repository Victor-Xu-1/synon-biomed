import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

class MockWorker {
  static instances: MockWorker[] = [];
  static constructorAttempts = 0;
  static constructorError: Error | null = null;
  static postMessageError: Error | null = null;

  readonly url: string | URL;
  readonly options?: WorkerOptions;
  readonly messages: unknown[] = [];
  readonly terminate = vi.fn();
  private readonly listeners = new Map<string, Array<(event: { data?: unknown }) => void>>();

  constructor(url: string | URL, options?: WorkerOptions) {
    MockWorker.constructorAttempts += 1;
    if (MockWorker.constructorError) throw MockWorker.constructorError;
    this.url = url;
    this.options = options;
    MockWorker.instances.push(this);
  }

  addEventListener(type: string, listener: (event: { data?: unknown }) => void): void {
    const listeners = this.listeners.get(type) ?? [];
    listeners.push(listener);
    this.listeners.set(type, listeners);
  }

  postMessage(message: unknown, _transfer?: Transferable[]): void {
    if (MockWorker.postMessageError) throw MockWorker.postMessageError;
    this.messages.push(message);
  }

  emitMessage(data: unknown): void {
    for (const listener of this.listeners.get('message') ?? []) listener({ data });
  }

  emit(type: 'error' | 'messageerror'): void {
    for (const listener of this.listeners.get(type) ?? []) listener({});
  }
}

describe('RDKit isolated worker client', () => {
  beforeEach(() => {
    vi.resetModules();
    MockWorker.instances = [];
    MockWorker.constructorAttempts = 0;
    MockWorker.constructorError = null;
    MockWorker.postMessageError = null;
    vi.stubGlobal('Worker', MockWorker);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it('uses one fixed same-origin worker and validates successful SVG responses', async () => {
    const { renderMoleculeSvg } = await import('@/renderer/services/rdkitBrowser');
    const first = renderMoleculeSvg(' CCO ', 88, 64);
    const second = renderMoleculeSvg('CCN', 280, 190);

    expect(MockWorker.instances).toHaveLength(1);
    const worker = MockWorker.instances[0];
    expect(String(worker.url)).toBe(new URL('rdkit/rdkit-worker.js', document.baseURI).toString());
    expect(worker.options).toEqual({ name: 'synon-rdkit' });
    expect(worker.messages).toEqual([
      {
        version: 1,
        id: 1,
        operation: 'render_svg',
        source: 'CCO',
        width: 88,
        height: 64,
      },
      {
        version: 1,
        id: 2,
        operation: 'render_svg',
        source: 'CCN',
        width: 280,
        height: 190,
      },
    ]);

    worker.emitMessage({
      version: 1,
      id: 2,
      ok: true,
      svg: '<svg><text>CCN</text></svg>',
    });
    worker.emitMessage({
      version: 1,
      id: 1,
      ok: true,
      svg: '<svg><text>CCO</text></svg>',
    });
    await expect(first).resolves.toContain('CCO');
    await expect(second).resolves.toContain('CCN');
  });

  it('validates an edited mol block and returns canonical RDKit molecule data', async () => {
    const { validateAndRenderMolBlock } = await import('@/renderer/services/rdkitBrowser');
    const molBlock =
      'edited ligand\n  Synon Biomed\n\n  1  0  0  0  0  0            999 V2000\n    0.0000    0.0000    0.0000 C   0  0\nM  END';
    const result = validateAndRenderMolBlock(molBlock, 320, 220);
    const worker = MockWorker.instances[0];
    expect(worker.messages[0]).toEqual({
      version: 1,
      id: 1,
      operation: 'validate_molblock',
      source: molBlock,
      width: 320,
      height: 220,
    });
    worker.emitMessage({
      version: 1,
      id: 1,
      ok: true,
      svg: '<svg>ligand</svg>',
      molBlock: `${molBlock}\n`,
      smiles: 'C',
    });
    await expect(result).resolves.toEqual({
      svg: '<svg>ligand</svg>',
      molBlock,
      smiles: 'C',
    });
  });

  it('returns null for invalid SMILES and rejects invalid inputs before creating a worker', async () => {
    const { renderMoleculeSvg } = await import('@/renderer/services/rdkitBrowser');
    await expect(renderMoleculeSvg('', 88, 64)).rejects.toThrow('Invalid molecule input');
    await expect(renderMoleculeSvg('CCO', 0, 64)).rejects.toThrow('Invalid molecule render dimensions');
    expect(MockWorker.instances).toHaveLength(0);

    const result = renderMoleculeSvg('invalid', 88, 64);
    MockWorker.instances[0].emitMessage({
      version: 1,
      id: 1,
      ok: false,
      error: 'invalid_smiles',
    });
    await expect(result).resolves.toBeNull();

    const failedRender = renderMoleculeSvg('CCO', 88, 64);
    MockWorker.instances[0].emitMessage({
      version: 1,
      id: 2,
      ok: false,
      error: 'render_failed',
    });
    await expect(failedRender).resolves.toBeNull();
  });

  it('turns synchronous worker construction and post failures into typed rejected promises', async () => {
    const { RDKitRenderError, renderMoleculeSvg } = await import('@/renderer/services/rdkitBrowser');
    MockWorker.constructorError = new Error('sensitive constructor detail');
    const firstConstruction = renderMoleculeSvg('CCO', 88, 64);
    const secondConstruction = renderMoleculeSvg('CCN', 88, 64);
    await expect(firstConstruction).rejects.toEqual(
      new RDKitRenderError('runtime_unavailable', 'RDKit worker is unavailable')
    );
    await expect(secondConstruction).rejects.toEqual(
      new RDKitRenderError('runtime_unavailable', 'RDKit worker is unavailable')
    );
    expect(MockWorker.constructorAttempts).toBe(1);
    expect(MockWorker.instances).toHaveLength(0);

    MockWorker.constructorError = null;
    await Promise.resolve();
    MockWorker.postMessageError = new Error('sensitive post detail');
    await expect(renderMoleculeSvg('CCN', 88, 64)).rejects.toMatchObject({
      name: 'RDKitRenderError',
      code: 'runtime_unavailable',
      message: 'RDKit worker failed',
    });
    expect(MockWorker.instances).toHaveLength(1);
    expect(MockWorker.instances[0].terminate).toHaveBeenCalledTimes(1);
  });

  it('terminates a failed authority, rejects its callers, and creates a fresh worker later', async () => {
    const { renderMoleculeSvg } = await import('@/renderer/services/rdkitBrowser');
    const failed = renderMoleculeSvg('CCO', 88, 64);
    const firstWorker = MockWorker.instances[0];
    firstWorker.emit('error');
    await expect(failed).rejects.toMatchObject({
      code: 'runtime_unavailable',
      message: 'RDKit worker failed',
    });
    expect(firstWorker.terminate).toHaveBeenCalledTimes(1);

    const recovered = renderMoleculeSvg('CCN', 88, 64);
    expect(MockWorker.instances).toHaveLength(2);
    MockWorker.instances[1].emitMessage({
      version: 1,
      id: 2,
      ok: true,
      svg: '<svg>CCN</svg>',
    });
    await expect(recovered).resolves.toContain('CCN');
  });

  it('fails closed on a malformed response and disposes all in-flight work', async () => {
    const { disposeRdkitWorker, renderMoleculeSvg } = await import('@/renderer/services/rdkitBrowser');
    const first = renderMoleculeSvg('CCO', 88, 64);
    const second = renderMoleculeSvg('CCN', 88, 64);
    const worker = MockWorker.instances[0];
    worker.emitMessage({
      version: 1,
      id: 1,
      ok: true,
      svg: '<script>bad</script>',
    });
    await expect(first).rejects.toThrow('invalid SVG');
    await expect(second).rejects.toThrow('invalid SVG');
    disposeRdkitWorker();
    expect(worker.terminate).toHaveBeenCalledTimes(1);
  });
});
