import React, { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import PreviewLoadingState from '@/renderer/components/media/PreviewLoadingState';
import {
  createSynonBiomedMcpAppResourceTicket,
  pinSynonBiomedMcpAppArtifact,
  pollSynonBiomedMcpAppRequest,
  registerSynonBiomedMcpApp,
  resolveSynonBiomedMcpAppRequest,
  serializeSynonBiomedMcpAppMountArguments,
  unregisterSynonBiomedMcpApp,
  type SynonBiomedMcpAppRequest,
  type SynonBiomedMcpAppResourceTicket,
  type SynonBiomedMcpAppToolResult,
} from '@/renderer/services/mcp/synonBiomedMcpApps';
import {
  mcpAppInitializeResult,
  normalizeMCPAppModelContext,
  normalizeMCPAppToolResult,
  normalizeMCPAppTools,
  parseMCPAppRPCMessage,
  type MCPAppRPCMessage,
} from './mcpAppHostProtocol';
import './SynonBiomedMcpAppViewer.css';
import type { SynonBiomedMcpAppSaveContract } from './mcpAppSaveContracts';
export { ketcherMcpAppSaveContract } from './mcpAppSaveContracts';

const initializeTimeoutMs = 20_000;
const maxMcpAppSaveBytes = 2 * 1024 * 1024;
const deniedPermissions =
  "camera 'none'; microphone 'none'; geolocation 'none'; clipboard-read 'none'; clipboard-write 'none'";

type SynonBiomedMcpAppViewerProps = {
  serverId: string;
  serverName: string;
  openTool: string;
  resourceUri: string;
  docsUrl?: string | null;
  rootFrameId?: string;
  frameId?: string;
  artifactId?: string;
  initialArguments: Record<string, unknown>;
  saveContract?: SynonBiomedMcpAppSaveContract;
};

type PendingCall =
  | { kind: 'tools-list' }
  | {
      kind: 'broker';
      registrationId: string;
      request: SynonBiomedMcpAppRequest;
    }
  | {
      kind: 'save';
      resolve: (result: SynonBiomedMcpAppToolResult) => void;
      reject: (reason: Error) => void;
    };

const SynonBiomedMcpAppViewer: React.FC<SynonBiomedMcpAppViewerProps> = ({
  serverId,
  serverName,
  openTool,
  resourceUri,
  docsUrl,
  rootFrameId,
  frameId,
  artifactId,
  initialArguments,
  saveContract,
}) => {
  const { t } = useTranslation();
  const iframeRef = useRef<HTMLIFrameElement>(null);
  const generationRef = useRef(0);
  const initializedRef = useRef(false);
  const saveActionRef = useRef<() => Promise<void>>(async () => undefined);
  const [mount, setMount] = useState<SynonBiomedMcpAppResourceTicket | null>(null);
  const [state, setState] = useState<'loading' | 'ready' | 'saving' | 'saved' | 'error' | 'input-too-large'>('loading');
  const [argumentsKey, argumentsError] = useMemo<readonly [string | null, 'error' | 'input-too-large' | null]>(() => {
    try {
      return [serializeSynonBiomedMcpAppMountArguments(initialArguments), null] as const;
    } catch (error) {
      return [
        null,
        error instanceof Error && error.message === 'MCP_APP_INPUT_TOO_LARGE' ? 'input-too-large' : 'error',
      ] as const;
    }
  }, [initialArguments]);
  const safeDocsUrl = useMemo(() => {
    if (!docsUrl) return null;
    try {
      const value = new URL(docsUrl);
      return value.protocol === 'https:' || value.protocol === 'http:' ? value.href : null;
    } catch {
      return null;
    }
  }, [docsUrl]);
  const registrationArtifactId = useMemo(
    () => artifactId?.trim() || createMCPAppMountID(),
    [artifactId, argumentsKey, resourceUri, serverId]
  );

  useEffect(() => {
    const generation = ++generationRef.current;
    initializedRef.current = false;
    setMount(null);
    setState('loading');
    let active = true;
    if (!argumentsKey) {
      setState(argumentsError ?? 'error');
      return () => {
        active = false;
        generationRef.current += 1;
        initializedRef.current = false;
      };
    }
    void createSynonBiomedMcpAppResourceTicket(serverId, resourceUri, initialArguments)
      .then((value) => {
        if (!active || generationRef.current !== generation) return;
        setMount(value);
      })
      .catch(() => {
        if (!active || generationRef.current !== generation) return;
        setState('error');
      });
    return () => {
      active = false;
      generationRef.current += 1;
      initializedRef.current = false;
    };
    // argumentsKey is the bounded semantic identity for initialArguments.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [argumentsError, argumentsKey, resourceUri, serverId]);

  useEffect(() => {
    if (!mount) return;
    const generation = generationRef.current;
    const iframe = iframeRef.current;
    if (!iframe) return;
    let active = true;
    let registrationId: string | null = null;
    let nextRequestId = 1;
    let modelContext: Record<string, unknown> = {};
    const pending = new Map<string | number, PendingCall>();
    const pollController = new AbortController();
    const isCurrent = () => active && generationRef.current === generation;
    const post = (message: MCPAppRPCMessage) => {
      if (!isCurrent()) return;
      iframe.contentWindow?.postMessage(message, mount.origin);
    };
    const postRequest = (method: string, params: unknown, call: PendingCall): string => {
      const id = `synon-host-${generation}-${nextRequestId++}`;
      pending.set(id, call);
      post({ jsonrpc: '2.0', id, method, params });
      return id;
    };
    const callAppTool = (name: string, argumentsValue: Record<string, unknown>) =>
      new Promise<SynonBiomedMcpAppToolResult>((resolve, reject) => {
        postRequest('tools/call', { name, arguments: argumentsValue }, { kind: 'save', resolve, reject });
      });
    const fail = () => {
      if (isCurrent()) setState('error');
    };
    const startPolling = () => {
      if (!registrationId) return;
      const registered = registrationId;
      void (async () => {
        try {
          while (!pollController.signal.aborted && isCurrent()) {
            // The backend holds one bounded 25-second long poll. A new request
            // is issued only after the preceding response completes.
            // oxlint-disable-next-line no-await-in-loop
            const request = await pollSynonBiomedMcpAppRequest(registered, pollController.signal);
            if (!request || !isCurrent()) continue;
            postRequest(
              'tools/call',
              { name: request.tool, arguments: request.arguments },
              { kind: 'broker', registrationId: registered, request }
            );
          }
        } catch {
          if (!pollController.signal.aborted) fail();
        }
      })();
    };
    const handleResponse = async (message: MCPAppRPCMessage) => {
      if (message.id === undefined) return;
      const call = pending.get(message.id);
      if (!call) return;
      pending.delete(message.id);
      if (call.kind === 'tools-list') {
        if (message.error) throw new Error('MCP_APP_TOOLS_LIST_FAILED');
        const tools = normalizeMCPAppTools(message.result);
        if (rootFrameId) {
          const registration = await registerSynonBiomedMcpApp({
            rootFrameId,
            frameId,
            server: serverId,
            artifactId: registrationArtifactId,
            tools,
          });
          if (!isCurrent()) {
            await unregisterSynonBiomedMcpApp(registration.registrationId).catch((): void => undefined);
            return;
          }
          registrationId = registration.registrationId;
          startPolling();
        }
        if (isCurrent()) setState('ready');
        return;
      }
      if (call.kind === 'broker') {
        const result = message.error
          ? {
              content: [
                {
                  type: 'text',
                  text: boundedErrorMessage(message.error.message),
                },
              ],
              isError: true,
            }
          : normalizeMCPAppToolResult(message.result);
        await resolveSynonBiomedMcpAppRequest(call.registrationId, call.request.requestId, result);
        return;
      }
      if (message.error) {
        call.reject(new Error('MCP_APP_SAVE_TOOL_FAILED'));
        return;
      }
      call.resolve(normalizeMCPAppToolResult(message.result));
    };
    const handleRequest = (message: MCPAppRPCMessage) => {
      if (!message.method) return;
      if (message.method === 'ui/initialize') {
        const params = asRecord(message.params);
        try {
          post({
            jsonrpc: '2.0',
            id: message.id,
            result: mcpAppInitializeResult(
              params?.protocolVersion,
              document.documentElement.classList.contains('dark')
            ),
          });
        } catch {
          post({
            jsonrpc: '2.0',
            id: message.id,
            error: { code: -32602, message: 'MCP_APP_PROTOCOL_UNSUPPORTED' },
          });
        }
        return;
      }
      if (message.method === 'ping') {
        post({ jsonrpc: '2.0', id: message.id, result: {} });
        return;
      }
      if (message.method === 'ui/request-display-mode') {
        post({ jsonrpc: '2.0', id: message.id, result: { mode: 'inline' } });
        return;
      }
      if (message.method === 'ui/update-model-context') {
        const next = normalizeMCPAppModelContext(message.params);
        if (next) modelContext = next;
        if (message.id !== undefined) post({ jsonrpc: '2.0', id: message.id, result: {} });
        return;
      }
      if (message.id !== undefined) {
        post({
          jsonrpc: '2.0',
          id: message.id,
          error: { code: -32601, message: 'MCP_APP_METHOD_NOT_SUPPORTED' },
        });
      }
    };
    const handleMessage = (event: MessageEvent) => {
      if (!isCurrent() || event.source !== iframe.contentWindow || event.origin !== mount.origin) return;
      const message = parseMCPAppRPCMessage(event.data);
      if (!message) return;
      if (message.method === 'ui/notifications/initialized') {
        if (initializedRef.current) return;
        initializedRef.current = true;
        post({
          jsonrpc: '2.0',
          method: 'ui/notifications/tool-input',
          params: { arguments: mount.toolInput },
        });
        post({
          jsonrpc: '2.0',
          method: 'ui/notifications/tool-result',
          params: mount.toolResult,
        });
        postRequest('tools/list', {}, { kind: 'tools-list' });
        return;
      }
      const operation = message.method ? Promise.resolve(handleRequest(message)) : handleResponse(message);
      void operation.catch(fail);
    };
    saveActionRef.current = async () => {
      if (!rootFrameId || !saveContract || !initializedRef.current || !isCurrent()) return;
      setState('saving');
      try {
        const result = await callAppTool(saveContract.appTool, {});
        const structured = asRecord(result.structuredContent);
        const content = structured?.[saveContract.resultField];
        if (typeof content !== 'string' || new TextEncoder().encode(content).byteLength > maxMcpAppSaveBytes) {
          throw new Error('MCP_APP_SAVE_RESULT_INVALID');
        }
        const filename = mcpAppSaveFilename(initialArguments.filename, saveContract);
        const idempotencyKey = await mcpAppSaveIdempotencyKey(rootFrameId, artifactId ?? '', filename, content);
        await pinSynonBiomedMcpAppArtifact({
          rootFrameId,
          frameId,
          artifactId,
          filename,
          contentType: saveContract.mimeType,
          content,
          contentEncoding: 'utf8',
          agentName: serverName,
          tool: saveContract.appTool,
          arguments: modelContext,
          idempotencyKey,
        });
        if (isCurrent()) setState('saved');
      } catch {
        fail();
      }
    };
    const timeout = window.setTimeout(() => {
      if (isCurrent() && !initializedRef.current) fail();
    }, initializeTimeoutMs);
    window.addEventListener('message', handleMessage);
    // Install the exact source/origin receiver before navigating. A bundled
    // App may initialize during its first script turn.
    iframe.src = mount.url;
    return () => {
      active = false;
      saveActionRef.current = async () => undefined;
      window.clearTimeout(timeout);
      window.removeEventListener('message', handleMessage);
      pollController.abort();
      for (const call of pending.values()) {
        if (call.kind === 'save') call.reject(new Error('MCP_APP_UNMOUNTED'));
      }
      pending.clear();
      if (registrationId) void unregisterSynonBiomedMcpApp(registrationId).catch((): void => undefined);
      if (iframeRef.current === iframe) iframe.src = 'about:blank';
    };
  }, [
    artifactId,
    frameId,
    initialArguments,
    mount,
    registrationArtifactId,
    rootFrameId,
    saveContract,
    serverId,
    serverName,
  ]);

  return (
    <div className='synon-mcp-app-viewer' data-state={state} data-testid='mcp-app-viewer'>
      <div className='synon-mcp-app-viewer__header'>
        <span className='synon-mcp-app-viewer__status' aria-hidden='true' />
        <strong>{serverName}</strong>
        <span aria-hidden='true'>›</span>
        <span>{openTool}</span>
        {state === 'saved' ? (
          <span className='synon-mcp-app-viewer__saved'>{t('preview.scientific.mcpApp.saved')}</span>
        ) : null}
        <span className='synon-mcp-app-viewer__actions'>
          {rootFrameId && saveContract ? (
            <button
              type='button'
              className='synon-mcp-app-viewer__save'
              disabled={state !== 'ready' && state !== 'saved'}
              onClick={() => void saveActionRef.current()}
            >
              {state === 'saving' ? t('preview.scientific.mcpApp.saving') : t('preview.scientific.mcpApp.save')}
            </button>
          ) : null}
          {safeDocsUrl ? (
            <a href={safeDocsUrl} target='_blank' rel='noopener noreferrer' className='synon-mcp-app-viewer__help'>
              {t('preview.scientific.mcpApp.help')}
            </a>
          ) : null}
        </span>
      </div>
      <div className='synon-mcp-app-viewer__body'>
        {state === 'loading' ? <PreviewLoadingState label={t('preview.scientific.mcpApp.loading')} /> : null}
        {state === 'error' ? (
          <div className='synon-mcp-app-viewer__error' role='alert'>
            {t('preview.scientific.mcpApp.unavailable')}
          </div>
        ) : null}
        {state === 'input-too-large' ? (
          <div className='synon-mcp-app-viewer__error' role='alert'>
            {t('preview.scientific.mcpApp.inputTooLarge')}
          </div>
        ) : null}
        {mount ? (
          <iframe
            ref={iframeRef}
            className='synon-mcp-app-viewer__frame'
            title={`${serverName} — ${openTool}`}
            sandbox='allow-scripts allow-same-origin'
            allow={deniedPermissions}
            referrerPolicy='no-referrer'
          />
        ) : null}
      </div>
    </div>
  );
};

function asRecord(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function boundedErrorMessage(value: string): string {
  const message = value.trim();
  return message ? message.slice(0, 512) : 'MCP App tool failed';
}

function createMCPAppMountID(): string {
  const value = globalThis.crypto?.randomUUID?.();
  return value ? `mcp-app-mount-${value}` : `mcp-app-mount-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

function mcpAppSaveFilename(value: unknown, contract: SynonBiomedMcpAppSaveContract): string {
  const supplied = typeof value === 'string' ? value.trim() : '';
  const base = supplied || contract.filenameStem;
  return base.toLowerCase().endsWith(contract.extension.toLowerCase()) ? base : `${base}${contract.extension}`;
}

async function mcpAppSaveIdempotencyKey(
  rootFrameId: string,
  artifactId: string,
  filename: string,
  content: string
): Promise<string> {
  const source = new TextEncoder().encode(
    ['synon-mcp-app-save-v1', rootFrameId, artifactId, filename, content].join('\0')
  );
  if (globalThis.crypto?.subtle) {
    const digest = new Uint8Array(await globalThis.crypto.subtle.digest('SHA-256', source));
    return `mcp-app-save-${Array.from(digest, (value) => value.toString(16).padStart(2, '0')).join('')}`;
  }
  let hash = 2166136261;
  for (const value of source) hash = Math.imul(hash ^ value, 16777619);
  return `mcp-app-save-${(hash >>> 0).toString(16).padStart(8, '0')}`;
}

export default SynonBiomedMcpAppViewer;
