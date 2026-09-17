export function isDeprecatedRuntimeAgentType(agentType?: string | null): boolean {
  return Boolean(agentType && agentType !== 'synonbiomed');
}

export function resolveSupportedConversationType(_backend?: string | null): 'acp' {
  return 'acp';
}
