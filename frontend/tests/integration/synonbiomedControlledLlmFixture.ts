import { createServer, type Server } from 'node:http';
import { once } from 'node:events';
import { createSynonBiomedTestFetch } from './synonbiomedTestAuth';

type Profile = {
  id: string;
  name: string;
  provider: string;
  baseUrl: string;
  model: string;
  temperature?: number;
  maxTokens?: number;
  hasApiKey: boolean;
};

type Snapshot = {
  activeProfileId?: string;
  profiles: Profile[];
};

export type ControlledLlmObservation = {
  text: string;
  sentAtUnixMs: number;
};

export type ControlledLlmFixture = {
  profile: Profile;
  publicProgressObservations: ControlledLlmObservation[];
  dispose: () => Promise<void>;
};

export type ControlledLlmFixtureOptions = {
  honorRequiredAskUser?: boolean;
  publicProgressChunks?: string[];
  publicProgressChunkDelayMs?: number;
  invalidAskUserAttempts?: number;
};

export async function createControlledLlmFixture(
  gatewayBaseUrl: string,
  options: ControlledLlmFixtureOptions = {}
): Promise<ControlledLlmFixture> {
  validateControlledFixtureOptions(options);
  const publicProgressObservations: ControlledLlmObservation[] = [];
  const server = createOpenAiFixtureServer(options, publicProgressObservations);
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  const address = server.address();
  if (!address || typeof address === 'string') {
    await closeServer(server);
    throw new Error('OpenAI-compatible fixture did not bind a TCP port');
  }

  const fetchImpl = await createSynonBiomedTestFetch(gatewayBaseUrl);
  const initial = await requestJson<Snapshot>(fetchImpl, gatewayBaseUrl, '/api/llm/providers');
  const previous = initial.profiles.find((profile) => profile.id === initial.activeProfileId);
  let profile: Profile;
  try {
    const created = await requestJson<{ profile: Profile }>(fetchImpl, gatewayBaseUrl, '/api/llm/providers', {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({
        name: `controlled-loopback-fixture-${Date.now()}`,
        provider: 'custom-openai',
        baseUrl: `http://127.0.0.1:${address.port}/v1`,
        model: 'synon-controlled-fixture',
        apiKey: 'local-integration-key',
        temperature: 0,
        maxTokens: 128,
      }),
    });
    profile = created.profile;
  } catch (error) {
    await closeServer(server);
    throw error;
  }

  return {
    profile,
    publicProgressObservations,
    dispose: async () => {
      try {
        const response = await fetchImpl(`${gatewayBaseUrl}/api/llm/providers/${encodeURIComponent(profile.id)}`, {
          method: 'DELETE',
        });
        if (!response.ok && response.status !== 404) {
          throw new Error(`delete LLM fixture failed: ${response.status} ${await response.text()}`);
        }
        if (previous) {
          await requestJson(fetchImpl, gatewayBaseUrl, '/api/llm/providers', {
            method: 'POST',
            headers: { 'content-type': 'application/json' },
            body: JSON.stringify({
              id: previous.id,
              name: previous.name,
              provider: previous.provider,
              baseUrl: previous.baseUrl,
              model: previous.model,
              temperature: previous.temperature,
              maxTokens: previous.maxTokens,
              copyApiKeyFrom: previous.id,
            }),
          });
        }
      } finally {
        await closeServer(server);
      }
    },
  };
}

