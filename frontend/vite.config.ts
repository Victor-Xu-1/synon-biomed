import { readFileSync } from 'node:fs';
import type { ClientRequest, ServerResponse } from 'node:http';
import { resolve } from 'node:path';
import type { Duplex } from 'node:stream';
import react from '@vitejs/plugin-react';
import { defineConfig, normalizePath, type PluginOption, type ProxyOptions } from 'vite';
import UnoCSS from '@unocss/vite';
import { viteStaticCopy } from 'vite-plugin-static-copy';
import unoConfig from './uno.config';

const repoRoot = import.meta.dirname;
const rendererRoot = resolve(repoRoot, 'packages/desktop/src/renderer');
const rootPackage = JSON.parse(readFileSync(resolve(repoRoot, 'package.json'), 'utf8')) as { version: string };

export function portableStaticCopySource(pathname: string): string {
  return normalizePath(pathname.replaceAll('\\', '/'));
}

/** Keep Mol*'s intentionally circular model/plugin graph in one lazy chunk. */
export function rendererManualChunk(id: string): string | undefined {
  const normalized = normalizePath(id.replaceAll('\\', '/'));
  if (/\/node_modules\/(?:react|react-dom|react-is|scheduler|use-sync-external-store)(?:\/|$)/.test(normalized)) {
    return 'react-vendor';
  }
  if (normalized.includes('/node_modules/mdast-util-to-markdown/')) return 'markdown-vendor';
  if (normalized.includes('/node_modules/tslib/')) return 'runtime-vendor';
  return normalized.includes('/node_modules/molstar/') ? 'molstar' : undefined;
}

export const rendererOnlyExplicitManualChunks = false;

const previewHashedAssetName = /-[A-Za-z0-9_-]{8,}\.[A-Za-z0-9]+$/;
const previewReservedPrefixes = ['/api', '/health', '/login', '/logout', '/synon-link', '/v1', '/ws'];

export function productionPreviewCacheControl(pathname: string): string | undefined {
  const normalized = pathname.split('?', 1)[0] || '/';
  if (previewReservedPrefixes.some((prefix) => normalized === prefix || normalized.startsWith(`${prefix}/`))) {
    return undefined;
  }
  if (
    normalized === '/' ||
    normalized.endsWith('.html') ||
    !normalized.slice(normalized.lastIndexOf('/') + 1).includes('.')
  ) {
    return 'no-cache';
  }
  if (normalized === '/theme-init.js' || normalized.startsWith('/rdkit/')) {
    return 'no-cache, must-revalidate';
  }
  return previewHashedAssetName.test(normalized) ? 'public, max-age=31536000, immutable' : 'public, max-age=3600';
}

type ProxyRequestLifetime = Pick<ClientRequest, 'destroyed' | 'destroy' | 'once' | 'removeListener'>;
type ProxyResponseLifetime = Pick<ServerResponse, 'writableEnded' | 'once' | 'removeListener'>;
type ProxySocketLifetime = Pick<Duplex, 'destroyed' | 'destroy' | 'once' | 'removeListener'>;

/**
 * Tie the upstream request to the browser response. Vite's proxy otherwise
 * leaves upstream sockets in CLOSE_WAIT when a browser aborts a slow history
 * response or the source backend is replaced during a rebuild.
 */
export function bindProxyRequestLifetime(
  proxyRequest: ProxyRequestLifetime,
  downstreamResponse: ProxyResponseLifetime
): () => void {
  let active = true;
  const cleanup = () => {
    if (!active) return;
    active = false;
    downstreamResponse.removeListener('close', downstreamClosed);
    downstreamResponse.removeListener('finish', cleanup);
    proxyRequest.removeListener('close', cleanup);
  };
  const downstreamClosed = () => {
    if (!downstreamResponse.writableEnded && !proxyRequest.destroyed) {
      proxyRequest.destroy(new Error('downstream response closed'));
    }
    cleanup();
  };
  downstreamResponse.once('close', downstreamClosed);
  downstreamResponse.once('finish', cleanup);
  proxyRequest.once('close', cleanup);
  return cleanup;
}

/** Close both halves of a proxied WebSocket when either peer disappears. */
export function bindProxySocketLifetime(left: ProxySocketLifetime, right: ProxySocketLifetime): () => void {
  let active = true;
  const closeLeft = () => {
    if (!left.destroyed) left.destroy();
    cleanup();
  };
  const closeRight = () => {
    if (!right.destroyed) right.destroy();
    cleanup();
  };
  const cleanup = () => {
    if (!active) return;
    active = false;
    left.removeListener('close', closeRight);
    left.removeListener('error', closeRight);
    right.removeListener('close', closeLeft);
    right.removeListener('error', closeLeft);
  };
  left.once('close', closeRight);
  left.once('error', closeRight);
  right.once('close', closeLeft);
  right.once('error', closeLeft);
  return cleanup;
}

function sourceBackendProxy(target: string, webSocket: boolean): ProxyOptions {
  return {
    target,
    changeOrigin: false,
    ws: webSocket,
    configure(proxy) {
      proxy.on('proxyReq', (proxyRequest, _request, response) => {
        bindProxyRequestLifetime(proxyRequest, response);
      });
      proxy.on('proxyReqWs', (proxyRequest, _request, downstreamSocket) => {
        const closePending = () => {
          if (!proxyRequest.destroyed) proxyRequest.destroy();
        };
        downstreamSocket.once('close', closePending);
        proxyRequest.once('upgrade', (_response, upstreamSocket) => {
          downstreamSocket.removeListener('close', closePending);
          bindProxySocketLifetime(downstreamSocket, upstreamSocket);
        });
        proxyRequest.once('close', () => downstreamSocket.removeListener('close', closePending));
      });
    },
  };
}

