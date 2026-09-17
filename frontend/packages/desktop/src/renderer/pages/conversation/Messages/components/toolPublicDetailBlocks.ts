import type { NormalizedToolCall } from '@/common/chat/normalizeToolCall';
import { toolPublicDetailText } from '@/renderer/services/i18n/toolPublicDetailLocale';

export type ToolPublicDetailBlock = {
  label: string;
  summary: string;
  content: string;
  language?: string;
  variant: 'code' | 'markdown' | 'text';
};

const PRODUCT_INTERNAL_FRAGMENT =
  /\b(?:harness|message projector|prompt constraint|retry controller|runtime policy|system prompt|tool call protocol)\b|提示词(?:约束)?|系统提示|运行时策略|重试控制|投影链路|内部事件流/giu;
const SECRET_ASSIGNMENT =
  /((?:api[_-]?key|auth(?:orization)?|bearer|cookie|credential|password|private[_-]?key|secret|sig(?:nature)?|token)\s*[:=]\s*)([^\s,;]+)/giu;
const SECRET_TOKEN = /\b(?:sk-[A-Za-z0-9_-]{8,}|gh[pousr]_[A-Za-z0-9_]{12,})\b/gu;

export function buildToolPublicDetailBlocks(
  tool: NormalizedToolCall,
  input: Record<string, unknown> | null,
  output: Record<string, unknown> | null,
  chinese: boolean
): { inputBlocks: ToolPublicDetailBlock[]; outputBlocks: ToolPublicDetailBlock[] } {
  const inputBlocks: ToolPublicDetailBlock[] = [];
  const outputBlocks: ToolPublicDetailBlock[] = [];

  if (input) {
    appendInputBlock(
      inputBlocks,
      input.code,
      toolPublicDetailText(chinese, 'analysisCode'),
      inferCodeLanguage(tool.name),
      chinese
    );
    appendInputBlock(
      inputBlocks,
      input.command,
      toolPublicDetailText(chinese, 'command'),
      inferCommandLanguage(tool.name),
      chinese
    );
    if (tool.name.trim().toLowerCase() === 'compute_details') {
      appendInputBlock(
        inputBlocks,
        input.old_text,
        toolPublicDetailText(chinese, 'previousComputeNotes'),
        'markdown',
        chinese
      );
      appendInputBlock(inputBlocks, input.text, toolPublicDetailText(chinese, 'computeNotes'), 'markdown', chinese);
    }
    const fileLanguage = markdownFileLanguage(input.file_path);
    appendInputBlock(
      inputBlocks,
      input.old_string,
      toolPublicDetailText(chinese, 'previousContent'),
      fileLanguage,
      chinese
    );
    appendInputBlock(
      inputBlocks,
      input.new_string,
      toolPublicDetailText(chinese, 'updatedContent'),
      fileLanguage,
      chinese
    );
    appendInputBlock(
      inputBlocks,
      input.content,
      toolPublicDetailText(chinese, 'writtenContent'),
      fileLanguage,
      chinese
    );
  }

  if (output) collectOutputBlocks(outputBlocks, output, tool, chinese);
  if (!output && tool.output) {
    appendOutputBlock(
      outputBlocks,
      tool.output,
      isSkillTool(tool.name)
        ? toolPublicDetailText(chinese, 'analysisGuide')
        : toolPublicDetailText(chinese, 'runOutput'),
      isSkillTool(tool.name) ? 'markdown' : inferOutputLanguage(tool.name),
      chinese
    );
  }

  return {
    inputBlocks: dedupeBlocks(inputBlocks),
    outputBlocks: dedupeBlocks(outputBlocks),
  };
}

export function formatPublicTaskPath(value: string): string | null {
  const normalized = sanitizePublicTaskText(value);
  if (!normalized) return null;
  const taskRelative = normalized
    .replace(
      /(?:\/home\/[^\s/]+\/\.local\/state\/synon-biomed[^\s]*\/data-workspaces\/[A-Za-z0-9-]+\/tasks\/[A-Za-z0-9-]+\/)/giu,
      './'
    )
    .replace(/(?:[A-Za-z]:\\[^\s]+\\data-workspaces\\[A-Za-z0-9-]+\\tasks\\[A-Za-z0-9-]+\\)/giu, '.\\');
  if (taskRelative.startsWith('./') || taskRelative.startsWith('.\\')) return taskRelative;
  if (taskRelative.startsWith('/') || /^[A-Za-z]:\\/u.test(taskRelative) || taskRelative.startsWith('\\\\')) {
    return lastPathSegment(taskRelative);
  }
  return taskRelative;
}