function createOpenAiFixtureServer(
  options: ControlledLlmFixtureOptions,
  publicProgressObservations: ControlledLlmObservation[]
): Server {
  let askUserCompleted = false;
  let askUserAttempts = 0;
  return createServer(async (request, response) => {
    const body = await readBody(request);
    if (request.method === 'GET' && request.url === '/v1/models') {
      sendJson(response, {
        object: 'list',
        data: [{ id: 'synon-controlled-fixture', object: 'model', owned_by: 'synon-integration' }],
      });
      return;
    }
    if (request.method === 'POST' && request.url === '/v1/chat/completions') {
      const input = body
        ? (JSON.parse(body) as {
            stream?: boolean;
            tool_choice?: string | { type?: string; function?: { name?: string } };
            tools?: Array<{ type?: string; function?: { name?: string } }>;
          })
        : {};
      const requiredTool =
        typeof input.tool_choice === 'object' && input.tool_choice?.type === 'function'
          ? input.tool_choice.function?.name
          : undefined;
      const askUserAvailable = input.tools?.some(
        (tool) => tool.type === 'function' && tool.function?.name === 'ask_user'
      );
      const emitAskUser =
        options.honorRequiredAskUser === true &&
        !askUserCompleted &&
        (requiredTool === 'ask_user' || askUserAvailable === true);
      const invalidAskUser = emitAskUser && askUserAttempts < (options.invalidAskUserAttempts ?? 0);
      if (emitAskUser) {
        askUserAttempts += 1;
        if (!invalidAskUser) askUserCompleted = true;
      }
      if (input.stream) {
        response.writeHead(200, {
          'content-type': 'text/event-stream',
          'cache-control': 'no-cache',
          connection: 'keep-alive',
        });
        if (emitAskUser) {
          for (const [index, chunk] of (options.publicProgressChunks ?? []).entries()) {
            publicProgressObservations.push({ text: chunk, sentAtUnixMs: Date.now() });
            response.write(
              `data: ${JSON.stringify({
                id: 'chatcmpl-synon-controlled-intake',
                object: 'chat.completion.chunk',
                created: 1,
                model: 'synon-controlled-fixture',
                choices: [
                  {
                    index: 0,
                    delta: {
                      role: 'assistant',
                      content: encodeControlledPublicProgress(`controlled-progress-${index + 1}`, chunk),
                    },
                    finish_reason: null,
                  },
                ],
              })}\n\n`
            );
            if (options.publicProgressChunkDelayMs) {
              await new Promise((resolve) => setTimeout(resolve, options.publicProgressChunkDelayMs));
            }
          }
          response.write(
            `data: ${JSON.stringify({
              id: 'chatcmpl-synon-controlled-intake',
              object: 'chat.completion.chunk',
              created: 1,
              model: 'synon-controlled-fixture',
              choices: [
                {
                  index: 0,
                  delta: {
                    role: 'assistant',
                    tool_calls: [
                      {
                        index: 0,
                        id: 'ask-controlled-intake-1',
                        type: 'function',
                        function: {
                          name: 'ask_user',
                          arguments: controlledAskUserArguments(invalidAskUser),
                        },
                      },
                    ],
                  },
                  finish_reason: null,
                },
              ],
            })}\n\n`
          );
          response.write(
            `data: ${JSON.stringify({
              id: 'chatcmpl-synon-controlled-intake',
              object: 'chat.completion.chunk',
              created: 1,
              model: 'synon-controlled-fixture',
              choices: [{ index: 0, delta: {}, finish_reason: 'tool_calls' }],
            })}\n\n`
          );
          response.end('data: [DONE]\n\n');
          return;
        }
        response.write(
          `data: ${JSON.stringify({
            id: 'chatcmpl-synon-controlled',
            object: 'chat.completion.chunk',
            created: 1,
            model: 'synon-controlled-fixture',
            choices: [{ index: 0, delta: { content: 'Provider reachable.' }, finish_reason: null }],
          })}\n\n`
        );
        response.write(
          `data: ${JSON.stringify({
            id: 'chatcmpl-synon-controlled',
            object: 'chat.completion.chunk',
            created: 1,
            model: 'synon-controlled-fixture',
            choices: [{ index: 0, delta: {}, finish_reason: 'stop' }],
          })}\n\n`
        );
        response.end('data: [DONE]\n\n');
        return;
      }
      if (emitAskUser) {
        sendJson(response, {
          id: 'chatcmpl-synon-controlled-intake',
          object: 'chat.completion',
          created: 1,
          model: 'synon-controlled-fixture',
          choices: [
            {
              index: 0,
              message: {
                role: 'assistant',
                content: '',
                tool_calls: [
                  {
                    id: 'ask-controlled-intake-1',
                    type: 'function',
                    function: {
                      name: 'ask_user',
                      arguments: controlledAskUserArguments(invalidAskUser),
                    },
                  },
                ],
              },
              finish_reason: 'tool_calls',
            },
          ],
          usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
        });
        return;
      }
      sendJson(response, {
        id: 'chatcmpl-synon-controlled',
        object: 'chat.completion',
        created: 1,
        model: 'synon-controlled-fixture',
        choices: [
          {
            index: 0,
            message: { role: 'assistant', content: 'Provider reachable.' },
            finish_reason: 'stop',
          },
        ],
        usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
      });
      return;
    }
    sendJson(response, { error: { message: 'Fixture route not found' } }, 404);
  });
}