function positivePort(value: string | undefined, fallback: number): number {
  const parsed = Number.parseInt(value ?? '', 10);
  return Number.isInteger(parsed) && parsed > 0 && parsed <= 65_535 ? parsed : fallback;
}

function iconParkPlugin() {
  return {
    name: 'synon-ai-icon-park',
    enforce: 'pre' as const,
    transform(source: string, id: string): { code: string; map: null } | null {
      if (!id.endsWith('.tsx') || id.includes('node_modules') || !source.includes('@icon-park/react')) return null;
      const code = source.replace(
        /import\s+\{\s+([a-zA-Z, ]*)\s+\}\s+from\s+['"]@icon-park\/react['"](;?)/g,
        (statement, names: string) => {
          const components = names.split(',').map((name) => name.trim());
          const imported = statement.replace(names, components.map((name) => `${name} as _${name}`).join(', '));
          const wrapped = components.map((name) => `const ${name} = IconParkHOC(_${name})`).join(';\n');
          return `${imported};import IconParkHOC from '@renderer/components/IconParkHOC';\n${wrapped};`;
        }
      );
      return code === source ? null : { code, map: null };
    },
  };
}

function productionPreviewCachePlugin() {
  return {
    name: 'production-preview-cache',
    configurePreviewServer(server: {
      middlewares: {
        use: (handler: (request: { url?: string }, response: ServerResponse, next: () => void) => void) => void;
      };
    }) {
      server.middlewares.use((request, response, next) => {
        const cacheControl = productionPreviewCacheControl(request.url ?? '/');
        if (cacheControl) response.setHeader('Cache-Control', cacheControl);
        next();
      });
    },
  };
}

export function createRendererPlugins(): PluginOption[] {
  return [
    UnoCSS(unoConfig),
    iconParkPlugin(),
    react(),
    productionPreviewCachePlugin(),
    viteStaticCopy({
      targets: [
        { src: portableStaticCopySource(resolve(repoRoot, 'LICENSE')), dest: '.' },
        {
          src: portableStaticCopySource(resolve(repoRoot, 'node_modules/@rdkit/rdkit/dist/RDKit_minimal.js')),
          dest: 'rdkit',
        },
        {
          src: portableStaticCopySource(resolve(repoRoot, 'node_modules/@rdkit/rdkit/dist/RDKit_minimal.wasm')),
          dest: 'rdkit',
        },
      ],
    }),
  ];
}

export default defineConfig(({ mode }) => {
  const devPort = positivePort(process.env.SYNON_DEV_WEB_PORT, 5173);
  const backendTarget = process.env.SYNON_DEV_BACKEND_URL?.trim();
  const proxy = backendTarget
    ? Object.fromEntries(
        ['/api', '/health', '/login', '/logout', '/synon-link', '/v1', '/ws'].map((path) => [
          path,
          sourceBackendProxy(backendTarget, path === '/api' || path === '/ws' || path === '/synon-link'),
        ])
      )
    : undefined;

  return {
    root: rendererRoot,
    base: './',
    publicDir: resolve(repoRoot, 'public'),
    esbuild: { legalComments: 'none' },
    server: {
      host: process.env.SYNON_DEV_WEB_HOST?.trim() || '127.0.0.1',
      port: devPort,
      strictPort: true,
      hmr: { host: 'localhost', port: devPort },
      proxy,
    },
    preview: {
      host: process.env.SYNON_DEV_WEB_HOST?.trim() || '127.0.0.1',
      port: devPort,
      strictPort: true,
      proxy,
    },
    resolve: {
      alias: {
        '@': resolve(repoRoot, 'packages/desktop/src'),
        '@common': resolve(repoRoot, 'packages/desktop/src/common'),
        '@renderer': rendererRoot,
        streamdown: resolve(repoRoot, 'node_modules/streamdown/dist/index.js'),
      },
      extensions: ['.ts', '.tsx', '.js', '.jsx', '.css'],
      dedupe: [
        'react',
        'react-dom',
        'react-router',
        '@codemirror/state',
        '@codemirror/view',
        '@codemirror/language',
        '@lezer/highlight',
      ],
    },
    plugins: createRendererPlugins(),
    build: {
      target: 'es2022',
      outDir: resolve(repoRoot, 'out/renderer'),
      emptyOutDir: true,
      sourcemap: mode === 'development',
      minify: mode !== 'development',
      reportCompressedSize: false,
      chunkSizeWarningLimit: 1500,
      cssCodeSplit: true,
      rollupOptions: {
        input: resolve(rendererRoot, 'index.html'),
        output: {
          manualChunks: rendererManualChunk,
          // Let Rollup keep Mol*'s private dependency graph with the manual
          // chunk. Restricting this to only explicit modules can strand shared
          // graph nodes in the caller chunk and create a runtime import cycle.
          onlyExplicitManualChunks: rendererOnlyExplicitManualChunks,
        },
        onwarn(warning, warn) {
          if (warning.code !== 'EVAL') warn(warning);
        },
      },
    },
    define: {
      'process.env.NODE_ENV': JSON.stringify(mode),
      'process.env.env': JSON.stringify(process.env.env),
      'process.env.SYNON_AI_MULTI_INSTANCE': JSON.stringify(''),
      'process.env.SENTRY_DSN': JSON.stringify(''),
      __APP_VERSION__: JSON.stringify(rootPackage.version),
      global: 'globalThis',
    },
  };
});
