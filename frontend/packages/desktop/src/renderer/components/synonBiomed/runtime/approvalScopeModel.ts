import type { SynonBiomedApprovalScope, SynonBiomedPendingInputRequest } from './runtimeOperationsModel';

const APPROVAL_SCOPE_COPY_ZH: Record<SynonBiomedApprovalScope, { label: string; hint: string }> = {
  once: { label: '本次', hint: '仅本次调用' },
  conversation: { label: '本会话', hint: '直到本次会话结束' },
  project: { label: '本项目', hint: '在本项目中记住' },
  always: { label: '全局', hint: '跨所有项目记住' },
};

const APPROVAL_SCOPE_COPY_EN: Record<SynonBiomedApprovalScope, { label: string; hint: string }> = {
  once: { label: 'Once', hint: 'Only this invocation' },
  conversation: { label: 'Conversation', hint: 'Until this conversation ends' },
  project: { label: 'Project', hint: 'Remember in this project' },
  always: { label: 'Always', hint: 'Remember across all projects' },
};

export const APPROVAL_SCOPE_COPY = APPROVAL_SCOPE_COPY_ZH;

export function getSynonBiomedApprovalScopeCopy(
  language = 'zh-CN'
): Record<SynonBiomedApprovalScope, { label: string; hint: string }> {
  return isEnglish(language) ? APPROVAL_SCOPE_COPY_EN : APPROVAL_SCOPE_COPY_ZH;
}

type ApprovalScopePolicy = {
  scopes: SynonBiomedApprovalScope[];
  defaultScope: SynonBiomedApprovalScope;
};

const ALL_SCOPES: SynonBiomedApprovalScope[] = ['once', 'conversation', 'project', 'always'];

const APPROVAL_SCOPE_POLICIES: Record<string, ApprovalScopePolicy> = {
  local_exec: { scopes: ALL_SCOPES, defaultScope: 'once' },
  agent_tool: { scopes: ['once', 'always'], defaultScope: 'once' },
  remote_exec: { scopes: ALL_SCOPES, defaultScope: 'once' },
  remote_read: { scopes: ALL_SCOPES, defaultScope: 'once' },
  mcp_tool: { scopes: ALL_SCOPES, defaultScope: 'once' },
  customize_mutation: { scopes: ALL_SCOPES, defaultScope: 'once' },
  byoc_submit: { scopes: ALL_SCOPES, defaultScope: 'once' },
  infer_submit: { scopes: ALL_SCOPES, defaultScope: 'once' },
  host_delete: { scopes: ['once', 'conversation'], defaultScope: 'once' },
  artifact_delete: { scopes: ['once'], defaultScope: 'once' },
  capability_install: { scopes: ['once'], defaultScope: 'once' },
  network: { scopes: ['always'], defaultScope: 'always' },
  host: { scopes: ['always'], defaultScope: 'always' },
};

const EXECUTION_HEADLINES_ZH: Record<string, string> = {
  python: '运行 Python 代码？',
  r: '运行 R 代码？',
  bash: '运行 Shell 命令？',
};

const EXECUTION_HEADLINES_EN: Record<string, string> = {
  python: 'Run Python code?',
  r: 'Run R code?',
  bash: 'Run a shell command?',
};

export function getSynonBiomedApprovalScopePolicy(
  kind: string,
  tool?: string | null,
  rememberable?: boolean
): ApprovalScopePolicy {
  if (rememberable === false) return { scopes: ['once'], defaultScope: 'once' };
  if (kind === 'local_exec' && tool?.toLowerCase() === 'software_runtime') {
    return { scopes: ['once'], defaultScope: 'once' };
  }
  return APPROVAL_SCOPE_POLICIES[kind] ?? { scopes: ALL_SCOPES, defaultScope: 'once' };
}

export function getSynonBiomedApprovalHeadline(request: SynonBiomedPendingInputRequest, language = 'zh-CN'): string {
  const english = isEnglish(language);
  if (request.kind === 'artifact_delete')
    return english ? 'Permanently delete these artifacts?' : '确认永久删除这些科研产物？';
  if (request.kind === 'local_exec') {
    const headlines = english ? EXECUTION_HEADLINES_EN : EXECUTION_HEADLINES_ZH;
    return (
      headlines[request.tool ?? ''] ??
      (english
        ? `Run ${(request.tool ?? 'this operation').replaceAll('_', ' ')}?`
        : `运行 ${(request.tool ?? '此操作').replaceAll('_', ' ')}？`)
    );
  }
  if (request.kind === 'remote_exec') return english ? 'Run this task on the remote host?' : '在远程主机上运行此任务？';
  if (request.kind === 'mcp_tool')
    return english ? `Call ${request.tool ?? 'connector'}?` : `调用 ${request.tool ?? '连接器'}？`;
  if (request.kind === 'agent_tool' && request.tool === 'edit_file' && request.target)
    return english ? `Write ${request.target}?` : `允许写入文件 ${request.target}？`;
  if (request.kind === 'network')
    return request.target
      ? english
        ? `Allow network access to ${request.target}?`
        : `允许访问网络域名 ${request.target}？`
      : english
        ? 'Allow network access?'
        : '允许网络访问？';
  if (request.kind === 'host')
    return request.target
      ? english
        ? `Allow host file access to ${request.target}?`
        : `允许访问主机路径 ${request.target}？`
      : english
        ? 'Allow host file access?'
        : '允许访问主机文件？';
  return english ? 'Allow this operation?' : '允许执行此操作？';
}

export function getSynonBiomedApprovalGrantHint(request: SynonBiomedPendingInputRequest, language = 'zh-CN'): string {
  const english = isEnglish(language);
  if (request.kind === 'artifact_delete' || request.kind === 'capability_install' || request.rememberable === false)
    return english
      ? 'This approval applies only to this request and cannot be remembered.'
      : '此次授权仅适用于当前请求，不会被记住。';
  if (request.kind === 'local_exec')
    return english
      ? `Scope applies to every ${request.tool ?? 'local execution'} invocation`
      : `范围适用于任何 ${request.tool ?? '本地执行'} 调用`;
  if (request.kind === 'mcp_tool')
    return english
      ? `Scope applies to every ${request.tool ?? 'connector tool'} invocation`
      : `范围适用于任何 ${request.tool ?? '连接器工具'} 调用`;
  if (request.kind === 'remote_exec')
    return english ? 'Scope applies to commands on this remote host' : '范围适用于该远程主机上的命令';
  return english
    ? 'Scope applies to similar operations. Revoke persistent grants in Settings > Permissions.'
    : '范围适用于同类操作；持久授权可在“设置 → 权限”中撤销';
}

function isEnglish(language: string): boolean {
  return language.toLowerCase().startsWith('en');
}