function encodeControlledPublicProgress(id: string, text: string): string {
  return `<|PublicProgressBegin|>${JSON.stringify({ version: 1, id, text })}<|PublicProgressEnd|>`;
}

function controlledAskUserArguments(invalid: boolean): string {
  if (invalid) {
    return JSON.stringify({
      question: '这次任务应按哪个验收边界继续？',
      header: '验收范围',
      options: [
        { label: '完整链路（推荐）', description: '缺少所需的证据字段。' },
        { label: '仅流式回答', description: '缺少所需的证据字段。' },
      ],
    });
  }
  return JSON.stringify({
    question: '这次任务应按哪个验收边界继续？',
    header: '验收范围',
    options: [
      {
        label: '完整链路（推荐）',
        description: '验证提问、恢复、流式回答和持久化。',
        pros: '覆盖用户可见的完整协议路径。',
        cons: '本地可控 provider 不证明远程模型质量。',
        readiness: '本选项不声明远程模型或生产环境已就绪。',
        readiness_status: 'unverified',
        decision_evidence: ['user-input:current-task'],
        readiness_evidence: [],
        selection_basis: 'user_objective',
        expected_outcome: '验证提问、恢复、公开流与最终持久化。',
        selection_rationale: '推荐，因为它覆盖本用例声明的完整链路。',
        recommended: true,
      },
      {
        label: '仅流式回答',
        description: '只验证模型回答与持久化。',
        pros: '路径更短且执行更快。',
        cons: '不会覆盖待答工具与恢复链路。',
        readiness: '本选项不声明远程模型或生产环境已就绪。',
        readiness_status: 'unverified',
        decision_evidence: ['user-input:current-task'],
        readiness_evidence: [],
        selection_basis: 'user_objective',
        expected_outcome: '仅验证公开流与最终持久化。',
        selection_rationale: '适合只需要最小流式链路时选择。',
        recommended: false,
      },
    ],
  });
}

function validateControlledFixtureOptions(options: ControlledLlmFixtureOptions): void {
  const chunks = options.publicProgressChunks ?? [];
  if (chunks.length > 64 || chunks.some((chunk) => !chunk.trim() || Buffer.byteLength(chunk, 'utf8') > 1024)) {
    throw new Error('Controlled LLM fixture public progress must contain at most 64 non-empty 1 KiB chunks');
  }
  const delay = options.publicProgressChunkDelayMs ?? 0;
  if (!Number.isInteger(delay) || delay < 0 || delay > 1_000) {
    throw new Error('Controlled LLM fixture progress delay must be an integer from 0 to 1000 ms');
  }
  const invalidAttempts = options.invalidAskUserAttempts ?? 0;
  if (!Number.isInteger(invalidAttempts) || invalidAttempts < 0 || invalidAttempts > 2) {
    throw new Error('Controlled LLM fixture invalid AskUser attempts must be an integer from 0 to 2');
  }
}

async function requestJson<T>(
  fetchImpl: typeof fetch,
  gatewayBaseUrl: string,
  path: string,
  init?: RequestInit
): Promise<T> {
  const response = await fetchImpl(`${gatewayBaseUrl}${path}`, init);
  if (!response.ok) throw new Error(`${init?.method ?? 'GET'} ${path}: ${response.status} ${await response.text()}`);
  return response.json() as Promise<T>;
}

async function readBody(request: NodeJS.ReadableStream): Promise<string> {
  const chunks: Buffer[] = [];
  for await (const chunk of request) chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk));
  return Buffer.concat(chunks).toString('utf8');
}

function sendJson(response: import('node:http').ServerResponse, body: unknown, status = 200): void {
  response.writeHead(status, { 'content-type': 'application/json' });
  response.end(JSON.stringify(body));
}

async function closeServer(server: Server): Promise<void> {
  if (!server.listening) return;

  const closed = once(server, 'close');
  server.close();
  server.closeAllConnections();
  await closed;
}