export function sanitizePublicTaskText(value: string): string | null {
  const lines = value.replace(/\r/gu, '').split('\n');
  const publicLines: string[] = [];
  for (const rawLine of lines) {
    const redacted = rawLine
      .replace(PRODUCT_INTERNAL_FRAGMENT, '[internal detail redacted]')
      .replace(SECRET_ASSIGNMENT, '$1[redacted]')
      .replace(SECRET_TOKEN, '[redacted]')
      .replace(/\bmem_[A-Za-z0-9_-]+\b/gu, '[memory record]')
      .replace(/\b(?:project|artifact|frame):[A-Za-z0-9_-]{8,}\b/gu, (scope) => scope.split(':')[0])
      .replace(
        /\.synon[\\/]+runtime[\\/]+skills[\\/]+[^\\/\s"']+[\\/]+[^\\/\s"']+[\\/]+scripts[\\/]+([^\\/\s"']+)/giu,
        'analysis-tools/$1'
      )
      .replace(
        /\/home\/[^\s/]+\/\.local\/state\/synon-biomed[^\s]*\/data-workspaces\/[A-Za-z0-9-]+\/tasks\/[A-Za-z0-9-]+\//giu,
        './'
      )
      .replace(/\/workspace(?:\/[^\s"'<>]+)?/giu, (path) =>
        path === '/workspace' ? '.' : `./${path.slice('/workspace/'.length)}`
      )
      .replace(/\/(?:home|tmp)\/[^\s"'<>]+/giu, (path) => publicHostPathBasename(path))
      .replace(/[A-Za-z]:\\[^\s"'<>]+/gu, (path) => publicHostPathBasename(path));
    publicLines.push(redacted);
  }
  const sanitized = publicLines.join('\n').trim();
  if (!sanitized) return null;
  return sanitized;
}

function publicHostPathBasename(path: string): string {
  return lastPathSegment(path) ?? '[task file]';
}

function lastPathSegment(path: string): string | null {
  const segments = path.split(/[\\/]/u);
  for (let index = segments.length - 1; index >= 0; index -= 1) {
    if (segments[index]) return segments[index];
  }
  return null;
}

function appendInputBlock(
  blocks: ToolPublicDetailBlock[],
  value: unknown,
  label: string,
  language: string,
  chinese: boolean
) {
  if (typeof value !== 'string') return;
  const content = sanitizePublicTaskText(value);
  if (!content) return;
  blocks.push({
    label,
    summary: blockSummary(content, chinese),
    content,
    language,
    variant: language === 'markdown' ? 'markdown' : language === 'text' ? 'text' : 'code',
  });
}

function appendOutputBlock(
  blocks: ToolPublicDetailBlock[],
  value: unknown,
  label: string,
  language: string,
  chinese: boolean
) {
  if (typeof value !== 'string') return;
  const content = sanitizePublicTaskText(value);
  if (!content) return;
  blocks.push({
    label,
    summary: blockSummary(content, chinese),
    content,
    language,
    variant: language === 'markdown' ? 'markdown' : language === 'text' ? 'text' : 'code',
  });
}

function collectOutputBlocks(
  blocks: ToolPublicDetailBlock[],
  value: Record<string, unknown>,
  tool: NormalizedToolCall,
  chinese: boolean,
  depth = 0
) {
  if (depth > 4) return;
  appendOutputBlock(blocks, value.stdout, 'STDOUT', inferOutputLanguage(tool.name), chinese);
  appendOutputBlock(blocks, value.stderr, 'STDERR', inferOutputLanguage(tool.name), chinese);
  if (isMemoryTool(tool.name)) {
    appendOutputBlock(blocks, value.output, toolPublicDetailText(chinese, 'memoryRecords'), 'text', chinese);
  }
  if (tool.name.trim().toLowerCase() === 'compute_details') {
    appendOutputBlock(blocks, value.details, toolPublicDetailText(chinese, 'computeNotes'), 'markdown', chinese);
  }
  for (const key of ['content', 'text', 'markdown']) {
    appendOutputBlock(
      blocks,
      value[key],
      isSkillTool(tool.name)
        ? toolPublicDetailText(chinese, 'analysisGuide')
        : toolPublicDetailText(chinese, 'detailedResult'),
      isSkillTool(tool.name) ? 'markdown' : 'text',
      chinese
    );
  }
  for (const key of ['result', 'data', 'environment', 'diagnostics']) {
    if (isRecord(value[key])) collectOutputBlocks(blocks, value[key], tool, chinese, depth + 1);
  }
}

function inferCodeLanguage(toolName: string): string {
  const normalized = toolName.toLowerCase();
  if (normalized.includes('python') || normalized === 'repl' || normalized === 'compute_provider') return 'python';
  if (normalized === 'r') return 'r';
  if (normalized === 'powershell') return 'powershell';
  if (normalized.includes('javascript') || normalized.includes('typescript')) return 'javascript';
  return 'text';
}

function inferCommandLanguage(toolName: string): string {
  return toolName.trim().toLowerCase() === 'powershell' ? 'powershell' : 'bash';
}

function inferOutputLanguage(toolName: string): string {
  return /(?:bash|shell|python|\br\b|repl|code_execution)/iu.test(toolName) ? 'console' : 'text';
}

function markdownFileLanguage(value: unknown): string {
  return typeof value === 'string' && /\.md(?:own)?$/iu.test(value.trim()) ? 'markdown' : 'text';
}

function isSkillTool(toolName: string): boolean {
  return /(?:^|_)(?:skill|load_skill)(?:_|$)/iu.test(toolName);
}

function isMemoryTool(toolName: string): boolean {
  return /^(?:read|search|write)_memory$/iu.test(toolName.trim());
}

function blockSummary(value: string, chinese: boolean): string {
  const lines = value.split('\n');
  const first = lines.find((line) => line.trim())?.trim() ?? '';
  if (lines.length <= 1) return truncate(first, 96);
  // The content is rendered immediately below this label. Repeating its first
  // line in the label makes structured connector evidence appear three times
  // (collection, label, content) and reads like generated filler.
  return toolPublicDetailText(chinese, 'lineCount', { count: lines.length });
}

function dedupeBlocks(blocks: ToolPublicDetailBlock[]): ToolPublicDetailBlock[] {
  const seen = new Set<string>();
  return blocks.filter((block) => {
    const key = `${block.label}\u0000${block.content}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value && typeof value === 'object' && !Array.isArray(value));
}

function truncate(value: string, maxCharacters: number): string {
  const characters = Array.from(value);
  return characters.length <= maxCharacters ? value : `${characters.slice(0, maxCharacters - 1).join('')}…`;
}
